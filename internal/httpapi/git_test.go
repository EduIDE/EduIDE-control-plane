package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitaccount"
)

const (
	gitAccess  = "gl-access-token-1"
	gitRefresh = "gl-refresh-token-1"
	gitCode    = "gl-auth-code-1"
)

// gitEnv has one fake GitLab provider named "gl".
func gitEnv(t *testing.T) (*env, *int) {
	t.Helper()
	revokes := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("code") != gitCode || r.PostForm.Get("code_verifier") == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"<b>nope</b>"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"` + gitAccess + `","refresh_token":"` + gitRefresh + `","expires_in":7200}`))
	})
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":7,"username":"gitty","name":"Gitty G"}`))
	})
	mux.HandleFunc("POST /oauth/revoke", func(w http.ResponseWriter, _ *http.Request) { revokes++ })
	fake := httptest.NewServer(mux)
	t.Cleanup(fake.Close)

	e := newEnv(t)
	pc := config.GitProvider{Name: "gl", Type: "gitlab", Host: "gitlab.example.org", DisplayName: "Example GitLab", ClientID: "cid", InactivityExpiry: &metav1.Duration{Duration: time.Hour}}
	keys, err := gitaccount.NewKeys([]byte(strings.Repeat("k", 40)))
	if err != nil {
		t.Fatal(err)
	}
	m := gitaccount.NewManager(e.k, testNS, keys, []gitaccount.Entry{{Config: pc, Client: gitaccount.NewProvider(pc, "secret", gitaccount.Endpoints{Web: fake.URL, API: fake.URL + "/api/v4"}, nil)}})
	e.srv.cfg.Hosts.Service = "ide.example.org"
	e.srv.cfg.GitAccounts = config.GitAccounts{Providers: []config.GitProvider{pc}}
	e.srv.EnableGitAccounts(m)
	return e, &revokes
}

