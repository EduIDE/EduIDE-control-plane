package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitaccount"
)

func connectAs(t *testing.T, e *env, token string) {
	t.Helper()
	if rec := callback(e, token, startFlow(t, e, token, ""), gitCode); rec.Code != 200 {
		t.Fatalf("connect %s: %d %s", token, rec.Code, rec.Body)
	}
}

// fakeRepoHost answers info/refs like a Git host: "pub" is public, "priv" needs the token, "gone" is 404 with
// the token, "boom" is 502, "moved" redirects to another host.
func fakeRepoHost(t *testing.T) (srv *httptest.Server, other *atomic.Int32, seen *[]string) {
	t.Helper()
	other = &atomic.Int32{}
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { other.Add(1) }))
	t.Cleanup(elsewhere.Close)
	paths := []string{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		user, pass, _ := r.BasicAuth()
		switch {
		case strings.HasPrefix(r.URL.Path, "/o/pub/"):
			w.WriteHeader(200)
		case strings.HasPrefix(r.URL.Path, "/o/priv/"):
			if user == "oauth2" && pass == gitAccess {
				w.WriteHeader(200)
				return
			}
			w.WriteHeader(401)
		case strings.HasPrefix(r.URL.Path, "/o/gone/"):
			w.WriteHeader(404)
		case strings.HasPrefix(r.URL.Path, "/o/moved/"):
			http.Redirect(w, r, elsewhere.URL+"/x", http.StatusFound)
		default:
			w.WriteHeader(502)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, other, &paths
}

func TestGitPreflight(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	e, _ := gitEnv(t)
	host, other, seen := fakeRepoHost(t)
	e.srv.gitProbeBase = func(string) string { return host.URL }
	connectAs(t, e, "alice")
	pre := func(token, uri string) (GitPreflightResponse, int) {
		rec := e.do("POST", "/git/preflight", token, map[string]string{"gitUri": uri})
		if rec.Code != 200 {
			return GitPreflightResponse{}, rec.Code
		}
		return decode[GitPreflightResponse](t, rec), 200
	}
	h := "https://gitlab.example.org/"
	for _, tc := range []struct {
		name, token, uri string
		want             GitPreflightResponseResult
		provider         bool
	}{
		{"public, anonymous", "bob", h + "o/pub", GitPreflightResponseResultOk, true},
		{"private, no account", "bob", h + "o/priv", GitPreflightResponseResultConnect, true},
		{"private, account", "alice", h + "o/priv.git", GitPreflightResponseResultOk, true},
		{"trailing slash", "alice", h + "o/priv/", GitPreflightResponseResultOk, true},
		{"not found with account", "alice", h + "o/gone", GitPreflightResponseResultNoAccess, true},
		{"not found without account", "bob", h + "o/gone", GitPreflightResponseResultConnect, true},
		{"host fails", "alice", h + "o/boom", GitPreflightResponseResultUnknown, true},
		{"redirect is not followed", "alice", h + "o/moved", GitPreflightResponseResultUnknown, true},
		{"not a provider", "alice", "https://other.example.org/o/priv", GitPreflightResponseResultNotAProvider, false},
		{"a port is not the provider", "alice", "https://gitlab.example.org:8443/o/priv", GitPreflightResponseResultNotAProvider, false},
	} {
		got, code := pre(tc.token, tc.uri)
		if code != 200 || got.Result != tc.want || (got.Provider != nil) != tc.provider {
			t.Errorf("%s: %d %+v", tc.name, code, got)
		}
	}
	if got, _ := pre("alice", h+"o/priv"); got.Provider.Name != "gl" || got.Provider.DisplayName != "Example GitLab" {
		t.Errorf("%+v", got.Provider)
	}
	if other.Load() != 0 {
		t.Error("the redirect was followed")
	}
	for _, p := range *seen {
		if !strings.HasSuffix(p, "/info/refs?service=git-upload-pack") {
			t.Errorf("probe path %s", p)
		}
	}
	for _, bad := range []string{"http://gitlab.example.org/o/priv", "https://u:p@gitlab.example.org/o/priv", "https://gitlab.example.org/", "https://gitlab.example.org/o/../x", "nonsense", ""} {
		if _, code := pre("alice", bad); code != 400 {
			t.Errorf("%q: %d", bad, code)
		}
	}
	if strings.Contains(buf.String(), "o/priv") || strings.Contains(buf.String(), gitAccess) {
		t.Errorf("the log shows the URL or the token:\n%s", buf.String())
	}
}

func TestGitPreflightHostDown(t *testing.T) {
	e, _ := gitEnv(t)
	host, _, _ := fakeRepoHost(t)
	e.srv.gitProbeBase = func(string) string { return host.URL }
	host.Close()
	rec := e.do("POST", "/git/preflight", "alice", map[string]string{"gitUri": "https://gitlab.example.org/o/priv"})
	if got := decode[GitPreflightResponse](t, rec); got.Result != GitPreflightResponseResultUnknown {
		t.Errorf("%+v", got)
	}
}

func TestGitPreflightIsRateLimited(t *testing.T) {
	e, _ := gitEnv(t)
	e.srv.cfg.RateLimit.LaunchesPerMinute = 3
	e.srv.gitProbeBase = func(string) string { return "http://127.0.0.1:1" }
	var last *httptest.ResponseRecorder
	for range e.srv.cfg.RateLimit.LaunchesPerMinute + 1 {
		last = e.do("POST", "/git/preflight", "alice", map[string]string{"gitUri": "https://other.example.org/a"})
	}
	if last.Code != 429 {
		t.Errorf("%d", last.Code)
	}
}

func TestGitIdentity(t *testing.T) {
	tpl := javaTemplate()
	fromToken := func(name string) *v1alpha1.IDETemplate {
		return &v1alpha1.IDETemplate{Spec: v1alpha1.IDETemplateSpec{InjectableKeys: []v1alpha1.InjectableKey{
			{Name: "GIT_USER", Source: v1alpha1.SourceUserName}, {Name: "GIT_MAIL", Source: v1alpha1.SourceUserEmail}}}}
	}
	noKeys := &v1alpha1.IDETemplate{Spec: v1alpha1.IDETemplateSpec{InjectableKeys: []v1alpha1.InjectableKey{{Name: "GIT_URI", Format: v1alpha1.FormatHTTPSURL}}}}
	base := map[string]string{"GIT_URI": "https://gitlab.example.org/o/r", "GIT_USER": "Idp Name", "GIT_MAIL": "idp@example.org"}
	for _, tc := range []struct {
		name       string
		tpl        *v1alpha1.IDETemplate
		id         gitaccount.Identity
		user, mail string
	}{
		{"account with name", tpl, gitaccount.Identity{Login: "gitty", DisplayName: "Gitty G", NoReplyEmail: "7-gitty@users.noreply.gitlab.example.org"}, "Gitty G", "7-gitty@users.noreply.gitlab.example.org"},
		{"login when no name", fromToken("x"), gitaccount.Identity{Login: "gitty", NoReplyEmail: "n@x"}, "gitty", "n@x"},
		{"no no-reply address keeps the mail", tpl, gitaccount.Identity{Login: "gitty", DisplayName: "Gitty G"}, "Gitty G", "idp@example.org"},
		{"template without the keys", noKeys, gitaccount.Identity{Login: "gitty", NoReplyEmail: "n@x"}, "Idp Name", "idp@example.org"},
	} {
		in := map[string]string{}
		for k, v := range base {
			in[k] = v
		}
		got := gitIdentity(tc.tpl, in, tc.id)
		if got["GIT_USER"] != tc.user || got["GIT_MAIL"] != tc.mail || got["GIT_URI"] != base["GIT_URI"] {
			t.Errorf("%s: %v", tc.name, got)
		}
		if in["GIT_USER"] != "Idp Name" {
			t.Errorf("%s: the input map was changed", tc.name)
		}
	}
}

// A launch on a provider repository gets the account identity; any other launch is unchanged.
func TestLaunchUsesTheGitIdentity(t *testing.T) {
	e, _ := gitEnv(t)
	connectAs(t, e, "alice")
	for _, tc := range []struct {
		name, token, uri, user, mail string
	}{
		{"connected on a provider repository", "alice", "https://gitlab.example.org/o/r", "Gitty G", "7-gitty@users.noreply.gitlab.example.org"},
		{"no account", "bob", "https://gitlab.example.org/o/r", "Bob B", "bob@example.org"},
		{"not a provider host", "alice", "https://git.example.org/o/r", "Alice A", "alice@example.org"},
	} {
		rec := e.do("POST", "/sessions", tc.token, map[string]any{"template": "java", "data": map[string]string{"GIT_URI": tc.uri}})
		if rec.Code != 201 {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
		sess := decode[Session](t, rec)
		var sec corev1.Secret
		if err := e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: sess.Name + "-data"}, &sec); err != nil {
			t.Fatal(err)
		}
		if string(sec.Data["GIT_USER"]) != tc.user || string(sec.Data["GIT_MAIL"]) != tc.mail {
			t.Errorf("%s: user %q mail %q", tc.name, sec.Data["GIT_USER"], sec.Data["GIT_MAIL"])
		}
	}
}

