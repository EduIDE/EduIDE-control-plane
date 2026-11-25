// Package feedback is the central feedback receiver: it accepts feedback from the services of the installations
// and files it as issues in a triage repository through a GitHub App. It stores nothing.
package feedback

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/time/rate"
)

const (
	maxBody       = 32 << 10
	maxTitle      = 120
	maxDesc       = 5000
	maxContext    = 300
	maxHost       = 253
	hourlyLimit   = 60
	installPatStr = `^[a-z0-9][a-z0-9.-]{0,62}$`
)

var installationName = regexp.MustCompile(installPatStr)

// Request is the body of POST /v1/feedback.
type Request struct {
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Context     struct {
		Version       string `json:"version"`
		AppDefinition string `json:"appDefinition"`
		Browser       string `json:"browser"`
	} `json:"context"`
	Installation string `json:"installation"`
	Host         string `json:"host"`
}

// IssueCreator files an issue and returns its number.
type IssueCreator interface {
	CreateIssue(ctx context.Context, in Issue) (int, error)
}

// Server is the HTTP handler of the receiver.
type Server struct {
	// KeysDir holds one file per installation: the file name is the installation name, the content its key.
	// A mounted Secret has this shape. Keys are read per request, so a new key needs no restart.
	KeysDir string
	GitHub  IssueCreator
	Limit   int // requests per hour and installation, default 60

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /v1/feedback", s.post)
	return mux
}

func reply(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": msg})
}

func (s *Server) key(installation string) []byte {
	if !installationName.MatchString(installation) {
		return nil
	}
	// installationName allows no path separators, so the name cannot leave KeysDir.
	b, err := os.ReadFile(filepath.Join(s.KeysDir, installation)) //nolint:gosec // G304: name checked above
	if err != nil {
		return nil
	}
	return []byte(strings.TrimSpace(string(b)))
}

func (s *Server) allow(installation string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limiters == nil {
		s.limiters = map[string]*rate.Limiter{}
	}
	l, ok := s.limiters[installation]
	if !ok {
		n := s.Limit
		if n <= 0 {
			n = hourlyLimit
		}
		l = rate.NewLimiter(rate.Every(time.Hour/time.Duration(n)), n)
		s.limiters[installation] = l
	}
	return l.Allow()
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	var req Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// An unparsable body names no installation, so there is nothing to authenticate against yet.
		reply(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Unknown installation and wrong key look the same, so the answer does not reveal which names exist.
	want := s.key(req.Installation)
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(want) == 0 || !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
		slog.Warn("feedback rejected", "installation", logName(req.Installation), "result", "unauthorized")
		reply(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if msg := validate(&req); msg != "" {
		slog.Info("feedback rejected", "installation", req.Installation, "kind", req.Kind, "result", "invalid")
		reply(w, http.StatusBadRequest, msg)
		return
	}
	if !s.allow(req.Installation) {
		slog.Info("feedback rejected", "installation", req.Installation, "kind", req.Kind, "result", "rate_limited")
		w.Header().Set("Retry-After", "60")
		reply(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	n, err := s.GitHub.CreateIssue(r.Context(), buildIssue(&req))
	if err != nil {
		slog.Error("feedback not filed", "installation", req.Installation, "kind", req.Kind, "result", "github_error", "error", err.Error())
		reply(w, http.StatusBadGateway, "upstream error")
		return
	}
	slog.Info("feedback filed", "installation", req.Installation, "kind", req.Kind, "result", "created", "issue", n)
	reply(w, http.StatusAccepted, "accepted")
}

// logName keeps an unauthenticated, attacker-chosen name from flooding or forging log lines.
func logName(s string) string {
	if installationName.MatchString(s) {
		return s
	}
	return "invalid"
}

func validate(r *Request) string {
	if r.Kind != "bug" && r.Kind != "feature" {
		return "kind must be bug or feature"
	}
	if n := utf8.RuneCountInString(r.Title); n < 1 || n > maxTitle || strings.TrimSpace(r.Title) == "" {
		return "title must have 1 to 120 characters"
	}
	if n := utf8.RuneCountInString(r.Description); n < 1 || n > maxDesc || strings.TrimSpace(r.Description) == "" {
		return "description must have 1 to 5000 characters"
	}
	for _, f := range []string{r.Context.Version, r.Context.AppDefinition, r.Context.Browser} {
		if utf8.RuneCountInString(f) > maxContext {
			return "context field too long"
		}
	}
	if len(r.Host) > maxHost {
		return "host too long"
	}
	return ""
}

// oneLine turns control characters into spaces, so a field cannot break out of a title or a table row.
func oneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s))
}

func cell(s string) string {
	s = strings.ReplaceAll(oneLine(s), "|", `\|`)
	if s == "" {
		return "-"
	}
	return s
}

func buildIssue(r *Request) Issue {
	label := "enhancement"
	if r.Kind == "bug" {
		label = "bug"
	}
	body := fmt.Sprintf("%s\n\n---\n\n| Context | |\n|---|---|\n| installation | %s |\n| host | %s |\n| version | %s |\n| app definition | %s |\n| browser | %s |\n| kind | %s |\n",
		r.Description, cell(r.Installation), cell(r.Host), cell(r.Context.Version), cell(r.Context.AppDefinition), cell(r.Context.Browser), r.Kind)
	return Issue{Title: "[" + r.Installation + "] " + oneLine(r.Title), Body: body, Labels: []string{"via-ide", "needs-triage", label}}
}

// Check fails fast at startup when the keys directory is missing.
func (s *Server) Check() error {
	_, err := os.ReadDir(s.KeysDir)
	return err
}
