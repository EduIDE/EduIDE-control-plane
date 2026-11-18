// Package agent implements the session pod sidecar (ADR 0006, api/agent/v1.yaml).
// It uses the standard library only to stay small.
package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
)

const (
	// TokenHeader carries the instance token set by the HTTPRoute.
	TokenHeader   = "X-EduIDE-Instance-Token" //nolint:gosec // header name, not a credential
	maxClaimBytes = 16 << 10
	maxNotices    = 16
	maxPollSecs   = 60
	readyCacheTTL = time.Second
)

// Agent holds the in-memory session state. Launch data never leaves memory and is never logged.
type Agent struct {
	token   []byte
	idePort string
	log     *slog.Logger

	conns atomic.Int64

	mu           sync.Mutex
	changed      chan struct{} // closed and replaced on every version bump
	version      int
	session      string
	data         map[string]string
	notices      []agentapi.Notice
	lastActivity time.Time

	readyMu   sync.Mutex
	readyAt   time.Time
	readyLast bool
}

// New returns an agent that guards and proxies to 127.0.0.1:idePort.
func New(token, idePort string, log *slog.Logger) *Agent {
	// the version starts at the start time so it never goes back when the agent container restarts
	return &Agent{token: []byte(token), idePort: idePort, log: log, changed: make(chan struct{}), version: int(time.Now().UnixMilli())}
}

func (a *Agent) tokenOK(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), a.token) == 1
}

// headerTokenOK requires exactly one token header value so duplicates cannot confuse the check.
func (a *Agent) headerTokenOK(h http.Header) bool {
	v := h.Values(TokenHeader)
	return len(v) == 1 && a.tokenOK(v[0])
}

// bump must be called with a.mu held.
func (a *Agent) bump() {
	a.version++
	close(a.changed)
	a.changed = make(chan struct{})
}

// ControlHandler serves the operator and kubelet endpoints.
func (a *Agent) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", a.readyz)
	mux.HandleFunc("POST /v1/claim", a.auth(a.claim))
	mux.HandleFunc("GET /v1/status", a.auth(a.status))
	mux.HandleFunc("POST /v1/notify", a.auth(a.notify))
	return mux
}

func (a *Agent) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !a.tokenOK(bearer) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (a *Agent) readyz(w http.ResponseWriter, _ *http.Request) {
	a.readyMu.Lock()
	if time.Since(a.readyAt) > readyCacheTTL {
		d := net.Dialer{Timeout: 500 * time.Millisecond}
		c, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", a.idePort))
		if err == nil {
			_ = c.Close()
		}
		a.readyLast, a.readyAt = err == nil, time.Now()
	}
	ok := a.readyLast
	a.readyMu.Unlock()
	if !ok {
		http.Error(w, "ide not ready", http.StatusServiceUnavailable)
	}
}

// DecodeClaim parses and validates a claim body, reading at most 16 KiB.
func DecodeClaim(r io.Reader) (agentapi.ClaimRequest, error) {
	var req agentapi.ClaimRequest
	body, err := io.ReadAll(io.LimitReader(r, maxClaimBytes+1))
	if err != nil {
		return req, err
	}
	if len(body) > maxClaimBytes {
		return req, errTooLarge
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, err
	}
	if req.Session == "" {
		return req, errors.New("session is required")
	}
	return req, nil
}

var errTooLarge = errors.New("claim too large")

func (a *Agent) claim(w http.ResponseWriter, r *http.Request) {
	req, err := DecodeClaim(r.Body)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errTooLarge) {
			code = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid claim", code)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session != "" && a.session != req.Session {
		http.Error(w, "claimed by another session", http.StatusConflict)
		return
	}
	if a.session == "" {
		// heartbeats before the claim do not count as session activity
		a.session, a.data, a.lastActivity = req.Session, req.Data, time.Time{}
		if a.data == nil {
			a.data = map[string]string{}
		}
		a.bump()
		a.log.Info("claimed", "session", req.Session)
	}
	w.WriteHeader(http.StatusOK)
}

func (a *Agent) status(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	st := agentapi.Status{Claimed: a.session != "", Ready: a.session != "", Connections: int(a.conns.Load())}
	if st.Claimed {
		s := a.session
		st.Session = &s
	}
	if !a.lastActivity.IsZero() {
		t := a.lastActivity
		st.LastActivity = &t
	}
	a.mu.Unlock()
	writeJSON(w, st)
}

func (a *Agent) notify(w http.ResponseWriter, r *http.Request) {
	var n agentapi.Notice
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClaimBytes)).Decode(&n); err != nil ||
		!n.Kind.Valid() || utf8.RuneCountInString(n.Message) > 1024 {
		http.Error(w, "invalid notice", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.notices = append(a.notices, n)
	if len(a.notices) > maxNotices {
		a.notices = a.notices[len(a.notices)-maxNotices:]
	}
	a.bump()
	a.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// LocalHandler serves the loopback endpoints for the eduide-session extension.
func (a *Agent) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/session", a.getSession)
	mux.HandleFunc("POST /v1/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		a.lastActivity = time.Now()
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (a *Agent) snapshot() (agentapi.LocalSession, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := agentapi.LocalSession{Version: a.version, Claimed: a.session != ""}
	if s.Claimed {
		d := make(map[string]string, len(a.data))
		for k, v := range a.data {
			d[k] = v
		}
		s.Data = &d
	}
	if len(a.notices) > 0 {
		n := append([]agentapi.Notice(nil), a.notices...)
		s.Notices = &n
	}
	return s, a.changed
}

func (a *Agent) getSession(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	secs := 30
	if v := r.URL.Query().Get("timeout"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			secs = min(n, maxPollSecs)
		}
	}
	timer := time.NewTimer(time.Duration(secs) * time.Second)
	defer timer.Stop()
	for {
		s, changed := a.snapshot()
		if s.Version > after {
			writeJSON(w, s)
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			writeJSON(w, s)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
