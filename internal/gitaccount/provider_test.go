package gitaccount

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

const (
	tokAccess  = "access-secret-1"
	tokRefresh = "refresh-secret-1"
	clientSec  = "client-secret-xyz"
)

// fakeHost answers the OAuth and API endpoints of GitHub and GitLab.
type fakeHost struct {
	srv           *httptest.Server
	refreshCalls  atomic.Int32
	revokeCalls   atomic.Int32
	revokeStatus  int
	onRevoke      func() // runs while a revoke is being answered
	refreshAnswer func(n int32) (int, string)
	// challenge, when set, is the PKCE challenge the authorization code was issued for: the token endpoints
	// accept the code only with a verifier that hashes to it.
	challenge string
	lastForm  url.Values
	lastBasic [2]string
}

func newFakeHost(t *testing.T) *fakeHost {
	f := &fakeHost{revokeStatus: 200}
	f.refreshAnswer = func(n int32) (int, string) {
		return 200, fmt.Sprintf(`{"access_token":"access-%d","refresh_token":"refresh-%d","expires_in":7200}`, n, n)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.lastForm = r.PostForm
		if r.PostForm.Get("code") != "good" || !f.verifierOK(r.PostForm.Get("code_verifier")) {
			_, _ = w.Write([]byte(`{"error":"bad_verification_code","error_description":"<script>alert(1)</script>"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"` + tokAccess + `"}`))
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+tokAccess {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"id":42,"login":"octo","name":"Octo Cat"}`))
	})
	mux.HandleFunc("DELETE /applications/cid/token", func(w http.ResponseWriter, r *http.Request) {
		f.revokeCalls.Add(1)
		f.lastBasic[0], f.lastBasic[1], _ = r.BasicAuth()
		w.WriteHeader(f.revokeStatus)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.lastForm = r.PostForm
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			if r.PostForm.Get("code") != "good" || !f.verifierOK(r.PostForm.Get("code_verifier")) {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"` + tokAccess + `","refresh_token":"` + tokRefresh + `","expires_in":7200}`))
		case "refresh_token":
			n := f.refreshCalls.Add(1)
			st, body := f.refreshAnswer(n)
			w.WriteHeader(st)
			_, _ = w.Write([]byte(body))
		}
	})
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":7,"username":"gitty","name":""}`))
	})
	mux.HandleFunc("POST /oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		f.revokeCalls.Add(1)
		_ = r.ParseForm()
		f.lastForm = r.PostForm
		if f.onRevoke != nil {
			f.onRevoke()
		}
		w.WriteHeader(f.revokeStatus)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHost) verifierOK(verifier string) bool {
	return f.challenge == "" || ChallengeOf(verifier) == f.challenge
}

func (f *fakeHost) provider(typ, host string) Provider {
	cfg := config.GitProvider{Name: typ, Type: typ, Host: host, ClientID: "cid"}
	return NewProvider(cfg, clientSec, Endpoints{Web: f.srv.URL, API: f.srv.URL + map[string]string{"gitlab": "/api/v4"}[typ]}, nil)
}

func TestAuthorizeURL(t *testing.T) {
	f := newFakeHost(t)
	for _, tc := range []struct{ typ, scope, extra string }{
		{"github", "repo", ""},
		{"gitlab", "read_repository write_repository read_user", "response_type=code"},
	} {
		u, err := url.Parse(f.provider(tc.typ, "git.example.org").AuthorizeURL("https://ide.example.org/cb", "st", "chal"))
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		want := map[string]string{"client_id": "cid", "redirect_uri": "https://ide.example.org/cb", "state": "st", "code_challenge": "chal", "code_challenge_method": "S256", "scope": tc.scope}
		for k, v := range want {
			if q.Get(k) != v {
				t.Errorf("%s: %s = %q, want %q", tc.typ, k, q.Get(k), v)
			}
		}
		if tc.extra != "" && !strings.Contains(u.RawQuery, tc.extra) {
			t.Errorf("%s: missing %s", tc.typ, tc.extra)
		}
		if strings.Contains(u.RawQuery, clientSec) {
			t.Errorf("%s: authorize URL carries the client secret", tc.typ)
		}
	}
}

func TestExchangeAndAccount(t *testing.T) {
	f := newFakeHost(t)
	ctx := context.Background()
	for _, tc := range []struct {
		typ, host string
		refresh   bool
		login     string
		email     string
		display   string
	}{
		{"github", "github.com", false, "octo", "42+octo@users.noreply.github.com", "Octo Cat"},
		// No guessed address for GitHub Enterprise Server: the launch falls back to the identity provider values.
		{"github", "ghe.example.org", false, "octo", "", "Octo Cat"},
		{"gitlab", "gitlab.example.org", true, "gitty", "7-gitty@users.noreply.gitlab.example.org", "gitty"},
	} {
		p := f.provider(tc.typ, tc.host)
		tok, err := p.Exchange(ctx, "good", "verifier", "https://ide.example.org/cb")
		if err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		if tok.Access != tokAccess || (tok.Refresh != "") != tc.refresh || (!tok.Expiry.IsZero()) != tc.refresh {
			t.Errorf("%s: token %+v", tc.typ, tok)
		}
		if f.lastForm.Get("code_verifier") != "verifier" || f.lastForm.Get("client_secret") != clientSec {
			t.Errorf("%s: form %v", tc.typ, f.lastForm)
		}
		acc, err := p.Account(ctx, tok.Access)
		if err != nil || acc.Login != tc.login || acc.NoReplyEmail != tc.email || acc.DisplayName != tc.display {
			t.Errorf("%s: account %+v %v", tc.typ, acc, err)
		}
	}
}

