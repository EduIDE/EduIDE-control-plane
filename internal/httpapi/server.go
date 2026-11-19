// Package httpapi implements the REST API of the service (api/openapi/v1.yaml).
// Reads come from the controller-runtime cache; writes create only CRs and the data Secret.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/auth"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

type Server struct {
	cfg  *config.Config
	r    client.Reader
	w    client.Client
	hub  *Hub
	ping time.Duration
	// stopWait and stopPoll bound how long DeleteWorkspace waits for a stopping session (U14).
	stopWait, stopPoll time.Duration
	// salt keys the pseudonymous user id of spans (telemetry.userSaltFile); nil leaves the id out.
	salt       []byte
	titleStrip *regexp.Regexp

	// prom is nil without admin.prometheusURL (U16).
	prom *promClient
	now  func() time.Time

	mu            sync.Mutex
	limiters      map[string]*rate.Limiter // shortcut: one entry per user is never evicted, fine for thousands of users
	adminLimiters map[string]*rate.Limiter
	ipLimiters    map[string]*rate.Limiter // public status (U17), per client IP

	// quotaMu guards the storage quota cache; its refresh calls the API server, which must not block mu.
	quotaMu sync.Mutex
	quota   *StorageQuota
	quotaAt time.Time

	// public status (U17): the cached body and the state of the sign-in check. pubMu lets one request rebuild an
	// expired body while the others wait for it instead of rebuilding it, too.
	pubMu   sync.Mutex
	pubAt   time.Time
	pubBody []byte
	signIn  atomic.Bool
}

// NewServer reads through r (the cache) and writes through w. cfg must come from config.Parse.
func NewServer(cfg *config.Config, r client.Reader, w client.Client, hub *Hub, salt []byte) *Server {
	s := &Server{cfg: cfg, r: r, w: w, hub: hub, ping: 15 * time.Second, stopWait: 30 * time.Second, stopPoll: 200 * time.Millisecond, salt: salt, limiters: map[string]*rate.Limiter{}, adminLimiters: map[string]*rate.Limiter{}, ipLimiters: map[string]*rate.Limiter{}, now: time.Now}
	if cfg.Overview.TitleStripPattern != "" {
		s.titleStrip = regexp.MustCompile(cfg.Overview.TitleStripPattern) // config.Parse validated it
	}
	s.signIn.Store(true) // until the first check says otherwise
	if cfg.Admin.PrometheusURL != "" {
		s.prom = newPromClient(cfg.Admin.PrometheusURL)
	}
	return s
}

// Handler serves the API under /api/v1; every route needs a valid bearer token.
func (s *Server) Handler(v auth.Verifier) http.Handler {
	return secureAndProblemFallback(hstsValue(s.cfg), HandlerWithOptions(s, StdHTTPServerOptions{
		BaseURL:     basePath,
		Middlewares: []MiddlewareFunc{s.adminAuthz, publicWithoutToken(auth.Middleware(userRecorder{v})), s.adminGate, s.accessLog, metricsMiddleware}, // last is outermost
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeProblem(w, problem(CodeInvalidInput, "invalid request parameter"))
		},
	}))
}

// hstsValue covers the subdomains (instance and webview host) unless the config opts out. The Envoy OIDC
// redirects on the session hosts never reach the IDE route, so only the parent host can pin them.
func hstsValue(cfg *config.Config) string {
	if cfg.Security.HSTSIncludeSubdomains != nil && !*cfg.Security.HSTSIncludeSubdomains {
		return "max-age=31536000"
	}
	return "max-age=31536000; includeSubDomains"
}

// secureAndProblemFallback adds the transport security headers to every response and turns the
// plain text 404 and 405 of the mux (unknown path or method) into Problem Details.
func secureAndProblemFallback(hsts string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", hsts)
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(&fallbackWriter{ResponseWriter: w}, r)
	})
}

type fallbackWriter struct {
	http.ResponseWriter
	swallow bool
}

