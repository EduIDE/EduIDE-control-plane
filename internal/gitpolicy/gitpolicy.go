// Package gitpolicy is the policy side of the Git proxy (ADR 0023): the internal authorize endpoint of the
// service. The git-proxy asks it for every request; all decisions live here and nowhere else.
package gitpolicy

import (
	"context"
	"crypto/sha256"
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
	// perInstanceRate and burst bound the calls of one instance. The git-proxy caches answers for a few seconds and
	// calls once per repository, so only a burst over many repositories (40 Git dependencies, 30 submodules fetched
	// in parallel) comes near. The burst covers that; the git-proxy retries a 429 once. Wrong tokens and made-up
	// instance names never reach this bucket (see ServeHTTP and unknownLookupRate).
	perInstanceRate  = 20
	perInstanceBurst = 100
	instanceIndex    = "status.instance"
	// maxTracked bounds the limiter map and the token cache.
	maxTracked = 4096
	// tokenTTL is how long the hash of an instance token is reused; the token never changes while the instance lives.
	// A missing Secret is remembered for missTTL only, so a new instance is not refused for long.
	tokenTTL = 30 * time.Second
	missTTL  = 5 * time.Second
	// unknownLookupRate bounds the Secret reads for instances without a session. Instances with a session
	// (the real ones) do not draw on it, so a flood of made-up names cannot starve them.
	unknownLookupRate  = 20
	unknownLookupBurst = 100
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
	// ApprovalMode is config.ApprovalOff, ApprovalRead or ApprovalWrite; empty means off.
	ApprovalMode string
	// Approvals reads the repositories approved for a session; nil means none.
	Approvals interface {
		List(ctx context.Context, session string) ([]Approval, error)
	}

	// now is the clock of the token cache and the limiters; nil takes time.Now. Tests set it.
	now func() time.Time

	mu       sync.Mutex
	limiters map[string]*limiterEntry
	tokens   map[string]tokenEntry
	unknown  *rate.Limiter
}