func TestExchangeErrors(t *testing.T) {
	f := newFakeHost(t)
	for _, typ := range []string{"github", "gitlab"} {
		_, err := f.provider(typ, "x.example.org").Exchange(context.Background(), "bad", "v", "r")
		if !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: %v", typ, err)
		}
		if err != nil && strings.Contains(err.Error(), "script") {
			t.Errorf("%s: provider text in the error: %v", typ, err)
		}
	}
	// A transport error carries no URL.
	f.srv.Close()
	_, err := f.provider("gitlab", "x.example.org").Exchange(context.Background(), "good", "v", "r")
	if err == nil || strings.Contains(err.Error(), f.srv.URL) {
		t.Errorf("want an error without the URL, got %v", err)
	}
}

func TestRefresh(t *testing.T) {
	f := newFakeHost(t)
	ctx := context.Background()
	if _, err := f.provider("github", "github.com").Refresh(ctx, "x"); !errors.Is(err, ErrNoRefresh) {
		t.Errorf("github: %v", err)
	}
	gl := f.provider("gitlab", "gitlab.example.org")
	tok, err := gl.Refresh(ctx, tokRefresh)
	if err != nil || tok.Refresh != "refresh-1" || f.lastForm.Get("refresh_token") != tokRefresh {
		t.Errorf("rotation: %+v %v %v", tok, err, f.lastForm)
	}
	f.refreshAnswer = func(int32) (int, string) { return 400, `{"error":"invalid_grant"}` }
	if _, err := gl.Refresh(ctx, "old"); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("invalid_grant: %v", err)
	}
	f.refreshAnswer = func(int32) (int, string) { return 500, `oops` }
	if _, err := gl.Refresh(ctx, "old"); err == nil || errors.Is(err, ErrInvalidGrant) {
		t.Errorf("server error: %v", err)
	}
}

func TestRevoke(t *testing.T) {
	f := newFakeHost(t)
	ctx := context.Background()
	for _, tc := range []struct {
		typ    string
		status int
		ok     bool
	}{{"github", 204, true}, {"github", 404, true}, {"github", 422, true}, {"github", 500, false}, {"gitlab", 200, true}, {"gitlab", 500, false}} {
		f.revokeStatus = tc.status
		err := f.provider(tc.typ, "x.example.org").Revoke(ctx, Token{Access: tokAccess})
		if (err == nil) != tc.ok {
			t.Errorf("%s %d: %v", tc.typ, tc.status, err)
		}
	}
	f.revokeStatus = 204
	_ = f.provider("github", "github.com").Revoke(ctx, Token{Access: tokAccess})
	if f.lastBasic != [2]string{"cid", clientSec} {
		t.Errorf("github revoke needs basic auth with client id and secret, got %v", f.lastBasic)
	}
	f.revokeStatus = 200
	_ = f.provider("gitlab", "x.example.org").Revoke(ctx, Token{Access: tokAccess})
	if f.lastForm.Get("token") != tokAccess || f.lastForm.Get("client_secret") != clientSec {
		t.Errorf("gitlab revoke form %v", f.lastForm)
	}
}

func TestDefaultEndpoints(t *testing.T) {
	for _, tc := range []struct {
		p        config.GitProvider
		web, api string
	}{
		{config.GitProvider{Type: "github", Host: "github.com"}, "https://github.com", "https://api.github.com"},
		{config.GitProvider{Type: "github", Host: "ghe.example.org"}, "https://ghe.example.org", "https://ghe.example.org/api/v3"},
		{config.GitProvider{Type: "gitlab", Host: "gitlab.example.org"}, "https://gitlab.example.org", "https://gitlab.example.org/api/v4"},
	} {
		if e := DefaultEndpoints(tc.p); e.Web != tc.web || e.API != tc.api {
			t.Errorf("%+v: %+v", tc.p, e)
		}
	}
}

func TestExchangeChecksPKCE(t *testing.T) {
	f := newFakeHost(t)
	f.challenge = ChallengeOf("the-verifier")
	for _, typ := range []string{"github", "gitlab"} {
		p := f.provider(typ, "x.example.org")
		if _, err := p.Exchange(context.Background(), "good", "the-verifier", "r"); err != nil {
			t.Errorf("%s: right verifier: %v", typ, err)
		}
		if _, err := p.Exchange(context.Background(), "good", "another", "r"); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("%s: wrong verifier: %v", typ, err)
		}
	}
}