func (f *fallbackWriter) WriteHeader(code int) {
	if (code == http.StatusNotFound || code == http.StatusMethodNotAllowed) && strings.HasPrefix(f.Header().Get("Content-Type"), "text/plain") {
		f.swallow = true
		f.Header().Del("Content-Length")
		e := problem(CodeNotFound, "no such endpoint")
		if code == http.StatusMethodNotAllowed {
			e = problem(CodeInvalidInput, "method not allowed")
			e.status, e.title = code, http.StatusText(code)
		}
		writeProblem(f.ResponseWriter, e)
		return
	}
	f.ResponseWriter.WriteHeader(code)
}

func (f *fallbackWriter) Write(p []byte) (int, error) {
	if f.swallow {
		return len(p), nil
	}
	return f.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach Flush for SSE.
func (f *fallbackWriter) Unwrap() http.ResponseWriter { return f.ResponseWriter }

func user(r *http.Request) auth.User {
	u, _ := auth.FromContext(r.Context())
	return u
}

func (s *Server) allowLaunch(owner string) (ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.limiters[owner]
	if !ok {
		n := s.cfg.RateLimit.LaunchesPerMinute
		l = rate.NewLimiter(rate.Limit(float64(n)/60), n)
		s.limiters[owner] = l
	}
	ok = l.Allow()
	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		slog.Debug("launch rate limit", "user", s.userID(owner), "allowed", ok, "tokens_left", int(l.Tokens()))
	}
	return ok
}

// ownedSessions lists through rd: the cache, or s.w where a decision must not miss a session created just now.
func (s *Server) ownedSessions(ctx context.Context, rd client.Reader, owner string) ([]v1alpha1.IDESession, error) {
	var l v1alpha1.IDESessionList
	err := rd.List(ctx, &l, client.InNamespace(s.cfg.Namespace), client.MatchingLabels{v1alpha1.LabelOwnerHash: v1alpha1.OwnerHash(owner)})
	return slices.DeleteFunc(l.Items, func(x v1alpha1.IDESession) bool { return x.Spec.Owner != owner }), err
}

func (s *Server) ownedWorkspaces(ctx context.Context, owner string) ([]v1alpha1.IDEWorkspace, error) {
	// shortcut: scans all workspaces of the cache, so workspaces imported without owner-hash label are found too
	// No deep copy of every workspace per call (the overview stream runs this on each change); results are only read.
	var l v1alpha1.IDEWorkspaceList
	err := s.r.List(ctx, &l, client.InNamespace(s.cfg.Namespace), client.UnsafeDisableDeepCopy)
	return slices.DeleteFunc(l.Items, func(x v1alpha1.IDEWorkspace) bool { return x.Spec.Owner != owner }), err
}

// ownedSession returns not-found for sessions of other users, so names do not leak.
func (s *Server) ownedSession(ctx context.Context, name, owner string) (*v1alpha1.IDESession, *apiError) {
	var sess v1alpha1.IDESession
	err := s.r.Get(ctx, types.NamespacedName{Namespace: s.cfg.Namespace, Name: name}, &sess)
	if apierrors.IsNotFound(err) || err == nil && sess.Spec.Owner != owner {
		return nil, problem(CodeNotFound, "session not found")
	}
	if err != nil {
		return nil, internalError(err)
	}
	return &sess, nil
}

func (s *Server) ownedWorkspace(ctx context.Context, name, owner string) (*v1alpha1.IDEWorkspace, *apiError) {
	var ws v1alpha1.IDEWorkspace
	err := s.r.Get(ctx, types.NamespacedName{Namespace: s.cfg.Namespace, Name: name}, &ws)
	if apierrors.IsNotFound(err) || err == nil && ws.Spec.Owner != owner {
		return nil, problem(CodeWorkspaceNotFound, "workspace not found")
	}
	if err != nil {
		return nil, internalError(err)
	}
	return &ws, nil
}