func TestPublicStatusListsGitProviders(t *testing.T) {
	e, _ := gitEnv(t)
	st := decode[PublicStatus](t, e.do("GET", "/status", "", nil))
	if st.GitProviders == nil || len(*st.GitProviders) != 1 {
		t.Fatalf("%+v", st.GitProviders)
	}
	if g := (*st.GitProviders)[0]; g.Name != "gl" || g.Type != "gitlab" || g.Host != "gitlab.example.org" || g.DisplayName != "Example GitLab" {
		t.Errorf("%+v", g)
	}
	// Nothing else about the accounts is public: no client id, no secret file, no approval settings.
	body := e.do("GET", "/status", "", nil).Body.String()
	for _, bad := range []string{"cid", "clientID", "approval", "inactivity"} {
		if strings.Contains(body, bad) {
			t.Errorf("status contains %q: %s", bad, body)
		}
	}
}

func approvalSession(t *testing.T, e *env, name, owner, gitURI string) {
	t.Helper()
	s := session(name, owner, "java", "Ready")
	s.Spec.DataSecretRef = name + "-data"
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-data", Namespace: testNS}, Data: map[string][]byte{"GIT_URI": []byte(gitURI)}}
	if err := e.k.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := e.k.Create(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
}

func TestGitRepositoryApproval(t *testing.T) {
	e, _ := gitEnv(t)
	approvalSession(t, e, "s-alice", "alice", "https://tok:en@gitlab.example.org/o/session.git")
	approvalSession(t, e, "s-bob", "bob", "https://gitlab.example.org/o/bob")
	connectAs(t, e, "alice")
	path := "/sessions/s-alice/git/repositories"
	put := func(token, repo, access string) *httptest.ResponseRecorder {
		return e.do("PUT", path, token, map[string]string{"repository": repo, "access": access})
	}
	r2 := "https://gitlab.example.org/o/second"

	list := decode[GitRepositories](t, e.do("GET", path, "alice", nil))
	if list.Mode != "write" || list.MaxPerSession != 3 || len(list.Approved) != 0 || list.SessionRepository == nil || *list.SessionRepository != "https://gitlab.example.org/o/session" {
		t.Fatalf("%+v", list)
	}
	if strings.Contains(*list.SessionRepository, "tok") {
		t.Fatal("credentials of the launch data in the answer")
	}

	if rec := put("alice", r2, "read"); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	first := decode[GitApproval](t, put("alice", r2+".git", "read"))
	if first.Repository != r2 || first.Access != GitAccessRead {
		t.Fatalf("not idempotent or not normalized: %+v", first)
	}
	if again := decode[GitApproval](t, put("alice", r2+"/", "read")); !again.ApprovedAt.Equal(first.ApprovedAt) {
		t.Error("an idempotent approval changed its time")
	}
	if up := decode[GitApproval](t, put("alice", r2, "write")); up.Access != GitAccessWrite {
		t.Errorf("raise to write: %+v", up)
	}
	list = decode[GitRepositories](t, e.do("GET", path, "alice", nil))
	if len(list.Approved) != 1 || list.Approved[0].Repository != r2 || list.Approved[0].Access != GitAccessWrite {
		t.Fatalf("%+v", list.Approved)
	}
	// The session repository has full access already and takes no place.
	if got := decode[GitApproval](t, put("alice", "https://gitlab.example.org/o/session", "read")); got.Access != GitAccessWrite {
		t.Errorf("%+v", got)
	}
	put("alice", "https://gitlab.example.org/o/third", "read")
	put("alice", "https://gitlab.example.org/o/fourth", "read")
	if rec := put("alice", "https://gitlab.example.org/o/fifth", "read"); rec.Code != 409 || codeOf(t, rec) != "git-approval-limit" {
		t.Errorf("limit: %d %s", rec.Code, rec.Body)
	}
	if rec := put("alice", r2, "read"); rec.Code != 200 { // a change of an existing one still works at the limit
		t.Errorf("%d", rec.Code)
	}

	del := func(token, repo string) int {
		return e.do("DELETE", path, token, map[string]string{"repository": repo}).Code
	}
	for range 2 { // removing twice is fine
		if del("alice", r2) != 204 {
			t.Error("remove is not idempotent")
		}
	}
	if l := decode[GitRepositories](t, e.do("GET", path, "alice", nil)); len(l.Approved) != 2 {
		t.Errorf("%+v", l.Approved)
	}

	// Other users, missing and deleting sessions are not found.
	for _, c := range []struct{ token, path string }{{"bob", path}, {"alice", "/sessions/s-bob/git/repositories"}, {"alice", "/sessions/nope/git/repositories"}} {
		if rec := e.do("GET", c.path, c.token, nil); rec.Code != 404 {
			t.Errorf("%s %s: %d", c.token, c.path, rec.Code)
		}
		if rec := e.do("PUT", c.path, c.token, map[string]string{"repository": r2, "access": "read"}); rec.Code != 404 {
			t.Errorf("PUT %s %s: %d", c.token, c.path, rec.Code)
		}
		if rec := e.do("DELETE", c.path, c.token, map[string]string{"repository": r2}); rec.Code != 404 {
			t.Errorf("DELETE %s %s: %d", c.token, c.path, rec.Code)
		}
	}
	var s v1alpha1.IDESession
	_ = e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: "s-alice"}, &s)
	s.Finalizers = []string{"test/hold"}
	_ = e.k.Update(context.Background(), &s)
	_ = e.k.Delete(context.Background(), &s)
	if rec := e.do("GET", path, "alice", nil); rec.Code != 404 {
		t.Errorf("deleting session: %d", rec.Code)
	}
}

