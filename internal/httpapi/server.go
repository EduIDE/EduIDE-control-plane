// Package httpapi implements the REST API of the service (api/openapi/v1.yaml).
// Reads come from the controller-runtime cache; writes create only CRs and the data Secret.
package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
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
	// salt keys the pseudonymous user id of spans (telemetry.userSaltFile); nil leaves the id out.
	salt       []byte
	titleStrip *regexp.Regexp

	mu       sync.Mutex
	limiters map[string]*rate.Limiter // shortcut: one entry per user is never evicted, fine for thousands of users
}

// NewServer reads through r (the cache) and writes through w. cfg must come from config.Parse.
func NewServer(cfg *config.Config, r client.Reader, w client.Client, hub *Hub, salt []byte) *Server {
	s := &Server{cfg: cfg, r: r, w: w, hub: hub, ping: 15 * time.Second, salt: salt, limiters: map[string]*rate.Limiter{}}
	if cfg.Overview.TitleStripPattern != "" {
		s.titleStrip = regexp.MustCompile(cfg.Overview.TitleStripPattern) // config.Parse validated it
	}
	return s
}

// Handler serves the API under /api/v1; every route needs a valid bearer token.
func (s *Server) Handler(v auth.Verifier) http.Handler {
	return secureAndProblemFallback(HandlerWithOptions(s, StdHTTPServerOptions{
		BaseURL:     basePath,
		Middlewares: []MiddlewareFunc{auth.Middleware(v), metricsMiddleware}, // last is outermost
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeProblem(w, problem(CodeInvalidInput, "invalid request parameter"))
		},
	}))
}

// secureAndProblemFallback adds the transport security headers to every response and turns the
// plain text 404 and 405 of the mux (unknown path or method) into Problem Details.
func secureAndProblemFallback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
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

func (s *Server) allowLaunch(owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.limiters[owner]
	if !ok {
		n := s.cfg.RateLimit.LaunchesPerMinute
		l = rate.NewLimiter(rate.Limit(float64(n)/60), n)
		s.limiters[owner] = l
	}
	return l.Allow()
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

func (s *Server) ListTemplates(w http.ResponseWriter, r *http.Request) {
	var ts v1alpha1.IDETemplateList
	var ps v1alpha1.IDEPoolList
	if err := s.r.List(r.Context(), &ts, client.InNamespace(s.cfg.Namespace)); err != nil {
		writeProblem(w, internalError(err))
		return
	}
	if err := s.r.List(r.Context(), &ps, client.InNamespace(s.cfg.Namespace)); err != nil {
		writeProblem(w, internalError(err))
		return
	}
	pooled := map[string]bool{}
	for _, p := range ps.Items {
		pooled[p.Spec.TemplateRef] = true
	}
	out := []Template{}
	for i := range ts.Items {
		t := &ts.Items[i]
		out = append(out, templateToAPI(t, pooled[t.Name] && slices.Contains(t.Spec.Persistence.Allowed, v1alpha1.PersistenceNone)))
	}
	slices.SortFunc(out, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, out)
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
			out = append(out, sessionToAPI(&items[i]))
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
	writeJSON(w, http.StatusOK, sessionToAPI(sess))
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
		return sessionToAPI(sess), p == v1alpha1.SessionReady || p == v1alpha1.SessionFailed || sess.DeletionTimestamp != nil, nil
	})
}

func (s *Server) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	items, err := s.ownedWorkspaces(r.Context(), user(r).Owner)
	if err != nil {
		writeProblem(w, internalError(err))
		return
	}
	out := []Workspace{}
	for i := range items {
		if items[i].DeletionTimestamp == nil {
			out = append(out, workspaceToAPI(&items[i]))
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
	writeJSON(w, http.StatusCreated, workspaceToAPI(ws))
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
	sessions, err := s.ownedSessions(r.Context(), s.w, owner)
	if err != nil {
		writeProblem(w, internalError(err))
		return
	}
	// A session that is being deleted still holds the volume.
	if slices.ContainsFunc(sessions, func(x v1alpha1.IDESession) bool { return x.Spec.WorkspaceRef == name }) {
		writeProblem(w, problem(CodeWorkspaceInUse, "a session uses this workspace"))
		return
	}
	if err := s.w.Delete(r.Context(), ws); client.IgnoreNotFound(err) != nil {
		writeProblem(w, internalError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
