package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitaccount"
)

// EnableGitAccounts turns the Git account endpoints on. Without it they answer as if no provider were configured.
func (s *Server) EnableGitAccounts(m *gitaccount.Manager) { s.git = m }

// gitProvider looks the provider up; false when the feature is off or the name is unknown.
func (s *Server) gitProvider(name string, e *gitaccount.Entry) bool {
	if s.git == nil {
		return false
	}
	var ok bool
	*e, ok = s.git.Provider(name)
	return ok
}

// gitRedirectURI is the redirect URI of the OAuth applications: a landing page route on the service host, which the
// landing page server answers with the single-page app (any path without a file extension). The operator
// registers exactly this URL at the Git host. The API path /api/ would not work: the browser arrives with no token.
func (s *Server) gitRedirectURI() string {
	return "https://" + s.cfg.Hosts.Service + "/git/callback"
}

func (s *Server) ListGitProviders(w http.ResponseWriter, r *http.Request) {
	out := []GitProvider{}
	if s.git != nil {
		for _, p := range s.git.Providers() {
			gp := GitProvider{Name: p.Name, Type: GitProviderType(p.Type), Host: p.Host, DisplayName: p.DisplayName}
			if login, ok := s.git.Connection(r.Context(), user(r).Owner, p.Name); ok {
				gp.Connected, gp.Login = true, &login
			}
			out = append(out, gp)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ConnectGitProvider(w http.ResponseWriter, r *http.Request, name Name) {
	var e gitaccount.Entry
	if !s.gitProvider(name, &e) {
		writeProblem(w, problem(CodeNotFound, "no such git provider"))
		return
	}
	if !s.allowGit("connect", user(r).Owner) {
		writeProblem(w, problem(CodeRateLimited, "too many calls, retry shortly"))
		return
	}
	var req GitConnectRequest
	s.limitBodyRead(w)
	if raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1)); err != nil || len(raw) > maxBodyBytes {
		writeProblem(w, problem(CodeInvalidInput, "cannot read request body"))
		return
	} else if len(strings.TrimSpace(string(raw))) > 0 {
		if perr := decodeJSON(strings.NewReader(string(raw)), &req); perr != nil {
			writeProblem(w, perr)
			return
		}
	}
	returnTo := ""
	if req.ReturnTo != nil {
		if returnTo = *req.ReturnTo; !validReturnTo(returnTo) {
			writeProblem(w, problem(CodeInvalidInput, "returnTo must be a relative path on the landing page"))
			return
		}
	}
	keys := s.git.Keys()
	nonce := gitaccount.NewNonce()
	state := keys.SealState(gitaccount.State{Owner: user(r).Owner, Provider: name, Nonce: nonce, Expires: s.now().Add(gitaccount.StateTTL).Unix(), ReturnTo: returnTo})
	writeJSON(w, http.StatusOK, GitConnectResponse{AuthorizeUrl: e.Client.AuthorizeURL(s.gitRedirectURI(), state, gitaccount.ChallengeOf(keys.Verifier(nonce)))})
}

func (s *Server) DisconnectGitProvider(w http.ResponseWriter, r *http.Request, name Name) {
	var e gitaccount.Entry
	if !s.gitProvider(name, &e) {
		writeProblem(w, problem(CodeNotFound, "no such git provider"))
		return
	}
	if err := s.git.Disconnect(r.Context(), user(r).Owner, name); err != nil {
		writeProblem(w, internalError(err))
		return
	}
	slog.InfoContext(r.Context(), "git account disconnected", "provider", name, "user", s.userID(user(r).Owner))
	w.WriteHeader(http.StatusNoContent)
}

// GitCallback ends the flow. The landing page posts what the Git host sent to its redirect route. The sealed
// state names the user who started the flow and must equal the caller, so a state planted in another browser
// attaches nothing to anybody. Codes, state and tokens are never logged, provider text never reaches the client.
func (s *Server) GitCallback(w http.ResponseWriter, r *http.Request) {
	if s.git == nil {
		writeProblem(w, problem(CodeNotFound, "no such endpoint"))
		return
	}
	owner := user(r).Owner
	if !s.allowGit("callback", owner) {
		writeProblem(w, problem(CodeRateLimited, "too many calls, retry shortly"))
		return
	}
	var req GitCallbackRequest
	s.limitBodyRead(w)
	if perr := decodeJSON(r.Body, &req); perr != nil {
		writeProblem(w, perr)
		return
	}
	ctx := r.Context()
	fail := func(perr *apiError, provider string, err error) {
		a := []any{"code", string(perr.code), "provider", provider, "user", s.userID(owner)}
		if err != nil {
			a = append(a, "error", err.Error())
		}
		slog.WarnContext(ctx, "git connect failed", a...)
		writeProblem(w, perr)
	}
	st, err := s.git.Keys().OpenState(req.State, s.now())
	if err != nil {
		if errors.Is(err, gitaccount.ErrStateExpired) {
			fail(problem(CodeGitStateExpired, "the connect flow expired, start again"), "", nil)
			return
		}
		fail(problem(CodeGitInvalidState, "the state is not valid"), "", nil)
		return
	}
	var e gitaccount.Entry
	if st.Owner != owner || !s.gitProvider(st.Provider, &e) {
		fail(problem(CodeGitInvalidState, "the state is not valid"), st.Provider, nil)
		return
	}
	switch {
	case deref(req.Error) == "access_denied":
		fail(problem(CodeGitDenied, "the connection was refused at the Git host"), st.Provider, nil)
		return
	case req.Error != nil && *req.Error != "":
		fail(problem(CodeGitProviderError, "the Git host reported an error"), st.Provider, nil)
		return
	case req.Code == nil || *req.Code == "":
		fail(problem(CodeInvalidInput, "code is required"), st.Provider, nil)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tok, err := e.Client.Exchange(ctx, *req.Code, s.git.Keys().Verifier(st.Nonce), s.gitRedirectURI())
	if err != nil {
		fail(problem(CodeGitProviderError, "the Git host did not accept the code"), st.Provider, err)
		return
	}
	acc, err := e.Client.Account(ctx, tok.Access)
	if err != nil {
		fail(problem(CodeGitProviderError, "the Git host did not return the account"), st.Provider, err)
		return
	}
	if err := s.git.Save(ctx, owner, st.Provider, tok, acc); err != nil {
		if errors.Is(err, gitaccount.ErrBusy) {
			fail(problem(CodeRateLimited, "the account is being refreshed, retry shortly"), st.Provider, err)
			return
		}
		slog.WarnContext(ctx, "git connect failed", "code", "store", "provider", st.Provider, "error", err.Error())
		writeProblem(w, internalError(err))
		return
	}
	slog.InfoContext(ctx, "git account connected", "provider", st.Provider, "user", s.userID(owner))
	writeJSON(w, http.StatusOK, GitCallbackResponse{Provider: st.Provider, Login: acc.Login, ReturnTo: cmpOr(st.ReturnTo, "/")})
}

// validReturnTo accepts a relative path on the landing page: one leading slash, no host, no backslash, no control characters.
func validReturnTo(s string) bool {
	if s == "" || len(s) > 512 || s[0] != '/' || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\") {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil
}

// withGitIdentity makes commits in a session on a provider repository carry the identity of the connected account:
// the display name (or login) and the no-reply address, instead of the identity provider's name and e-mail. It
// changes nothing without a Git provider repository, without an account, or when the template does not inject the
// keys from the token.
func (s *Server) withGitIdentity(ctx context.Context, owner string, tpl *v1alpha1.IDETemplate, data map[string]string) map[string]string {
	if s.git == nil || data["GIT_URI"] == "" {
		return data
	}
	u, err := url.Parse(data["GIT_URI"])
	if err != nil || u.Scheme != "https" || u.Port() != "" {
		return data
	}
	id, ok := s.git.IdentityForHost(ctx, owner, u.Hostname())
	if !ok {
		return data
	}
	return gitIdentity(tpl, data, id)
}

// gitIdentity overrides GIT_USER and GIT_MAIL where the template takes them from the token. The no-reply address
// is empty for hosts whose format is unknown; GIT_MAIL then stays as it is.
func gitIdentity(tpl *v1alpha1.IDETemplate, data map[string]string, id gitaccount.Identity) map[string]string {
	out := maps.Clone(data)
	for _, k := range tpl.Spec.InjectableKeys {
		switch {
		case k.Name == "GIT_USER" && k.Source == v1alpha1.SourceUserName:
			out["GIT_USER"] = cmpOr(id.DisplayName, id.Login)
		case k.Name == "GIT_MAIL" && k.Source == v1alpha1.SourceUserEmail && id.NoReplyEmail != "":
			out["GIT_MAIL"] = id.NoReplyEmail
		}
	}
	return out
}
