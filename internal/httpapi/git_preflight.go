package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/gitaccount"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitpolicy"
)

const preflightTimeout = 3 * time.Second

const (
	preflightOK       = GitPreflightResponseResultOk
	preflightConnect  = GitPreflightResponseResultConnect
	preflightNoAccess = GitPreflightResponseResultNoAccess
	preflightUnknown  = GitPreflightResponseResultUnknown
)

// GitPreflight tells the landing page whether a launch with this repository needs a step first. It asks the
// provider host once, with the user's credential if there is an account. The URL and the answer body are never
// logged or returned, and the credential goes only to the configured host on port 443.
func (s *Server) GitPreflight(w http.ResponseWriter, r *http.Request) {
	if s.git == nil {
		writeProblem(w, problem(CodeNotFound, "no such endpoint"))
		return
	}
	owner := user(r).Owner
	if !s.allowGit("preflight", owner) {
		writeProblem(w, problem(CodeRateLimited, "too many calls, retry shortly"))
		return
	}
	var req GitPreflightRequest
	s.limitBodyRead(w)
	if perr := decodeJSON(r.Body, &req); perr != nil {
		writeProblem(w, perr)
		return
	}
	u, perr := parseGitRepoURL(req.GitUri, false)
	if perr != nil {
		writeProblem(w, perr)
		return
	}
	var entry *gitaccount.Entry
	for _, p := range s.git.Providers() {
		// A port is not a provider: the proxy and the credential rule know the host on 443 only.
		if strings.EqualFold(p.Host, u.Hostname()) && u.Port() == "" {
			e, _ := s.git.Provider(p.Name)
			entry = &e
		}
	}
	if entry == nil {
		writeJSON(w, http.StatusOK, GitPreflightResponse{Result: GitPreflightResponseResultNotAProvider})
		return
	}
	out := GitPreflightResponse{Result: s.probe(r.Context(), owner, entry, u), Provider: &GitPreflightProvider{Name: entry.Config.Name, DisplayName: entry.Config.DisplayName}}
	writeJSON(w, http.StatusOK, out)
}

// parseGitRepoURL applies the rules of the launch key GIT_URI: https, a host, no credentials. The path must pass
// gitapi.CanonicalRepo, the same rule the git-proxy applies, so the result is the repository and nothing else. With strict a query or fragment is an error; without
// it they are dropped.
func parseGitRepoURL(raw string, strict bool) (*url.URL, *apiError) {
	bad := problem(CodeInvalidInput, "the repository must be an https URL without credentials, query or fragment")
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		strict && (u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#")) {
		return nil, bad
	}
	repo, ok := gitapi.CanonicalRepo(u.Path)
	if !ok {
		return nil, bad
	}
	return &url.URL{Scheme: "https", Host: u.Host, Path: repo}, nil
}

// probe sends the one request and maps the answer. Anything that is not a clear answer is "unknown", so a slow or
// broken host never blocks a launch.
func (s *Server) probe(ctx context.Context, owner string, e *gitaccount.Entry, u *url.URL) GitPreflightResponseResult {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	target := *u
	if s.gitProbeBase != nil {
		b, _ := url.Parse(s.gitProbeBase(e.Config.Host))
		target.Scheme, target.Host = b.Scheme, b.Host
	}
	target.Path += "/info/refs"
	target.RawQuery = "service=git-upload-pack"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return preflightUnknown
	}
	req.Header.Set("User-Agent", "eduide-preflight")
	connected := false
	rec, _, err := s.git.TokenForHost(ctx, owner, e.Config.Host)
	switch {
	case err == nil:
		connected = true
		req.SetBasicAuth(gitpolicy.UpstreamUser(e.Config.Type), rec.AccessToken)
	case !errors.Is(err, gitaccount.ErrNotConnected):
		return preflightUnknown // the refresh failed: not the user's repository problem
	}
	hc := e.HTTP
	if hc == nil {
		hc = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := hc.Do(req) // #nosec G704 -- the host is a configured provider, never free user input
	if err != nil {
		return preflightUnknown
	}
	_ = res.Body.Close() // the body is not needed and not read
	switch res.StatusCode {
	case http.StatusOK:
		return preflightOK
	case http.StatusUnauthorized:
		return preflightConnect // anonymous: private. With an account: the token no longer works, connect again.
	case http.StatusForbidden, http.StatusNotFound:
		if connected {
			return preflightNoAccess
		}
		return preflightConnect
	}
	return preflightUnknown
}
