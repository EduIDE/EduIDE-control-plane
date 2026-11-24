package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/gitaccount"
)

// flowCookie binds a connect flow to the browser that started it (ADR 0023). It holds the nonce of the state.
const flowCookie = "eduide-git-flow"

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

func (s *Server) gitRedirectURI() string {
	return "https://" + s.cfg.Hosts.Service + basePath + "/git/callback"
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
	state := keys.Sign(gitaccount.State{Owner: user(r).Owner, Provider: name, Nonce: nonce, Expires: s.now().Add(gitaccount.StateTTL).Unix(), ReturnTo: returnTo})
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Value: nonce, Path: basePath + "/git/callback", MaxAge: int(gitaccount.StateTTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
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

// GitCallback ends the flow. Every outcome is a redirect to the landing page with a result code; the provider's
// error text, the code, the state and the tokens are never shown or logged.
func (s *Server) GitCallback(w http.ResponseWriter, r *http.Request, params GitCallbackParams) {
	if s.git == nil {
		writeProblem(w, problem(CodeNotFound, "no such endpoint"))
		return
	}
	ctx := r.Context()
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Path: basePath + "/git/callback", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	fail := func(returnTo, provider, code string, err error) {
		a := []any{"code", code, "provider", provider}
		if err != nil {
			a = append(a, "error", err.Error())
		}
		slog.WarnContext(ctx, "git connect failed", a...)
		s.gitRedirect(w, r, returnTo, url.Values{"git": {"error"}, "code": {code}})
	}
	st, err := s.git.Keys().ParseState(deref(params.State), s.now())
	if err != nil {
		code := "invalid-state"
		if errors.Is(err, gitaccount.ErrStateExpired) {
			code = "expired"
		}
		fail("", "", code, nil)
		return
	}
	// The cookie proves this browser started the flow. Without it an attacker could hand a victim a callback URL
	// that attaches the victim's Git account to the attacker's user.
	if c, err := r.Cookie(flowCookie); err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(st.Nonce)) != 1 {
		fail("", st.Provider, "invalid-state", nil)
		return
	}
	var e gitaccount.Entry
	if !s.gitProvider(st.Provider, &e) {
		fail(st.ReturnTo, st.Provider, "invalid-state", nil)
		return
	}
	switch {
	case deref(params.Error) == "access_denied":
		fail(st.ReturnTo, st.Provider, "denied", nil)
		return
	case params.Error != nil || params.Code == nil || *params.Code == "":
		fail(st.ReturnTo, st.Provider, "provider-error", nil)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tok, err := e.Client.Exchange(ctx, *params.Code, s.git.Keys().Verifier(st.Nonce), s.gitRedirectURI())
	if err != nil {
		fail(st.ReturnTo, st.Provider, "exchange-failed", err)
		return
	}
	acc, err := e.Client.Account(ctx, tok.Access)
	if err != nil {
		fail(st.ReturnTo, st.Provider, "account-failed", err)
		return
	}
	if err := s.git.Save(ctx, st.Owner, st.Provider, tok, acc); err != nil {
		fail(st.ReturnTo, st.Provider, "store-failed", err)
		return
	}
	slog.InfoContext(ctx, "git account connected", "provider", st.Provider, "user", s.userID(st.Owner))
	s.gitRedirect(w, r, st.ReturnTo, url.Values{"git": {"connected"}, "provider": {st.Provider}})
}

// gitRedirect sends the browser to returnTo (default "/") on the landing page with the result in the query.
func (s *Server) gitRedirect(w http.ResponseWriter, r *http.Request, returnTo string, result url.Values) {
	u := &url.URL{Scheme: "https", Host: s.cfg.Hosts.Service, Path: "/"}
	if p, err := url.Parse(returnTo); err == nil && validReturnTo(returnTo) {
		u.Path, u.RawQuery = p.Path, p.RawQuery
	}
	q := u.Query()
	for k, v := range result {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, u.String(), http.StatusFound)
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
