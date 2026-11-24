// Package gitpolicy is the policy side of the Git proxy (ADR 0023): the internal authorize endpoint of the
// service. The git-proxy asks it for every request; all decisions live here and nowhere else.
package gitpolicy

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitaccount"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

const (
	maxBody = 4 << 10
	// perInstanceRate and burst bound the calls of one instance; the git-proxy caches answers, so real traffic stays far below.
	perInstanceRate  = 10
	perInstanceBurst = 30
	instanceIndex    = "status.instance"
)

// instanceRE is the shape of an instance id; it ends up in a Secret name.
var instanceRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,63}$`)

// Accounts is the part of gitaccount.Manager the policy needs.
type Accounts interface {
	Providers() []config.GitProvider
	TokenForHost(ctx context.Context, owner, host string) (gitaccount.Record, bool, error)
	// Connection reports whether the owner has an account for the provider, without counting as a use.
	Connection(ctx context.Context, owner, provider string) (login string, connected bool)
}

// Sessions finds the sessions of an instance.
type Sessions interface {
	ByInstance(ctx context.Context, instance string) ([]v1alpha1.IDESession, error)
}

// Handler serves POST /internal/v1/git/authorize.
type Handler struct {
	Sessions Sessions
	// Secrets reads the instance token and the launch data. It must read the API server (no Secret cache).
	Secrets   client.Reader
	Accounts  Accounts
	Namespace string
	// LandingURL is the landing page, named in the "connect" message.
	LandingURL string
	// Salt keys the user id of the answer; empty leaves it out.
	Salt []byte

	mu       sync.Mutex
	limiters map[string]*limiterEntry
}

type limiterEntry struct {
	l    *rate.Limiter
	seen time.Time
}

// ServeHTTP answers 401 for a missing or wrong token, 429 over the rate limit, 400 for a malformed request and 200
// with a decision otherwise. A decision with allow=false is a normal answer, not an error.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != gitapi.AuthorizePath {
		http.NotFound(w, r)
		return
	}
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || bearer == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req gitapi.AuthorizeRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody || json.Unmarshal(body, &req) != nil ||
		!instanceRE.MatchString(req.Instance) || req.Host == "" || req.Repository == "" ||
		(req.Kind != gitapi.KindRead && req.Kind != gitapi.KindPush) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !h.allow(req.Instance) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	switch ok, err := h.tokenOK(r.Context(), req.Instance, bearer); {
	case err != nil:
		slog.ErrorContext(r.Context(), "git authorize: instance token lookup failed", "error", err.Error())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	case !ok:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	resp, err := h.decide(r.Context(), req)
	if err != nil {
		slog.ErrorContext(r.Context(), "git authorize failed", "error", err.Error())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) allow(instance string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if h.limiters == nil {
		h.limiters = map[string]*limiterEntry{}
	}
	if len(h.limiters) > 1024 { // forget instances that were quiet for a while
		for k, e := range h.limiters {
			if now.Sub(e.seen) > 5*time.Minute {
				delete(h.limiters, k)
			}
		}
	}
	e := h.limiters[instance]
	if e == nil {
		e = &limiterEntry{l: rate.NewLimiter(perInstanceRate, perInstanceBurst)}
		h.limiters[instance] = e
	}
	e.seen = now
	return e.l.Allow()
}

// tokenOK compares the bearer with the instance token Secret in constant time. An unknown instance is not ok.
func (h *Handler) tokenOK(ctx context.Context, instance, bearer string) (bool, error) {
	var sec corev1.Secret
	err := h.Secrets.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: "ide-" + instance + "-token"}, &sec)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	want := sec.Data[v1alpha1.InstanceTokenKey]
	return len(want) > 0 && subtle.ConstantTimeCompare([]byte(bearer), want) == 1, nil
}

func (h *Handler) provider(host string) (config.GitProvider, bool) {
	for _, p := range h.Accounts.Providers() {
		if strings.EqualFold(p.Host, host) {
			return p, true
		}
	}
	return config.GitProvider{}, false
}

// session returns the Ready session of the instance, which is not being deleted.
func (h *Handler) session(ctx context.Context, instance string) (*v1alpha1.IDESession, error) {
	l, err := h.Sessions.ByInstance(ctx, instance)
	if err != nil {
		return nil, err
	}
	for i := range l {
		if s := &l[i]; s.Status.Phase == v1alpha1.SessionReady && s.DeletionTimestamp == nil {
			return s, nil
		}
	}
	return nil, nil
}

func (h *Handler) decide(ctx context.Context, req gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
	sess, err := h.session(ctx, req.Instance)
	if err != nil || sess == nil {
		return gitapi.AuthorizeResponse{Reason: gitapi.ReasonNotReady, Message: "This EduIDE session is not ready for Git access."}, err
	}
	owner := sess.Spec.Owner
	deny := func(reason, msg string) gitapi.AuthorizeResponse {
		r := gitapi.AuthorizeResponse{Reason: reason, Message: msg, Session: sess.Name}
		if len(h.Salt) > 0 {
			r.User = telemetry.UserID(h.Salt, owner)
		}
		return r
	}
	p, ok := h.provider(req.Host)
	if !ok {
		return deny(gitapi.ReasonUnknownHost, req.Host+" is not a Git host EduIDE connects to."), nil
	}
	sessionHost, sessionRepo, err := h.sessionRepository(ctx, sess)
	if err != nil {
		return gitapi.AuthorizeResponse{}, err
	}
	shown := ""
	if sessionRepo != "" {
		shown = sessionHost + "/" + sessionRepo
	}
	if !mayUseCredential(sessionHost, sessionRepo, req.Host, req.Repository) {
		if req.Kind == gitapi.KindPush {
			s := shown
			if s == "" {
				s = "none"
			}
			return deny(gitapi.ReasonPushRule, "EduIDE only pushes to the repository this session was started with ("+s+
				"). Start a session from "+p.Host+"/"+gitapi.NormalizeRepo(req.Repository)+" to push there."), nil
		}
		// Anonymous: code in the session must not read the user's other private repositories.
		_, connected := h.Accounts.Connection(ctx, owner, p.Name)
		resp := deny(gitapi.ReasonAnonymousOther, "")
		resp.Allow, resp.Connected, resp.SessionRepository = true, connected, shown
		return resp, nil
	}
	rec, _, err := h.Accounts.TokenForHost(ctx, owner, req.Host)
	resp := deny("", "")
	resp.Allow, resp.SessionRepository = true, shown
	switch {
	case errors.Is(err, gitaccount.ErrNotConnected):
		if req.Kind == gitapi.KindPush {
			return deny(gitapi.ReasonConnect, gitapi.ConnectMessage(p.DisplayName, h.LandingURL)), nil
		}
		resp.Reason = gitapi.ReasonAnonymous
	case err != nil:
		return gitapi.AuthorizeResponse{}, err
	default:
		resp.Reason, resp.Connected, resp.Upstream = gitapi.ReasonOK, true, &gitapi.Upstream{Username: upstreamUser(p.Type), Password: rec.AccessToken}
	}
	return resp, nil
}

// mayUseCredential answers whether the session may use the user's credential for a repository. Today that is
// the session repository only. A later list of approved repositories changes this function and nothing else.
func mayUseCredential(sessionHost, sessionRepo, host, repo string) bool {
	return sessionRepo != "" && gitapi.SameRepository(host, repo, sessionHost, sessionRepo)
}

// upstreamUser is the basic auth user name each platform expects with an OAuth access token.
func upstreamUser(providerType string) string {
	if providerType == config.GitLab {
		return "oauth2"
	}
	return "x-access-token"
}

// sessionRepository is host and normalized path of the session's GIT_URI, empty without one. The credentials
// in the URL (an Artemis VCS token) are dropped by SplitURL and never leave this function.
func (h *Handler) sessionRepository(ctx context.Context, s *v1alpha1.IDESession) (host, repo string, err error) {
	if s.Spec.DataSecretRef == "" {
		return "", "", nil
	}
	var sec corev1.Secret
	err = h.Secrets.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: s.Spec.DataSecretRef}, &sec)
	if apierrors.IsNotFound(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	host, repo, _ = gitapi.SplitURL(string(sec.Data["GIT_URI"]))
	return host, repo, nil
}

// IndexSessions registers the status.instance field index; call it before the cache starts.
func IndexSessions(ctx context.Context, c cache.Cache) error {
	return c.IndexField(ctx, &v1alpha1.IDESession{}, instanceIndex, func(o client.Object) []string {
		if i := o.(*v1alpha1.IDESession).Status.Instance; i != "" {
			return []string{i}
		}
		return nil
	})
}

// CacheSessions looks sessions up in the cache through the index of IndexSessions.
type CacheSessions struct {
	Cache     client.Reader
	Namespace string
}

func (s CacheSessions) ByInstance(ctx context.Context, instance string) ([]v1alpha1.IDESession, error) {
	var l v1alpha1.IDESessionList
	err := s.Cache.List(ctx, &l, client.InNamespace(s.Namespace), client.MatchingFields{instanceIndex: instance}, client.UnsafeDisableDeepCopy)
	return l.Items, err
}
