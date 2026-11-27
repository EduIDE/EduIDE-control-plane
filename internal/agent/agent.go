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
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/logx"
)

const (
	// TokenHeader carries the instance token set by the HTTPRoute.
	TokenHeader   = "X-EduIDE-Instance-Token" //nolint:gosec // header name, not a credential
	maxClaimBytes = 16 << 10
	maxNotices    = 16
	maxPollSecs   = 60
	readyCacheTTL = time.Second
	// maxMilestones is the number of distinct milestone names: four set by the agent, four reported by the extension.
	maxMilestones = 8
)

// Milestones the agent sets itself.
const (
	msClaimReceived = "claim-received"
	msFirstRequest  = "first-request"
	msFirstUpgrade  = "first-websocket"
	msDataDelivered = "data-delivered"
)

// reportable are the milestone names the extension may post on the local listener.
var reportable = map[string]bool{"plugin-host-started": true, "workspace-ready": true, "clone-done": true, "first-input": true}

// Agent holds the in-memory session state. Launch data never leaves memory and is never logged.
type Agent struct {
	token   []byte
	idePort string
	log     *slog.Logger
	// PathPrefix is the public path of this instance ("/<instance>/"); cookie paths of the IDE are moved below it.
	PathPrefix string

	conns atomic.Int64

	mu           sync.Mutex
	changed      chan struct{} // closed and replaced on every version bump
	version      int
	session      string
	data         map[string]string
	notices      []agentapi.Notice
	lastActivity time.Time

	// milestones are the moments of the IDE start (ADR 0013): first report per name wins, so the list stays below maxMilestones.
	milestones []agentapi.Milestone
	// firstRequest, firstUpgrade and dataSent keep the proxy hot path off the mutex once the milestone is set.
	firstRequest, firstUpgrade, dataSent atomic.Bool

	readyMu   sync.Mutex
	readyAt   time.Time
	readyLast bool
	readySeen bool

	// git is nil while the feature is off (EnableGit).
	git *gitRoute

	// limit keeps probes and port scans from flooding the log.
	limit *logx.Limiter
}

// New returns an agent that guards and proxies to 127.0.0.1:idePort.
func New(token, idePort string, log *slog.Logger) *Agent {
	// the version starts at the start time so it never goes back when the agent container restarts
	return &Agent{token: []byte(token), idePort: idePort, log: log, changed: make(chan struct{}), version: int(time.Now().UnixMilli()), limit: logx.NewLimiter(10)}
}

func (a *Agent) tokenOK(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), a.token) == 1
}

// headerTokenOK requires exactly one token header value so duplicates cannot confuse the check.
func (a *Agent) headerTokenOK(h http.Header) bool {
	v := h.Values(TokenHeader)
	return len(v) == 1 && a.tokenOK(v[0])
}

