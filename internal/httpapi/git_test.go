package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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
	e.srv.cfg.GitAccounts = config.GitAccounts{Providers: []config.GitProvider{pc}, Approval: config.GitApproval{Mode: config.ApprovalWrite, MaxPerSession: 3}}
	e.srv.EnableGitAccounts(m)
	return e, &revokes
}

// startFlow calls connect as user and returns the state of the authorize URL.
func startFlow(t *testing.T, e *env, token, body string) (state string) {
	t.Helper()
	rec := e.do("POST", "/git/providers/gl/connect", token, body)
	if rec.Code != 200 {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Errorf("connect sets a cookie: %v", rec.Result().Cookies())
	}
	u, err := url.Parse(decode[GitConnectResponse](t, rec).AuthorizeUrl)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("redirect_uri") != "https://ide.example.org/git/callback" {
		t.Errorf("authorize query %v", q)
	}
	if strings.Contains(q.Get("state"), "alice") {
		t.Error("the state shows the user")
	}
	return q.Get("state")
}

// callback posts what the landing page received from the host.
func callback(e *env, token, state, code string) *httptest.ResponseRecorder {
	b := map[string]string{"state": state}
	if code != "" {
		b["code"] = code
	}
	return e.do("POST", "/git/callback", token, b)
}

func codeOf(t *testing.T, rec *httptest.ResponseRecorder) ProblemCode {
	t.Helper()
	return decode[Problem](t, rec).Code
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

func TestGitConnectFlow(t *testing.T) {
	e, _ := gitEnv(t)
	state := startFlow(t, e, "alice", "")
	rec := callback(e, "alice", state, gitCode)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if r := decode[GitCallbackResponse](t, rec); r.Provider != "gl" || r.Login != "gitty" || r.ReturnTo != "/" {
		t.Fatalf("%+v", r)
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
	state := startFlow(t, e, "alice", `{"returnTo":"/start?appDef=java"}`)
	if r := decode[GitCallbackResponse](t, callback(e, "alice", state, gitCode)); r.ReturnTo != "/start?appDef=java" {
		t.Errorf("%+v", r)
	}
	for _, bad := range []string{"//evil.example.org", "https://evil.example.org/", "evil", `/\evil`, "/a\nb", "/" + strings.Repeat("a", 600)} {
		b, _ := json.Marshal(map[string]string{"returnTo": bad})
		if rec := e.do("POST", "/git/providers/gl/connect", "alice", string(b)); rec.Code != 400 {
			t.Errorf("returnTo %q: %d", bad, rec.Code)
		}
	}
}

func TestGitCallbackFailures(t *testing.T) {
	e, _ := gitEnv(t)
	state := startFlow(t, e, "alice", "")
	for _, tc := range []struct {
		name, token, state, code string
		want                     ProblemCode
		status                   int
	}{
		{"garbage state", "alice", "x" + state, gitCode, CodeGitInvalidState, 400},
		{"no state", "alice", "", gitCode, CodeGitInvalidState, 400},
		// A state planted in the browser of another user: it names alice, the caller is bob.
		{"state of another user", "bob", state, gitCode, CodeGitInvalidState, 400},
		{"no code", "alice", state, "", CodeInvalidInput, 400},
		{"provider rejects the code", "alice", state, "wrong", CodeGitProviderError, 502},
	} {
		rec := callback(e, tc.token, tc.state, tc.code)
		if rec.Code != tc.status || codeOf(t, rec) != tc.want {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "nope") {
			t.Errorf("%s: provider text in the answer", tc.name)
		}
	}
	if gitSecrets(t, e) != 0 {
		t.Fatal("a failed flow stored a token")
	}
	for _, c := range []struct {
		errParam string
		want     ProblemCode
	}{{"access_denied", CodeGitDenied}, {"server_error", CodeGitProviderError}} {
		rec := e.do("POST", "/git/callback", "alice", map[string]string{"state": state, "error": c.errParam})
		if codeOf(t, rec) != c.want {
			t.Errorf("error=%s: %d %s", c.errParam, rec.Code, rec.Body)
		}
	}
}

func TestGitCallbackExpiredState(t *testing.T) {
	e, _ := gitEnv(t)
	state := startFlow(t, e, "alice", "")
	e.srv.now = func() time.Time { return time.Now().Add(gitaccount.StateTTL + time.Second) }
	if rec := callback(e, "alice", state, gitCode); codeOf(t, rec) != CodeGitStateExpired {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if gitSecrets(t, e) != 0 {
		t.Fatal("stored")
	}
}

func TestGitTwoParallelFlowsBothSucceed(t *testing.T) {
	e, _ := gitEnv(t)
	first := startFlow(t, e, "alice", "")
	second := startFlow(t, e, "alice", `{"returnTo":"/other"}`)
	if r := decode[GitCallbackResponse](t, callback(e, "alice", second, gitCode)); r.ReturnTo != "/other" {
		t.Errorf("%+v", r)
	}
	if rec := callback(e, "alice", first, gitCode); rec.Code != 200 {
		t.Errorf("the first flow failed after the second finished: %d %s", rec.Code, rec.Body)
	}
}

func TestGitCallbackStoresForTheCaller(t *testing.T) {
	e, _ := gitEnv(t)
	state := startFlow(t, e, "alice", "")
	callback(e, "alice", state, gitCode)
	if _, ok := e.srv.git.Connection(context.Background(), "alice", "gl"); !ok {
		t.Error("alice is not connected")
	}
	if _, ok := e.srv.git.Connection(context.Background(), "bob", "gl"); ok {
		t.Error("bob is connected")
	}
}

func TestGitConnectAndCallbackAreRateLimited(t *testing.T) {
	e, _ := gitEnv(t)
	e.srv.cfg.RateLimit.LaunchesPerMinute = 3 // a small bucket: a slow (race) run must not refill it between calls
	n := e.srv.cfg.RateLimit.LaunchesPerMinute
	var last *httptest.ResponseRecorder
	for range n + 1 {
		last = e.do("POST", "/git/providers/gl/connect", "alice", "")
	}
	if last.Code != 429 {
		t.Errorf("connect: %d", last.Code)
	}
	for range n + 1 {
		last = callback(e, "alice", "x", "y")
	}
	if last.Code != 429 {
		t.Errorf("callback: %d", last.Code)
	}
	if rec := e.do("POST", "/git/providers/gl/connect", "bob", ""); rec.Code != 200 {
		t.Errorf("another user is limited too: %d", rec.Code)
	}
}

func TestGitDisconnect(t *testing.T) {
	e, revokes := gitEnv(t)
	state := startFlow(t, e, "alice", "")
	callback(e, "alice", state, gitCode)
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
	for _, c := range []struct{ method, path string }{{"GET", "/git/providers"}, {"POST", "/git/providers/gl/connect"}, {"DELETE", "/git/providers/gl"},
		{"POST", "/git/callback"}, {"POST", "/git/preflight"}, {"GET", "/sessions/s/git/repositories"}, {"PUT", "/sessions/s/git/repositories"}, {"DELETE", "/sessions/s/git/repositories"}} {
		if rec := e.do(c.method, c.path, "", nil); rec.Code != 401 {
			t.Errorf("%s %s without token: %d", c.method, c.path, rec.Code)
		}
	}
}

func TestGitFeatureOff(t *testing.T) {
	e := newEnv(t)
	if list := decode[[]GitProvider](t, e.do("GET", "/git/providers", "alice", nil)); len(list) != 0 {
		t.Errorf("%+v", list)
	}
	for _, c := range []struct{ method, path string }{{"POST", "/git/providers/gl/connect"}, {"DELETE", "/git/providers/gl"},
		{"POST", "/git/callback"}, {"POST", "/git/preflight"}, {"GET", "/sessions/s/git/repositories"}} {
		body := any(nil)
		if c.method == "POST" && !strings.Contains(c.path, "connect") {
			body = `{"state":"x","gitUri":"https://a.example.org/r"}`
		}
		if rec := e.do(c.method, c.path, "alice", body); rec.Code != 404 {
			t.Errorf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	if st := decode[PublicStatus](t, e.do("GET", "/status", "", nil)); st.GitProviders != nil {
		t.Errorf("status shows git providers without the feature: %+v", st.GitProviders)
	}
}

func TestGitFlowDoesNotLogSecrets(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	e, _ := gitEnv(t)
	state := startFlow(t, e, "alice", "")
	callback(e, "alice", state, "wrong")
	state = startFlow(t, e, "alice", "")
	callback(e, "alice", state, gitCode)
	callback(e, "bob", state, gitCode)
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
