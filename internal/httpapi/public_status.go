package httpapi

// Public status (U17, ADR 0021): GET /api/v1/status without a token. It computes everything inside the service
// and answers only with the fields of PublicStatus: no user, hash, session or workspace name, host, node,
// capacity limit, pool target or error detail. TestPublicStatusAllowlist guards that.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/time/rate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

const (
	publicCacheTTL       = 15 * time.Second
	publicPerMinute      = 100 // many students share one campus NAT address; the answer is cached anyway
	publicMaxLimiters    = 10000
	slowP95              = 60 * time.Second
	slowWindow           = 15 * time.Minute
	downWindow           = 5 * time.Minute
	downMinStarts        = 4
	workspacePendingDown = 2 * time.Minute
	signInEvery          = 30 * time.Second
	signInTimeout        = 3 * time.Second
)

// publicWithoutToken lets GET /status through without a bearer token and keeps the token check for all others.
func publicWithoutToken(authed func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		checked := authed(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Pattern == "GET "+basePath+"/status" {
				next.ServeHTTP(w, r)
				return
			}
			checked.ServeHTTP(w, r)
		})
	}
}

// clientIP is the last X-Forwarded-For entry (added by Envoy, the client cannot forge it), else the peer address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) allowIP(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.ipLimiters[ip]
	if !ok {
		if len(s.ipLimiters) >= publicMaxLimiters { // shortcut: reset instead of evicting, one burst of free requests per reset
			s.ipLimiters = map[string]*rate.Limiter{}
		}
		l = rate.NewLimiter(rate.Limit(float64(publicPerMinute)/60), publicPerMinute)
		s.ipLimiters[ip] = l
	}
	return l.Allow()
}

func worse(a, b ServiceLevel) ServiceLevel {
	rank := map[ServiceLevel]int{ServiceLevelOk: 0, ServiceLevelSlow: 1, ServiceLevelDown: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// startingLevel: down when more than half of the starts of the last 5 min failed (at least 4 starts), slow when
// the p95 of the starts of the last 15 min exceeds 60 s, else ok.
func (st *startStats) startingLevel(now time.Time) ServiceLevel {
	st.mu.Lock()
	defer st.mu.Unlock()
	var recent, failed int
	var durs []time.Duration
	for _, o := range st.outcomes {
		age := now.Sub(o.at)
		if age < 0 || age > slowWindow {
			continue
		}
		if age <= downWindow {
			recent++
			if o.failed {
				failed++
			}
		}
		if !o.failed {
			durs = append(durs, o.dur)
		}
	}
	if recent >= downMinStarts && failed*2 > recent {
		return ServiceLevelDown
	}
	if len(durs) > 0 {
		slices.Sort(durs)
		if durs[(len(durs)*95+99)/100-1] > slowP95 {
			return ServiceLevelSlow
		}
	}
	return ServiceLevelOk
}

func (s *Server) publicStatus(ctx context.Context) ([]byte, *apiError) {
	now := s.now()
	out := PublicStatus{UpdatedAt: now.UTC(), Templates: []PublicTemplate{}}
	out.Parts.Starting = s.hub.starts.startingLevel(now)
	out.Parts.SignIn, out.Parts.Workspaces = ServiceLevelOk, ServiceLevelOk
	if !s.signIn.Load() {
		out.Parts.SignIn = ServiceLevelDown
	}
	var wl v1alpha1.IDEWorkspaceList
	if err := s.r.List(ctx, &wl, client.InNamespace(s.cfg.Namespace)); err != nil {
		return nil, internalError(err)
	}
	for i := range wl.Items {
		w := &wl.Items[i]
		if w.DeletionTimestamp == nil && (w.Status.Phase == "" || w.Status.Phase == v1alpha1.WorkspacePending) && now.Sub(w.CreationTimestamp.Time) > workspacePendingDown {
			out.Parts.Workspaces = ServiceLevelDown
		}
	}
	tpls, perr := s.templates(ctx)
	if perr != nil {
		return nil, perr
	}
	ps, perr := s.pools(ctx)
	if perr != nil {
		return nil, perr
	}
	warm := map[string]bool{}
	for i := range ps {
		warm[ps[i].Spec.TemplateRef] = warm[ps[i].Spec.TemplateRef] || ps[i].Status.IdleReady > 0
	}
	for _, t := range tpls {
		pt := PublicTemplate{Name: t.Name, DisplayName: t.DisplayName, LogoUrl: t.LogoUrl, Warm: warm[t.Name]}
		if e := t.StartEstimate; e != nil {
			if e.None != nil {
				pt.StartEstimate.None = &PublicSeconds{Seconds: e.None.Seconds}
			}
			if e.Volume != nil {
				pt.StartEstimate.Volume = &PublicSeconds{Seconds: e.Volume.Seconds}
			}
		}
		out.Templates = append(out.Templates, pt)
	}
	if s.git != nil { // the landing page shows the connect buttons from this; nothing else about Git accounts is public
		gp := []PublicGitProvider{}
		for _, p := range s.git.Providers() {
			gp = append(gp, PublicGitProvider{Name: p.Name, Type: PublicGitProviderType(p.Type), Host: p.Host, DisplayName: p.DisplayName})
		}
		out.GitProviders = &gp
	}
	out.Overall = worse(worse(out.Parts.SignIn, out.Parts.Starting), out.Parts.Workspaces)
	b, err := json.Marshal(out)
	if err != nil {
		return nil, internalError(err)
	}
	return b, nil
}

func (s *Server) GetPublicStatus(w http.ResponseWriter, r *http.Request) {
	if !s.allowIP(clientIP(r)) {
		writeProblem(w, problem(CodeRateLimited, "too many requests, retry shortly"))
		return
	}
	s.pubMu.Lock()
	if s.pubBody == nil || s.now().Sub(s.pubAt) >= publicCacheTTL {
		b, perr := s.publicStatus(r.Context())
		if perr != nil {
			s.pubMu.Unlock()
			writeProblem(w, perr)
			return
		}
		s.pubBody, s.pubAt = b, s.now()
	}
	cached := s.pubBody
	s.pubMu.Unlock()
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "public, max-age=15")
	_, _ = w.Write(cached)
}

// RunSignInCheck asks the OIDC discovery of the issuer every 30 s and stores whether it answers.
// It returns when ctx ends.
func (s *Server) RunSignInCheck(ctx context.Context) {
	hc := &http.Client{Timeout: signInTimeout}
	check := func() { s.signIn.Store(discoveryReachable(ctx, hc, s.cfg.OIDC.IssuerURL)) }
	check()
	t := time.NewTicker(signInEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

func discoveryReachable(ctx context.Context, hc *http.Client, issuer string) bool {
	ctx, cancel := context.WithTimeout(ctx, signInTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return false
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