// startFlow calls connect as user and returns the state and the cookie header of the flow.
func startFlow(t *testing.T, e *env, token, body string) (state, cookie string) {
	t.Helper()
	rec := e.do("POST", "/git/providers/gl/connect", token, body)
	if rec.Code != 200 {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(decode[GitConnectResponse](t, rec).AuthorizeUrl)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("redirect_uri") != "https://ide.example.org/api/v1/git/callback" {
		t.Errorf("authorize query %v", q)
	}
	c := rec.Result().Cookies()
	if len(c) != 1 {
		t.Fatalf("cookies %v", c)
	}
	if !c[0].HttpOnly || !c[0].Secure || c[0].SameSite != http.SameSiteLaxMode || c[0].Path != "/api/v1/git/callback" || c[0].MaxAge <= 0 {
		t.Errorf("flow cookie %+v", c[0])
	}
	return q.Get("state"), c[0].Name + "=" + c[0].Value
}

func callback(e *env, state, code, cookie string) *httptest.ResponseRecorder {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if code != "" {
		q.Set("code", code)
	}
	hdr := []string{}
	if cookie != "" {
		hdr = append(hdr, "Cookie", cookie)
	}
	return e.do("GET", "/git/callback?"+q.Encode(), "", nil, hdr...)
}

func gitSecrets(t *testing.T, e *env) int {
	t.Helper()
	var l corev1.SecretList
	if err := e.k.List(context.Background(), &l); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range l.Items {
		if strings.HasPrefix(s.Name, "git-account-") {
			n++
		}
	}
	return n
}

func location(t *testing.T, rec *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestGitConnectFlow(t *testing.T) {
	e, _ := gitEnv(t)
	state, cookie := startFlow(t, e, "alice", "")
	u := location(t, callback(e, state, gitCode, cookie))
	if u.Host != "ide.example.org" || u.Path != "/" || u.Query().Get("git") != "connected" || u.Query().Get("provider") != "gl" {
		t.Fatalf("redirect %s", u)
	}
	if gitSecrets(t, e) != 1 {
		t.Fatal("no token Secret")
	}
	list := decode[[]GitProvider](t, e.do("GET", "/git/providers", "alice", nil))
	if len(list) != 1 || !list[0].Connected || *list[0].Login != "gitty" || list[0].Host != "gitlab.example.org" || list[0].DisplayName != "Example GitLab" {
		t.Errorf("alice: %+v", list)
	}
	if list := decode[[]GitProvider](t, e.do("GET", "/git/providers", "bob", nil)); list[0].Connected || list[0].Login != nil {
		t.Errorf("bob: %+v", list)
	}
}

func TestGitConnectReturnTo(t *testing.T) {
	e, _ := gitEnv(t)
	state, cookie := startFlow(t, e, "alice", `{"returnTo":"/start?appDef=java"}`)
	u := location(t, callback(e, state, gitCode, cookie))
	if u.Path != "/start" || u.Query().Get("appDef") != "java" || u.Query().Get("git") != "connected" {
		t.Errorf("redirect %s", u)
	}
	for _, bad := range []string{"//evil.example.org", "https://evil.example.org/", "evil", `/\evil`, "/a\nb", "/" + strings.Repeat("a", 600)} {
		b := `{"returnTo":` + strings.ReplaceAll(`"`+strings.ReplaceAll(bad, "\n", `\n`)+`"`, `\evil`, `\\evil`) + `}`
		if rec := e.do("POST", "/git/providers/gl/connect", "alice", b); rec.Code != 400 {
			t.Errorf("returnTo %q: %d", bad, rec.Code)
		}
	}
}

func TestGitCallbackFailures(t *testing.T) {
	e, _ := gitEnv(t)
	state, cookie := startFlow(t, e, "alice", "")
	otherState, otherCookie := startFlow(t, e, "bob", "")
	_ = otherState
	p, sig, _ := strings.Cut(state, ".")
	for _, tc := range []struct {
		name, state, code, cookie, want string
	}{
		{"tampered state", p + "x." + sig, gitCode, cookie, "invalid-state"},
		{"no state", "", gitCode, cookie, "invalid-state"},
		{"no cookie", state, gitCode, "", "invalid-state"},
		// Alice's state in the browser of someone who started another flow: login CSRF.
		{"cookie of another flow", state, gitCode, otherCookie, "invalid-state"},
		{"no code", state, "", cookie, "provider-error"},
		{"provider rejects the code", state, "wrong", cookie, "exchange-failed"},
	} {
		u := location(t, callback(e, tc.state, tc.code, tc.cookie))
		if u.Query().Get("git") != "error" || u.Query().Get("code") != tc.want {
			t.Errorf("%s: %s", tc.name, u)
		}
		if strings.Contains(u.String(), "nope") {
			t.Errorf("%s: provider text in the redirect", tc.name)
		}
	}
	if gitSecrets(t, e) != 0 {
		t.Fatal("a failed flow stored a token")
	}
	// The provider reports an error itself.
	rec := e.do("GET", "/git/callback?state="+url.QueryEscape(state)+"&error=access_denied&error_description=%3Cscript%3E", "", nil, "Cookie", cookie)
	if u := location(t, rec); u.Query().Get("code") != "denied" || strings.Contains(u.String(), "script") {
		t.Errorf("denied: %s", u)
	}
	rec = e.do("GET", "/git/callback?state="+url.QueryEscape(state)+"&error=server_error", "", nil, "Cookie", cookie)
	if u := location(t, rec); u.Query().Get("code") != "provider-error" {
		t.Errorf("provider error: %s", u)
	}
}

func TestGitCallbackExpiredState(t *testing.T) {
	e, _ := gitEnv(t)
	state, cookie := startFlow(t, e, "alice", "")
	e.srv.now = func() time.Time { return time.Now().Add(gitaccount.StateTTL + time.Second) }
	if u := location(t, callback(e, state, gitCode, cookie)); u.Query().Get("code") != "expired" {
		t.Errorf("%s", u)
	}
	if gitSecrets(t, e) != 0 {
		t.Fatal("stored")
	}
}

func TestGitCallbackStoresForTheUserOfTheState(t *testing.T) {
	e, _ := gitEnv(t)
	state, cookie := startFlow(t, e, "alice", "")
	location(t, callback(e, state, gitCode, cookie))
	if _, ok := e.srv.git.Connection(context.Background(), "alice", "gl"); !ok {
		t.Error("alice is not connected")
	}
	if _, ok := e.srv.git.Connection(context.Background(), "bob", "gl"); ok {
		t.Error("bob is connected")
	}
}

func TestGitDisconnect(t *testing.T) {
	e, revokes := gitEnv(t)
	state, cookie := startFlow(t, e, "alice", "")
	location(t, callback(e, state, gitCode, cookie))
	if rec := e.do("DELETE", "/git/providers/gl", "bob", nil); rec.Code != 204 || gitSecrets(t, e) != 1 {
		t.Fatalf("bob must not touch alice's account: %d", rec.Code)
	}
	if rec := e.do("DELETE", "/git/providers/gl", "alice", nil); rec.Code != 204 || gitSecrets(t, e) != 0 || *revokes != 1 {
		t.Fatalf("%d secrets=%d revokes=%d", rec.Code, gitSecrets(t, e), *revokes)
	}
	if rec := e.do("DELETE", "/git/providers/gl", "alice", nil); rec.Code != 204 {
		t.Errorf("second disconnect: %d", rec.Code)
	}
	if rec := e.do("DELETE", "/git/providers/nope", "alice", nil); rec.Code != 404 {
		t.Errorf("unknown provider: %d", rec.Code)
	}
}

func TestGitAuth(t *testing.T) {
	e, _ := gitEnv(t)
	for _, c := range []struct{ method, path string }{{"GET", "/git/providers"}, {"POST", "/git/providers/gl/connect"}, {"DELETE", "/git/providers/gl"}} {
		if rec := e.do(c.method, c.path, "", nil); rec.Code != 401 {
			t.Errorf("%s %s without token: %d", c.method, c.path, rec.Code)
		}
	}
	// The callback needs no bearer token; a request without state is answered by a redirect, not by 401.
	if rec := e.do("GET", "/git/callback", "", nil); rec.Code != 302 {
		t.Errorf("callback: %d", rec.Code)
	}
}

func TestGitFeatureOff(t *testing.T) {
	e := newEnv(t)
	if list := decode[[]GitProvider](t, e.do("GET", "/git/providers", "alice", nil)); len(list) != 0 {
		t.Errorf("%+v", list)
	}
	for _, c := range []struct{ method, path, token string }{{"POST", "/git/providers/gl/connect", "alice"}, {"DELETE", "/git/providers/gl", "alice"}, {"GET", "/git/callback?state=x", ""}} {
		if rec := e.do(c.method, c.path, c.token, nil); rec.Code != 404 {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
}

func TestGitFlowDoesNotLogSecrets(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	e, _ := gitEnv(t)
	state, cookie := startFlow(t, e, "alice", "")
	callback(e, state, "wrong", cookie)
	state, cookie = startFlow(t, e, "alice", "")
	callback(e, state, gitCode, cookie)
	callback(e, state, gitCode, "")
	e.do("DELETE", "/git/providers/gl", "alice", nil)
	for _, s := range []string{gitAccess, gitRefresh, gitCode, state, "secret"} {
		if strings.Contains(buf.String(), s) {
			t.Errorf("log contains %q:\n%s", s, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "git account connected") {
		t.Error("nothing logged, the check proves nothing")
	}
}

func TestGitProviderHostIsAllowedForGitURI(t *testing.T) {
	cfg := testConfig()
	cfg.GitAccounts.Providers = []config.GitProvider{{Name: "gl", Type: "gitlab", Host: "gitlab.example.org"}}
	hosts := cfg.HTTPSURLHosts
	for host, want := range map[string]bool{"git.example.org": true, "gitlab.example.org": true, "GitLab.Example.org": true, "evil.example.org": false} {
		_, perr := validateData(javaTemplate(), map[string]string{"GIT_URI": "https://" + host + "/a/b.git"}, alice, hosts)
		if (perr == nil) != want {
			t.Errorf("%s: %v", host, perr)
		}
	}
	if len(testConfig().HTTPSURLHosts("GIT_URI")) != 1 {
		t.Error("allowedHosts alone changed")
	}
	// A provider host is no host for the Artemis URL: that server gets requests with credentials.
	if _, perr := validateData(&v1alpha1.IDETemplate{Spec: v1alpha1.IDETemplateSpec{InjectableKeys: []v1alpha1.InjectableKey{{Name: "ARTEMIS_URL", Format: v1alpha1.FormatHTTPSURL}}}}, map[string]string{"ARTEMIS_URL": "https://gitlab.example.org"}, alice, hosts); perr == nil {
		t.Error("a provider host is accepted for ARTEMIS_URL")
	}
}