// tokenEntry is what the service knows of the token Secret of one instance. It holds a hash, never the token.
type tokenEntry struct {
	hash    [sha256.Size]byte
	exists  bool
	expires time.Time
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
	// The token comes first: only a caller that holds the instance token draws on the budget of the instance.
	// Otherwise anyone who knows an instance name (it is in the IDE URL) could empty the bucket of that session.
	switch ok, err := h.tokenOK(r.Context(), req.Instance, bearer); {
	case errors.Is(err, errLookupBudget):
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "git authorize: instance token lookup failed", "error", err.Error())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	case !ok:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !h.allow(req.Instance) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
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

func (h *Handler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// allow charges the bucket of an authenticated instance. The map is bounded: quiet buckets are dropped first,
// and when it is still full the call is let through untracked (the caller holds a valid instance token).
func (h *Handler) allow(instance string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	if h.limiters == nil {
		h.limiters = map[string]*limiterEntry{}
	}
	e := h.limiters[instance]
	if e == nil {
		if len(h.limiters) >= maxTracked {
			for k, o := range h.limiters {
				if now.Sub(o.seen) > 5*time.Minute {
					delete(h.limiters, k)
				}
			}
			if len(h.limiters) >= maxTracked {
				return true
			}
		}
		e = &limiterEntry{l: rate.NewLimiter(perInstanceRate, perInstanceBurst)}
		h.limiters[instance] = e
	}
	e.seen = now
	return e.l.AllowN(now, 1)
}

var errLookupBudget = errors.New("too many token lookups for unknown instances")

// tokenOK compares the bearer with the instance token in constant time. An unknown instance is not ok.
// The hash of the token is cached, so wrong tokens for a real instance cost no API call at all.
func (h *Handler) tokenOK(ctx context.Context, instance, bearer string) (bool, error) {
	got := sha256.Sum256([]byte(bearer))
	now := h.clock()
	h.mu.Lock()
	e, hit := h.tokens[instance]
	h.mu.Unlock()
	if !hit || !now.Before(e.expires) {
		if l, err := h.Sessions.ByInstance(ctx, instance); err == nil && len(l) == 0 && !h.unknownBudget() {
			return false, errLookupBudget
		}
		var err error
		if e, err = h.lookupToken(ctx, instance, now); err != nil {
			return false, err
		}
	}
	return e.exists && subtle.ConstantTimeCompare(got[:], e.hash[:]) == 1, nil
}

func (h *Handler) unknownBudget() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unknown == nil {
		h.unknown = rate.NewLimiter(unknownLookupRate, unknownLookupBurst)
	}
	return h.unknown.Allow()
}

func (h *Handler) lookupToken(ctx context.Context, instance string, now time.Time) (tokenEntry, error) {
	var sec corev1.Secret
	err := h.Secrets.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: "ide-" + instance + "-token"}, &sec)
	e := tokenEntry{expires: now.Add(missTTL)}
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return tokenEntry{}, err
	default:
		if want := sec.Data[v1alpha1.InstanceTokenKey]; len(want) > 0 {
			e = tokenEntry{hash: sha256.Sum256(want), exists: true, expires: now.Add(tokenTTL)}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tokens == nil {
		h.tokens = map[string]tokenEntry{}
	}
	_, known := h.tokens[instance]
	if !known && len(h.tokens) >= maxTracked {
		for k, o := range h.tokens {
			if !now.Before(o.expires) {
				delete(h.tokens, k)
			}
		}
	}
	if known || len(h.tokens) < maxTracked {
		h.tokens[instance] = e
	}
	return e, nil
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
	var approved []Approval
	if h.ApprovalMode != "" && h.ApprovalMode != config.ApprovalOff && h.Approvals != nil {
		if approved, err = h.Approvals.List(ctx, sess.Name); err != nil {
			return gitapi.AuthorizeResponse{}, err
		}
	}
	if !mayUseCredential(h.ApprovalMode, sessionHost, sessionRepo, approved, req.Host, req.Repository, req.Kind) {
		_, connected := h.Accounts.Connection(ctx, owner, p.Name)
		// An approval can help only with an account, and only as far as the mode goes: a push needs mode write.
		approvable := connected && (h.ApprovalMode == config.ApprovalWrite || h.ApprovalMode == config.ApprovalRead && req.Kind == gitapi.KindRead)
		if req.Kind == gitapi.KindPush {
			if approvable {
				return deny(gitapi.ReasonPushRule, gitapi.ApproveMessage(h.LandingURL, sess.Name, req.Host, req.Repository, config.ApprovalWrite)), nil
			}
			s := shown
			if s == "" {
				s = "none"
			}
			return deny(gitapi.ReasonPushRule, "EduIDE only pushes to the repository this session was started with ("+s+
				"). Start a session from "+p.Host+"/"+gitapi.NormalizeRepo(req.Repository)+" to push there."), nil
		}
		// Anonymous: code in the session must not read the user's other private repositories.
		resp := deny(gitapi.ReasonAnonymousOther, "")
		resp.Allow, resp.Connected, resp.SessionRepository = true, connected, shown
		if approvable {
			resp.ApprovalMode = h.ApprovalMode
		}
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
		resp.Reason, resp.Connected, resp.Upstream = gitapi.ReasonOK, true, &gitapi.Upstream{Username: UpstreamUser(p.Type), Password: rec.AccessToken}
	}
	return resp, nil
}

// mayUseCredential answers whether the session may use the user's credential for a repository: always for the
// session repository, for an approved one as far as the approval mode and the approval allow (read, or push only
// with write). It is the one place that decides this.
func mayUseCredential(mode, sessionHost, sessionRepo string, approved []Approval, host, repo, kind string) bool {
	switch credentialAccess(mode, sessionHost, sessionRepo, approved, host, repo) {
	case config.ApprovalWrite:
		return true
	case config.ApprovalRead:
		return kind == gitapi.KindRead
	}
	return false
}

// UpstreamUser is the basic auth user name each platform expects with an OAuth access token.
func UpstreamUser(providerType string) string {
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