// templates is the template list of GET /templates and of its event stream.
func (s *Server) templates(ctx context.Context) ([]Template, *apiError) {
	var ts v1alpha1.IDETemplateList
	var ps v1alpha1.IDEPoolList
	if err := s.r.List(ctx, &ts, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, internalError(err)
	}
	if err := s.r.List(ctx, &ps, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, internalError(err)
	}
	pooled, idleReady := map[string]bool{}, map[string]bool{}
	for _, p := range ps.Items {
		pooled[p.Spec.TemplateRef] = true
		idleReady[p.Spec.TemplateRef] = idleReady[p.Spec.TemplateRef] || p.Status.IdleReady > 0
	}
	now := s.now()
	out := []Template{}
	for i := range ts.Items {
		t := &ts.Items[i]
		tpl := templateToAPI(t, pooled[t.Name] && slices.Contains(t.Spec.Persistence.Allowed, v1alpha1.PersistenceNone))
		tpl.StartEstimate = s.hub.starts.startEstimates(t, idleReady[t.Name], now)
		out = append(out, tpl)
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (s *Server) ListTemplates(w http.ResponseWriter, r *http.Request) {
	out, perr := s.templates(r.Context())
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) WatchTemplates(w http.ResponseWriter, r *http.Request) {
	s.streamEvery(w, r, templatesKey, "templates", templateEventGap, func(ctx context.Context) (any, bool, *apiError) {
		out, perr := s.templates(ctx)
		return out, false, perr
	})
}

// template returns the template from the cache, nil when it is gone (running sessions survive their template).
func (s *Server) template(ctx context.Context, name string) *v1alpha1.IDETemplate {
	var t v1alpha1.IDETemplate
	if s.r.Get(ctx, types.NamespacedName{Namespace: s.cfg.Namespace, Name: name}, &t) != nil {
		return nil
	}
	return &t
}

func (s *Server) sessionAPI(ctx context.Context, sess *v1alpha1.IDESession) Session {
	return sessionToAPI(sess, s.template(ctx, sess.Spec.TemplateRef))
}

// workspaceAPI converts a workspace; busy are the names of the workspaces a session uses.
func (s *Server) workspaceAPI(ws *v1alpha1.IDEWorkspace, busy map[string]bool) Workspace {
	return workspaceToAPI(ws, busy[ws.Name], s.cfg.Workspaces.Grace.Duration)
}

// busyWorkspaces are the workspaces a session that has not failed uses (also while it is being deleted).
func busyWorkspaces(sessions []v1alpha1.IDESession) map[string]bool {
	busy := map[string]bool{}
	for i := range sessions {
		if ref := sessions[i].Spec.WorkspaceRef; ref != "" && sessions[i].Status.Phase != v1alpha1.SessionFailed {
			busy[ref] = true
		}
	}
	return busy
}

func (s *Server) ListSessions(w http.ResponseWriter, r *http.Request) {
	items, err := s.ownedSessions(r.Context(), s.r, user(r).Owner)
	if err != nil {
		writeProblem(w, internalError(err))
		return
	}
	out := []Session{}
	for i := range items {
		if items[i].DeletionTimestamp == nil {
			out = append(out, s.sessionAPI(r.Context(), &items[i]))
		}
	}
	slices.SortFunc(out, func(a, b Session) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetSession(w http.ResponseWriter, r *http.Request, name Name) {
	sess, perr := s.ownedSession(r.Context(), name, user(r).Owner)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAPI(r.Context(), sess))
}

func (s *Server) DeleteSession(w http.ResponseWriter, r *http.Request, name Name) {
	sess, perr := s.ownedSession(r.Context(), name, user(r).Owner)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	if err := s.w.Delete(r.Context(), sess); client.IgnoreNotFound(err) != nil {
		writeProblem(w, internalError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) WatchSession(w http.ResponseWriter, r *http.Request, name Name) {
	owner := user(r).Owner
	s.stream(w, r, v1alpha1.OwnerHash(owner), "session", func(ctx context.Context) (any, bool, *apiError) {
		sess, perr := s.ownedSession(ctx, name, owner)
		if perr != nil {
			return nil, false, perr
		}
		p := sess.Status.Phase
		return s.sessionAPI(ctx, sess), p == v1alpha1.SessionReady || p == v1alpha1.SessionFailed || sess.DeletionTimestamp != nil, nil
	})
}

func (s *Server) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	items, err := s.ownedWorkspaces(r.Context(), user(r).Owner)
	if err != nil {
		writeProblem(w, internalError(err))
		return
	}
	sessions, err := s.ownedSessions(r.Context(), s.r, user(r).Owner)
	if err != nil {
		writeProblem(w, internalError(err))
		return
	}
	busy := busyWorkspaces(sessions)
	out := []Workspace{}
	for i := range items {
		if items[i].DeletionTimestamp == nil {
			out = append(out, s.workspaceAPI(&items[i], busy))
		}
	}
	slices.SortFunc(out, func(a, b Workspace) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	// Each workspace is a volume: share the launch budget so a client cannot create them without bound.
	if !s.allowLaunch(u.Owner) {
		writeProblem(w, problem(CodeRateLimited, "too many requests, retry shortly"))
		return
	}
	var req WorkspaceRequest
	if perr := decodeJSON(r.Body, &req); perr != nil {
		writeProblem(w, perr)
		return
	}
	if perr := validateDisplay(req.Display); perr != nil {
		writeProblem(w, perr)
		return
	}
	var tpl v1alpha1.IDETemplate
	if err := s.r.Get(r.Context(), types.NamespacedName{Namespace: s.cfg.Namespace, Name: req.Template}, &tpl); err != nil {
		if apierrors.IsNotFound(err) {
			writeProblem(w, problem(CodeTemplateNotFound, "template not found"))
		} else {
			writeProblem(w, internalError(err))
		}
		return
	}
	if !slices.Contains(tpl.Spec.Persistence.Allowed, v1alpha1.PersistenceVolume) {
		writeProblem(w, problem(CodePersistence, "template does not allow persistence"))
		return
	}
	var d v1alpha1.Display
	if req.Display != nil {
		d = v1alpha1.Display{Title: deref(req.Display.Title), Source: deref(req.Display.Source)}
	}
	ws, perr := s.ensureWorkspace(r.Context(), workspaceName(u.Owner, tpl.Name, d.Source), u.Owner, tpl.Name, d)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	writeJSON(w, http.StatusCreated, s.workspaceAPI(ws, nil))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *Server) DeleteWorkspace(w http.ResponseWriter, r *http.Request, name Name) {
	owner := user(r).Owner
	ws, perr := s.ownedWorkspace(r.Context(), name, owner)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	// U14: stop the sessions on the workspace first; a session that is being deleted still holds the volume.
	if perr := s.stopSessionsOn(r.Context(), owner, name); perr != nil {
		writeProblem(w, perr)
		return
	}
	if err := s.w.Delete(r.Context(), ws); client.IgnoreNotFound(err) != nil {
		writeProblem(w, internalError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stopSessionsOn deletes the sessions on the workspace and waits until they are gone (at most s.stopWait).
func (s *Server) stopSessionsOn(ctx context.Context, owner, workspace string) *apiError {
	deadline := time.Now().Add(s.stopWait)
	for {
		sessions, err := s.ownedSessions(ctx, s.w, owner)
		if err != nil {
			return internalError(err)
		}
		left := 0
		for i := range sessions {
			if sessions[i].Spec.WorkspaceRef != workspace {
				continue
			}
			left++
			if sessions[i].DeletionTimestamp == nil {
				if err := s.w.Delete(ctx, &sessions[i]); client.IgnoreNotFound(err) != nil {
					return internalError(err)
				}
			}
		}
		if left == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return problem(CodeInternal, "the session on this workspace did not stop in time, retry")
		}
		select {
		case <-ctx.Done():
			return internalError(ctx.Err())
		case <-time.After(s.stopPoll):
		}
	}
}