// tokenRejected logs a failed token check without the token; at most 10 lines per minute and endpoint.
func (a *Agent) tokenRejected(endpoint string, r *http.Request) {
	if ok, n := a.limit.Allow("token/" + endpoint); ok {
		a.log.Info("token check failed", "endpoint", endpoint, "method", r.Method, "suppressed", n)
	}
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
			a.tokenRejected("control", r)
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
		if reachable := err == nil; !a.readySeen || a.readyLast != reachable {
			if reachable {
				a.log.Info("ide port reachable", "port", a.idePort)
			} else {
				a.log.Info("ide port unreachable", "port", a.idePort)
			}
		}
		a.readyLast, a.readyAt, a.readySeen = err == nil, time.Now(), true
		a.log.Debug("readiness probe", "reachable", err == nil)
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
		a.log.Info("claim refused: instance belongs to another session", "session", req.Session)
		http.Error(w, "claimed by another session", http.StatusConflict)
		return
	}
	if a.session == "" {
		// heartbeats before the claim do not count as session activity
		a.session, a.data, a.lastActivity = req.Session, req.Data, time.Time{}
		a.addMilestone(msClaimReceived, time.Now())
		if a.data == nil {
			a.data = map[string]string{}
		}
		a.bump()
		a.log.Info("claimed", "session", req.Session, "keys", sortedKeys(req.Data))
	} else {
		a.log.Info("re-claimed", "session", req.Session, "keys", sortedKeys(req.Data), "version", a.version)
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
	if len(a.milestones) > 0 {
		m := slices.Clone(a.milestones)
		st.Milestones = &m
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
	if n.At != nil && n.At.Before(time.Now()) {
		// The session has ended by then; showing it would only confuse the user.
		a.log.Info("expired notice dropped", "kind", string(n.Kind))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.mu.Lock()
	a.notices = append(a.notices, n)
	if len(a.notices) > maxNotices {
		a.notices = a.notices[len(a.notices)-maxNotices:]
	}
	a.bump()
	queued := len(a.notices)
	a.mu.Unlock()
	a.log.Info("notice queued", "kind", string(n.Kind), "queued", queued)
	w.WriteHeader(http.StatusNoContent)
}

// addMilestone records the first moment of a name. It must be called with a.mu held.
func (a *Agent) addMilestone(name string, at time.Time) {
	if len(a.milestones) >= maxMilestones || slices.ContainsFunc(a.milestones, func(m agentapi.Milestone) bool { return m.Name == name }) {
		return
	}
	a.milestones = append(a.milestones, agentapi.Milestone{Name: name, At: at.UTC()})
}

// markOnce records an agent milestone the first time it happens after the claim. Traffic to an idle pod does not count.
func (a *Agent) markOnce(done *atomic.Bool, name string) {
	if done.Load() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == "" {
		return
	}
	done.Store(true)
	a.addMilestone(name, time.Now())
}

func (a *Agent) milestone(w http.ResponseWriter, r *http.Request) {
	var m agentapi.Milestone
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&m); err != nil || !reportable[m.Name] ||
		m.At.IsZero() || m.At.After(time.Now().Add(time.Minute)) {
		http.Error(w, "invalid milestone", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.addMilestone(m.Name, m.At)
	a.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// LocalHandler serves the loopback endpoints for the eduide-session extension.
func (a *Agent) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/session", a.getSession)
	mux.HandleFunc("POST /v1/milestones", a.milestone)
	mux.HandleFunc("GET /v1/git/hosts", a.getGitHosts)
	if a.git != nil {
		mux.HandleFunc("/git/{host}/{rest...}", a.gitHandler)
	}
	mux.HandleFunc("POST /v1/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		a.log.Debug("heartbeat")
		a.mu.Lock()
		a.lastActivity = time.Now()
		// Activity cancels pending idle warnings, so a reloaded IDE does not show a stale one.
		if kept := slices.DeleteFunc(a.notices, func(n agentapi.Notice) bool { return n.Kind == agentapi.IdleWarning }); len(kept) != len(a.notices) {
			a.notices = kept
			a.bump()
		}
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
	// A notice can expire while it waits in the queue.
	n := slices.DeleteFunc(slices.Clone(a.notices), func(n agentapi.Notice) bool { return n.At != nil && n.At.Before(time.Now()) })
	if len(n) > 0 {
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
	if a.log.Enabled(r.Context(), slog.LevelDebug) {
		start := time.Now()
		defer func() {
			a.log.Debug("long poll finished", "after", after, "timeout_s", secs, "waited_ms", time.Since(start).Milliseconds())
		}()
	}
	for {
		s, changed := a.snapshot()
		if s.Version > after {
			a.delivered(s)
			writeJSON(w, s)
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			a.delivered(s)
			writeJSON(w, s)
			return
		case <-r.Context().Done():
			return
		}
	}
}

// delivered marks the first answer that carries the launch data.
func (a *Agent) delivered(s agentapi.LocalSession) {
	if s.Claimed {
		a.markOnce(&a.dataSent, msDataDelivered)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// sortedKeys lists the key names of the launch data; the values stay out of every log.
func sortedKeys(m map[string]string) []string {
	return slices.Sorted(maps.Keys(m))
}