func TestGitRepositoryApprovalRefusals(t *testing.T) {
	e, _ := gitEnv(t)
	approvalSession(t, e, "s-bob", "bob", "https://gitlab.example.org/o/bob")
	path := "/sessions/s-bob/git/repositories"
	ok := "https://gitlab.example.org/o/other"
	put := func(repo, access string) *httptest.ResponseRecorder {
		return e.do("PUT", path, "bob", map[string]string{"repository": repo, "access": access})
	}
	if rec := put(ok, "read"); rec.Code != 409 || codeOf(t, rec) != "git-not-connected" {
		t.Errorf("no account: %d %s", rec.Code, rec.Body)
	}
	connectAs(t, e, "bob")
	for _, tc := range []struct {
		name, repo, access string
		code               int
		want               ProblemCode
	}{
		{"not a provider", "https://other.example.org/o/r", "read", 400, "git-not-a-provider"},
		{"http", "http://gitlab.example.org/o/r", "read", 400, "invalid-input"},
		{"credentials", "https://u:p@gitlab.example.org/o/r", "read", 400, "invalid-input"},
		{"query", ok + "?a=b", "read", 400, "invalid-input"},
		{"fragment", ok + "#x", "read", 400, "invalid-input"},
		{"port", "https://gitlab.example.org:8443/o/r", "read", 400, "invalid-input"},
		{"no path", "https://gitlab.example.org/", "read", 400, "invalid-input"},
		{"bad access", ok, "admin", 400, "invalid-input"},
	} {
		if rec := put(tc.repo, tc.access); rec.Code != tc.code || codeOf(t, rec) != tc.want {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	e.srv.cfg.GitAccounts.Approval.Mode = config.ApprovalRead
	if rec := put(ok, "write"); rec.Code != 403 || codeOf(t, rec) != "git-access-not-allowed" {
		t.Errorf("read mode, write: %d %s", rec.Code, rec.Body)
	}
	if rec := put(ok, "read"); rec.Code != 200 {
		t.Errorf("read mode, read: %d", rec.Code)
	}
	e.srv.cfg.GitAccounts.Approval.Mode = config.ApprovalOff
	if rec := put(ok, "read"); rec.Code != 403 || codeOf(t, rec) != "git-approval-disabled" {
		t.Errorf("mode off: %d %s", rec.Code, rec.Body)
	}
	if l := decode[GitRepositories](t, e.do("GET", path, "bob", nil)); l.Mode != "off" || len(l.Approved) != 0 {
		t.Errorf("mode off lists approvals: %+v", l)
	}
}

func TestGitApprovalAuditLineAndNoSecretInLogs(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	e, _ := gitEnv(t)
	approvalSession(t, e, "s-alice", "alice", "https://tok:secret-vcs@gitlab.example.org/o/session")
	connectAs(t, e, "alice")
	e.do("PUT", "/sessions/s-alice/git/repositories", "alice", map[string]string{"repository": "https://gitlab.example.org/o/second", "access": "read"})
	e.do("DELETE", "/sessions/s-alice/git/repositories", "alice", map[string]string{"repository": "https://gitlab.example.org/o/second"})
	out := buf.String()
	for _, want := range []string{"git repository approved", "git repository approval removed", "s-alice", "o/second"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q", want)
		}
	}
	if strings.Contains(out, "secret-vcs") || strings.Contains(out, `"user":"alice"`) {
		t.Errorf("log shows a secret or the user name:\n%s", out)
	}
}

func TestValidReturnTo(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"/", true},
		{"/status", true},
		{"/?appDef=java&gitUri=https%3A%2F%2Fgithub.com%2Fa%2Fb", true},
		{"/a/b?x=1#f", true},
		{"", false},
		{"status", false},
		{"//evil.example", false},
		{"//", false},
		{"/\\evil.example", false},
		{"/a\\b", false},
		{"https://evil.example/", false},
		{"javascript:alert(1)", false},
		{"/a\nb", false},
		{"/a\rb", false},
		{"/a\x00b", false},
		{"/a\x7fb", false},
		{"/" + strings.Repeat("x", 512), false},
		{"http://evil.example", false},
		{"/%2F%2Fevil.example", true}, // percent-encoded slashes are no host separator for the browser
	} {
		if got := validReturnTo(tc.in); got != tc.want {
			t.Errorf("validReturnTo(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
