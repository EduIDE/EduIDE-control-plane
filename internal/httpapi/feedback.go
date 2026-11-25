package httpapi

// Feedback from the IDE (ADR 0023): POST /api/v1/feedback. The agent of a session pod calls it with the instance
// token. The service checks the token, rate limits per session owner, redacts, drops the user identity and
// forwards the text to the central receiver. Neither the text nor the owner is ever logged.

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

const (
	feedbackPerHour = 5
	feedbackWindow  = time.Hour
	feedbackTimeout = 10 * time.Second
)

var instanceRE = regexp.MustCompile(`^[a-z0-9]{1,40}$`)

func newFeedbackClient() *http.Client {
	return &http.Client{
		Timeout: feedbackTimeout,
		// A redirect would carry the installation key to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// EnableFeedback sets the installation key for the receiver. Call it before serving.
func (s *Server) EnableFeedback(key string) { s.feedbackKey = key }

// feedbackGate answers 404 while the deployment has feedback off, before the token check and any other check.
func (s *Server) feedbackGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Pattern == "POST "+basePath+"/feedback" && !s.cfg.Feedback.Enabled {
			writeProblem(w, problem(CodeNotFound, "no such endpoint"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// forwardedFeedback is what the receiver gets: the text and technical context, no user identity.
type forwardedFeedback struct {
	Kind         FeedbackRequestKind `json:"kind"`
	Title        string              `json:"title"`
	Description  string              `json:"description"`
	Context      any                 `json:"context"`
	Installation string              `json:"installation"`
	Host         string              `json:"host"`
}

func (s *Server) SendFeedback(w http.ResponseWriter, r *http.Request) {
	if !s.allowIP(clientIP(r)) {
		writeProblem(w, problem(CodeRateLimited, "too many requests, retry shortly"))
		return
	}
	owner, template, perr := s.feedbackOwner(r)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	var req FeedbackRequest
	s.limitBodyRead(w)
	if perr := decodeJSON(r.Body, &req); perr != nil {
		writeProblem(w, perr)
		return
	}
	req.Title, req.Description = strings.TrimSpace(req.Title), strings.TrimSpace(req.Description)
	if !req.Kind.Valid() || !between(req.Title, 1, 120) || !between(req.Description, 1, 5000) {
		writeProblem(w, problem(CodeInvalidInput, "kind, title or description is missing or too long"))
		return
	}
	at := s.now()
	if wait := s.reserveFeedback(owner, at); wait > 0 {
		e := problem(CodeRateLimited, "too many feedback messages, retry later")
		e.retryAfter = wait
		writeProblem(w, e)
		return
	}
	body, _ := json.Marshal(forwardedFeedback{
		// Redaction can lengthen the text; the receiver enforces the same limits.
		Kind: req.Kind, Title: clip(redact(req.Title), 120), Description: clip(redact(req.Description), 5000),
		Context: map[string]string{
			"version":       cleanContext(req.Context.Version),
			"appDefinition": cleanContext(template), // from the session, whatever the client sent
			"browser":       cleanContext(req.Context.Browser),
		},
		Installation: s.cfg.Deployment, Host: s.cfg.Hosts.Service,
	})
	status, retry, err := s.forwardFeedback(r, body)
	if status == http.StatusTooManyRequests {
		s.refundFeedback(owner, at)
		e := problem(CodeRateLimited, "the feedback receiver is busy, retry later")
		e.retryAfter = retry
		writeProblem(w, e)
		return
	}
	if err != nil {
		s.refundFeedback(owner, at) // the user gets the slot back when the receiver was the problem
		slog.Warn("feedback not forwarded", "error", err.Error())
		writeProblem(w, problem(CodeUpstream, "feedback could not be delivered"))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

var contextDrop = regexp.MustCompile(`[^A-Za-z0-9 ._()/+-]`)

// cleanContext keeps a technical context value harmless: redacted, a small character set, at most 100 characters.
func cleanContext(s string) string {
	return clip(contextDrop.ReplaceAllString(redact(s), ""), 100)
}

func between(s string, lo, hi int) bool {
	n := utf8.RuneCountInString(s)
	return n >= lo && n <= hi
}

// feedbackOwner returns the owner and template of the claimed session of the instance after checking the instance
// token. The cached session lookup comes first, so requests for unknown instances cost no API server call.
func (s *Server) feedbackOwner(r *http.Request) (owner, template string, _ *apiError) {
	deny := problem(CodeUnauthorized, "instance token or instance is not valid")
	instance := r.Header.Get("X-EduIDE-Instance")
	bearer, ok := bearerOf(r)
	if !instanceRE.MatchString(instance) || !ok {
		return "", "", deny
	}
	// shortcut: scans the sessions of the cache; fine for a few thousand sessions, add an index on status.instance beyond that
	var l v1alpha1.IDESessionList
	if err := s.r.List(r.Context(), &l, client.InNamespace(s.cfg.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return "", "", internalError(err)
	}
	for i := range l.Items {
		if l.Items[i].Status.Instance == instance && l.Items[i].Spec.Owner != "" {
			owner, template = l.Items[i].Spec.Owner, l.Items[i].Spec.TemplateRef
			break
		}
	}
	if owner == "" {
		return "", "", deny
	}
	var sec corev1.Secret
	// Secrets are not in the cache, read them directly.
	err := s.w.Get(r.Context(), types.NamespacedName{Namespace: s.cfg.Namespace, Name: "ide-" + instance + "-token"}, &sec)
	if apierrors.IsNotFound(err) {
		return "", "", deny
	}
	if err != nil {
		return "", "", internalError(err)
	}
	want := sec.Data[v1alpha1.InstanceTokenKey]
	if len(want) == 0 || subtle.ConstantTimeCompare([]byte(bearer), want) != 1 {
		return "", "", deny
	}
	return owner, template, nil
}

func bearerOf(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) <= len(p) || h[:len(p)] != p {
		return "", false
	}
	return h[len(p):], true
}

// reserveFeedback takes one of the owner's slots and returns 0, or the seconds until one is free.
// shortcut: the limit is per replica, so n replicas allow n times 5 per hour, and entries of owners that stay away are only
// dropped on the owner's next call; move to a shared store (or a Lease) when this matters.
func (s *Server) reserveFeedback(owner string, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept []time.Time
	for _, t := range s.feedbackLimits[owner] {
		if now.Sub(t) < feedbackWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= feedbackPerHour {
		s.feedbackLimits[owner] = kept
		return max(1, int((feedbackWindow-now.Sub(kept[0])).Seconds())+1)
	}
	s.feedbackLimits[owner] = append(kept, now)
	return 0
}

func (s *Server) refundFeedback(owner string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.feedbackLimits[owner]
	for i, t := range l {
		if t.Equal(at) {
			s.feedbackLimits[owner] = append(l[:i:i], l[i+1:]...)
			break
		}
	}
	if len(s.feedbackLimits[owner]) == 0 {
		delete(s.feedbackLimits, owner)
	}
}

// forwardFeedback returns the status of the receiver (0 when unreachable) and its Retry-After in seconds.
func (s *Server) forwardFeedback(r *http.Request, body []byte) (status, retryAfter int, _ error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.cfg.Feedback.ReceiverURL, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.feedbackKey)
	resp, err := s.feedbackClient.Do(req) // #nosec G704 -- URL comes from the operator's config
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		retryAfter, _ = strconv.Atoi(resp.Header.Get("Retry-After"))
		return resp.StatusCode, retryAfter, &receiverStatusError{resp.StatusCode}
	}
	return resp.StatusCode, 0, nil
}

type receiverStatusError struct{ code int }

func (e *receiverStatusError) Error() string { return "receiver answered " + strconv.Itoa(e.code) }

var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[email]"},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.?[A-Za-z0-9_-]*`), "[token]"},
	{regexp.MustCompile(`\b(?:gh[pousr]_|github_pat_|glpat-)[A-Za-z0-9_-]{10,}`), "[token]"},
	{regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`), "[token]"},
	{regexp.MustCompile(`[A-Za-z0-9+/_-]{40,}={0,2}`), "[token]"},
	{regexp.MustCompile(`\b0\d{7}\b`), "[matriculation number]"},
}

// redact removes what must not end up in a public issue. It errs on the side of removing too much.
func redact(s string) string {
	for _, x := range redactions {
		s = x.re.ReplaceAllString(s, x.with)
	}
	return s
}
