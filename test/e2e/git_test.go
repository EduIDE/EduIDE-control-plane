//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// The Git accounts run (ADR 0023). run.sh installs the chart with values-git.yaml first and runs these tests in
// a phase of their own; they need the fake provider test/fake-git (Service fake-git) and do nothing without it.
//
// The tests play the landing page: they connect through the API, follow the authorize step against the fake and
// post code and state to /git/callback. Tokens, codes and state never reach the test output: only lengths do.

var gitHost = "fake-git." + namespace + ".svc"

const (
	gitSessionRepo = "course/ex1"
	gitOtherRepo   = "course/other"
	gitPublicRepo  = "open/public"
	// gitAccessTTL is FAKE_GIT_ACCESS_TTL in manifests/fake-git.yaml.
	gitAccessTTL = 75 * time.Second
)

func gitURL(repo string) string { return "https://" + gitHost + "/" + repo + ".git" }

// ---- kubectl (the harness has the kind kubeconfig in KUBECONFIG; setup() refuses anything else) ----------

var kubectlChecked sync.Once

// kubectl runs kubectl against the kind cluster in the e2e namespace. It checks once that the kubeconfig is the
// kind one: context kind-* and a server on 127.0.0.1.
func kubectl(t testing.TB, stdin string, args ...string) (string, error) {
	t.Helper()
	kc := os.Getenv("KUBECONFIG")
	run := func(a ...string) (string, error) {
		c := exec.Command("kubectl", append([]string{"--kubeconfig", kc}, a...)...)
		c.Env = append(os.Environ(), "KUBECONFIG="+kc)
		if stdin != "" {
			c.Stdin = strings.NewReader(stdin)
		}
		var out bytes.Buffer
		c.Stdout, c.Stderr = &out, &out
		err := c.Run()
		return out.String(), err
	}
	kubectlChecked.Do(func() {
		ctxName, _ := run("config", "current-context")
		server, _ := run("config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
		if !strings.HasPrefix(strings.TrimSpace(ctxName), "kind-") || !strings.HasPrefix(strings.TrimSpace(server), "https://127.0.0.1:") {
			t.Fatalf("refusing to run kubectl: context %q server %q is not the local kind cluster", ctxName, server)
		}
	})
	return run(append([]string{"-n", namespace}, args...)...)
}

// ---- the fake provider -------------------------------------------------------------------------------

type fakeState struct {
	Counters map[string]int `json:"counters"`
	Repos    map[string]struct {
		Requests   int            `json:"requests"`
		Users      map[string]int `json:"users"`
		ValidToken int            `json:"validToken"`
		Rejected   int            `json:"rejected"`
		Pushes     int            `json:"pushes"`
		LastPush   *struct {
			Ref string `json:"ref"`
			New string `json:"new"`
		} `json:"lastPush"`
	} `json:"repos"`
	Codes         []string `json:"codes"`
	AccessTokens  []string `json:"accessTokens"`
	RefreshTokens []string `json:"refreshTokens"`
}

// secrets are all values the test must never find outside the provider and the service: codes, tokens and state.
func (s fakeState) secrets(states []string) []string {
	var out []string
	for _, l := range [][]string{s.Codes, s.AccessTokens, s.RefreshTokens, states} {
		out = append(out, l...)
	}
	return out
}

type fakeGit struct {
	tlsPort, statePort string
	tlsClient          *http.Client
}

var forwardRE = regexp.MustCompile(`127\.0\.0\.1:(\d+) -> (\d+)`)

// startFakeGit forwards the TLS port and the introspection port of the fake to random local ports.
func startFakeGit(t *testing.T) *fakeGit {
	t.Helper()
	caFile := os.Getenv("E2E_GIT_CA_FILE")
	if caFile == "" {
		t.Skip("E2E_GIT_CA_FILE is not set: the Git accounts run is part of test/e2e/run.sh")
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	if _, err := kubectl(t, "", "version", "--client"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("kubectl", "--kubeconfig", os.Getenv("KUBECONFIG"), "-n", namespace, "port-forward", "svc/fake-git", ":443", ":9000")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+os.Getenv("KUBECONFIG"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	local := make(chan [2]string, 4)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if m := forwardRE.FindStringSubmatch(sc.Text()); m != nil {
				local <- [2]string{m[2], m[1]}
			}
		}
	}()
	f := &fakeGit{tlsClient: &http.Client{Timeout: 20 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: gitHost, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	deadline := time.After(30 * time.Second)
	for f.tlsPort == "" || f.statePort == "" {
		select {
		case p := <-local:
			if p[0] == "8443" {
				f.tlsPort = p[1]
			} else if p[0] == "9000" {
				f.statePort = p[1]
			}
		case <-deadline:
			t.Fatal("kubectl port-forward to fake-git did not start")
		}
	}
	return f
}

func (f *fakeGit) state(t testing.TB) fakeState {
	t.Helper()
	resp, err := http.Get("http://127.0.0.1:" + f.statePort + "/_test/state")
	if err != nil {
		t.Fatalf("fake-git state: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var s fakeState
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatalf("fake-git state: %v", err)
	}
	return s
}

// authorize does what the browser does at the provider: it opens the authorize URL, which the fake answers with
// a redirect to the landing page, and reads code and state from the Location header. It does not follow it.
func (f *fakeGit) authorize(t testing.TB, authorizeURL string) (code, state string) {
	t.Helper()
	u, err := url.Parse(authorizeURL)
	if err != nil || u.Scheme != "https" || u.Host != gitHost || u.Path != "/oauth/authorize" {
		t.Fatalf("authorizeUrl has host %q path %q", u.Host, u.Path)
	}
	q := u.Query()
	if q.Get("client_id") != "e2e-client" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("redirect_uri") != "https://"+serviceHost+"/git/callback" {
		t.Fatalf("authorizeUrl parameters: client_id %q redirect_uri %q", q.Get("client_id"), q.Get("redirect_uri"))
	}
	resp, err := f.tlsClient.Get("https://127.0.0.1:" + f.tlsPort + u.RequestURI())
	if err != nil {
		t.Fatalf("authorize at the fake: %v", err)
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil || loc.Host != serviceHost || loc.Path != "/git/callback" {
		t.Fatalf("authorize answered %d with a redirect to host %q path %q", resp.StatusCode, loc.Host, loc.Path)
	}
	return loc.Query().Get("code"), loc.Query().Get("state")
}

// ---- API helpers -------------------------------------------------------------------------------------

type gitProviderInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Host        string `json:"host"`
	DisplayName string `json:"displayName"`
	Connected   bool   `json:"connected"`
	Login       string `json:"login"`
}

func (a *api) gitProviders() []gitProviderInfo {
	a.t.Helper()
	status, b := a.do(http.MethodGet, "/git/providers", nil)
	var out []gitProviderInfo
	if status != 200 || json.Unmarshal(b, &out) != nil {
		a.t.Fatalf("GET /git/providers: status %d", status)
	}
	return out
}

func (a *api) fake() gitProviderInfo {
	a.t.Helper()
	for _, p := range a.gitProviders() {
		if p.Name == "fake" {
			return p
		}
	}
	a.t.Fatal("provider fake is not listed")
	return gitProviderInfo{}
}

// connect starts the flow and returns the authorize URL.
func (a *api) connect() string {
	a.t.Helper()
	status, b := a.do(http.MethodPost, "/git/providers/fake/connect", map[string]string{"returnTo": "/"})
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if status != 200 || json.Unmarshal(b, &out) != nil || out.AuthorizeURL == "" {
		a.t.Fatalf("connect: status %d", status)
	}
	return out.AuthorizeURL
}

// callback posts what the landing page posts and returns the status and the problem code, if any.
func (a *api) callback(body map[string]string) (int, string, map[string]any) {
	a.t.Helper()
	status, b := a.do(http.MethodPost, "/git/callback", body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	code, _ := m["code"].(string)
	return status, code, m
}

func (a *api) preflight(repo string) (result, provider string) {
	a.t.Helper()
	status, b := a.do(http.MethodPost, "/git/preflight", map[string]string{"gitUri": repo})
	var out struct {
		Result   string `json:"result"`
		Provider *struct {
			Name string `json:"name"`
		} `json:"provider"`
	}
	if status != 200 || json.Unmarshal(b, &out) != nil {
		a.t.Fatalf("preflight %s: status %d", repo, status)
	}
	if out.Provider != nil {
		provider = out.Provider.Name
	}
	return out.Result, provider
}

// connectAccount runs the whole flow for the user of a and returns the state value (for the leak checks).
func connectAccount(t *testing.T, a *api, f *fakeGit) (state string) {
	t.Helper()
	code, state := f.authorize(t, a.connect())
	status, problem, body := a.callback(map[string]string{"code": code, "state": state})
	if status != 200 || body["login"] != "e2euser" || body["provider"] != "fake" {
		t.Fatalf("callback: status %d code %q", status, problem)
	}
	return state
}

// ---- git in a session (through the test endpoint of the fake IDE) ------------------------------------

type gitStep struct {
	Name   string `json:"name"`
	Exit   int    `json:"exit"`
	Stderr string `json:"stderr"`
}

type gitRun []gitStep

// failure is the first failed step, or nil.
func (r gitRun) failure() *gitStep {
	for i := range r {
		if r[i].Exit != 0 {
			return &r[i]
		}
	}
	return nil
}

func (r gitRun) String() string {
	var sb strings.Builder
	for _, s := range r {
		fmt.Fprintf(&sb, "%s=%d ", s.Name, s.Exit)
	}
	return sb.String()
}

// gitOps asks the fake IDE of the session to clone, commit and push a repository; target is session, second or public.
func gitOps(t testing.TB, hc *http.Client, s Session, target string) gitRun {
	t.Helper()
	var steps gitRun
	// The fake IDE knows the provider hosts a moment after the claim (409 before).
	eventually(t, 60*time.Second, "git test endpoint of the fake IDE", func() bool {
		resp, err := hc.Post(s.URL+"git-test?target="+target, "", nil)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct {
			Steps gitRun `json:"steps"`
		}
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
			return false
		}
		steps = out.Steps
		return true
	})
	return steps
}

// launchGitSession launches the plain template with a Git repository and waits until the fake IDE has the launch data.
func launchGitSession(t *testing.T, u user, repo string) (*api, Session, *http.Client, dataState) {
	t.Helper()
	a := newAPI(t, u)
	s := a.mustLaunch(LaunchRequest{Template: "plain", Data: map[string]string{"GIT_URI": gitURL(repo)}})
	s = a.waitPhase(s.Name, 3*time.Minute, "Ready", "Failed")
	if s.Phase != "Ready" {
		t.Fatalf("phase %s: %s %s", s.Phase, s.ErrorCode, s.Message)
	}
	hc := browserLogin(t, u, "https://"+instanceHost+"/oauth2/login")
	var ds dataState
	eventually(t, 60*time.Second, "fake IDE has the launch data", func() bool {
		var status int
		var err error
		ds, status, err = fetchDataState(hc, s.URL)
		return err == nil && status == 200 && ds.Claimed
	})
	return a, s, hc, ds
}

// ---- the tests ---------------------------------------------------------------------------------------

func TestGitAccounts(t *testing.T) {
	f := startFakeGit(t)
	var states []string // every state value the test saw, for the leak checks

	t.Run("feature is on", func(t *testing.T) {
		eventually(t, 2*time.Minute, "git-proxy ready", func() bool {
			var l appsv1.DeploymentList
			if err := kube.List(ctx(), &l, client.InNamespace(namespace)); err != nil {
				return false
			}
			for _, d := range l.Items {
				if strings.HasSuffix(d.Name, "-git-proxy") && d.Status.ReadyReplicas >= 1 && d.Status.UpdatedReplicas == d.Status.Replicas {
					return true
				}
			}
			return false
		})
		_, body, _ := get(t, testHTTPClient(t, false, true), "https://"+serviceHost+"/api/v1/status")
		var st struct {
			GitProviders []struct{ Name, Type, Host, DisplayName string } `json:"gitProviders"`
		}
		if err := json.Unmarshal([]byte(body), &st); err != nil || len(st.GitProviders) != 1 || st.GitProviders[0].Name != "fake" || st.GitProviders[0].Host != gitHost {
			t.Fatalf("GET /status gitProviders = %+v (%v)", st.GitProviders, err)
		}
		p := newAPI(t, alice).fake()
		if p.Type != "gitlab" || p.Host != gitHost || p.DisplayName != "Fake Git" || p.Connected || p.Login != "" {
			t.Fatalf("provider = %+v", p)
		}
	})

	t.Run("denied at the provider", func(t *testing.T) {
		a := newAPI(t, alice)
		_, state := f.authorize(t, a.connect())
		status, code, _ := a.callback(map[string]string{"error": "access_denied", "state": state})
		if status != 400 || code != "git-denied" {
			t.Fatalf("status %d code %q, want 400 git-denied", status, code)
		}
		if a.fake().Connected {
			t.Fatal("a denied flow connected the account")
		}
	})

	t.Run("preflight before connect", func(t *testing.T) {
		a := newAPI(t, alice)
		for _, c := range []struct{ repo, want, provider string }{
			{gitURL(gitSessionRepo), "connect", "fake"},
			{gitURL(gitPublicRepo), "ok", "fake"},
			{"https://git.example.org/some/repo.git", "not-a-provider", ""},
		} {
			if got, prov := a.preflight(c.repo); got != c.want || prov != c.provider {
				t.Errorf("preflight %s = %q provider %q, want %q %q", c.repo, got, prov, c.want, c.provider)
			}
		}
	})

	var connectedAt time.Time
	t.Run("another user cannot complete my flow", func(t *testing.T) {
		a, b := newAPI(t, alice), newAPI(t, bob)
		code, state := f.authorize(t, a.connect())
		states = append(states, state)
		before := f.state(t).Counters["exchanges"]
		status, problem, _ := b.callback(map[string]string{"code": code, "state": state})
		if status != 400 || problem != "git-invalid-state" {
			t.Fatalf("callback of another user: status %d code %q, want 400 git-invalid-state", status, problem)
		}
		if f.state(t).Counters["exchanges"] != before {
			t.Fatal("the service exchanged the code for another user")
		}
		if a.fake().Connected || b.fake().Connected {
			t.Fatal("the rejected callback stored an account")
		}
		// The code is still unused: the owner of the flow completes it.
		status, problem, body := a.callback(map[string]string{"code": code, "state": state})
		if status != 200 || body["login"] != "e2euser" || body["provider"] != "fake" || body["returnTo"] != "/" {
			t.Fatalf("callback of the owner: status %d code %q", status, problem)
		}
		connectedAt = time.Now()
		if p := a.fake(); !p.Connected || p.Login != "e2euser" {
			t.Fatalf("provider after connect = %+v", p)
		}
		if b.fake().Connected {
			t.Fatal("the second user shows as connected")
		}
		// A used state is no key: the same callback again must fail.
		if status, problem, _ := a.callback(map[string]string{"code": code, "state": state}); status < 400 {
			t.Fatalf("replayed callback: status %d code %q", status, problem)
		}
	})

	t.Run("preflight after connect", func(t *testing.T) {
		a := newAPI(t, alice)
		for _, c := range []struct{ repo, want string }{
			{gitURL(gitSessionRepo), "ok"},
			{gitURL(gitPublicRepo), "ok"},
			{gitURL("course/missing"), "no-access"},
			{"https://git.example.org/some/repo.git", "not-a-provider"},
		} {
			if got, _ := a.preflight(c.repo); got != c.want {
				t.Errorf("preflight %s = %q, want %q", c.repo, got, c.want)
			}
		}
	})

	t.Run("session with a connected account", func(t *testing.T) {
		a, s, hc, ds := launchGitSession(t, alice, gitSessionRepo)
		pod := podOfSession(t, s)

		t.Run("identity", func(t *testing.T) {
			// GIT_USER and GIT_MAIL are injected from the token; with a provider repository the service replaces them.
			sum := sha256.Sum256([]byte("4242-e2euser@users.noreply." + gitHost + gitURL(gitSessionRepo) + "E2E User"))
			if !slices.Equal(ds.Keys, []string{"GIT_MAIL", "GIT_URI", "GIT_USER"}) || ds.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("data-state keys %v hash %s: GIT_USER or GIT_MAIL is not the account identity", ds.Keys, ds.SHA256)
			}
		})

		t.Run("clone, commit and push the session repository", func(t *testing.T) {
			before := f.state(t).Repos[gitSessionRepo]
			run := gitOps(t, hc, s, "session")
			if fail := run.failure(); fail != nil || len(run) != 4 {
				t.Fatalf("steps %s failure %+v", run, fail)
			}
			after := f.state(t).Repos[gitSessionRepo]
			if after.Pushes != before.Pushes+1 || after.LastPush == nil || after.LastPush.Ref != "refs/heads/main" || len(after.LastPush.New) != 40 {
				t.Fatalf("fake saw pushes %d -> %d, last %+v", before.Pushes, after.Pushes, after.LastPush)
			}
			if after.ValidToken <= before.ValidToken || after.Users["oauth2"] <= before.Users["oauth2"] {
				t.Fatal("the fake did not see the access token of the account on the session repository")
			}
		})

		repoPath := "/sessions/" + s.Name + "/git/repositories"
		otherURL := "https://" + gitHost + "/" + gitOtherRepo
		link := func(access string) []string {
			return []string{"approveRepo=" + url.QueryEscape(otherURL), "session=" + s.Name, "access=" + access}
		}
		// pollGit repeats the Git steps until cond holds: the proxy caches decisions for a few seconds.
		pollGit := func(t *testing.T, target, what string, cond func(gitRun) bool) gitRun {
			var run gitRun
			eventually(t, 10*time.Second, what, func() bool { run = gitOps(t, hc, s, target); return cond(run) })
			return run
		}
		noSecrets := func(t *testing.T, text string) {
			for i, v := range f.state(t).secrets(states) {
				if strings.Contains(text, v) {
					t.Errorf("value %d is in the message shown to the user", i)
				}
			}
		}

		t.Run("second private repository is refused with an approval link", func(t *testing.T) {
			before := f.state(t).Repos[gitOtherRepo]
			run := gitOps(t, hc, s, "second")
			fail := run.failure()
			if fail == nil || fail.Name != "clone" {
				t.Fatalf("steps %s failure %+v", run, fail)
			}
			for _, want := range append(link("read"), "This session may not use "+gitHost+"/"+gitOtherRepo) {
				if !strings.Contains(fail.Stderr, want) {
					t.Errorf("stderr lacks %q: %s", want, fail.Stderr)
				}
			}
			noSecrets(t, fail.Stderr)
			after := f.state(t).Repos[gitOtherRepo]
			if after.Users["oauth2"] != before.Users["oauth2"] || after.ValidToken != before.ValidToken {
				t.Fatal("the credential went to a repository that is not approved")
			}
		})

		t.Run("approve read, then write", func(t *testing.T) {
			put := func(access string) {
				status, b := a.do(http.MethodPut, repoPath, map[string]string{"repository": otherURL, "access": access})
				var got struct{ Repository, Access, ApprovedAt string }
				if status != 200 || json.Unmarshal(b, &got) != nil || got.Repository != otherURL || got.Access != access || got.ApprovedAt == "" {
					t.Fatalf("PUT %s: status %d body %s", access, status, b)
				}
			}
			put("read")
			put("read") // idempotent
			before := f.state(t).Repos[gitOtherRepo]
			run := pollGit(t, "second", "clone works after the read approval", func(r gitRun) bool {
				f := r.failure()
				return f == nil || f.Name != "clone"
			})
			if fail := run.failure(); fail == nil || fail.Name != "push" || !strings.Contains(fail.Stderr, "This session may not push to") || !strings.Contains(fail.Stderr, "access=write") {
				t.Fatalf("push with a read approval: steps %s failure %+v", run, fail)
			}
			if st := f.state(t).Repos[gitOtherRepo]; st.ValidToken <= before.ValidToken || st.Pushes != before.Pushes {
				t.Fatalf("fake saw valid token requests %d -> %d, pushes %d -> %d", before.ValidToken, st.ValidToken, before.Pushes, st.Pushes)
			}

			put("write")
			pollGit(t, "second", "push works after the write approval", func(r gitRun) bool { return r.failure() == nil })
			st := f.state(t).Repos[gitOtherRepo]
			if st.Pushes != before.Pushes+1 || st.LastPush == nil || st.LastPush.Ref != "refs/heads/main" || st.Users["oauth2"] == 0 {
				t.Fatalf("fake saw pushes %d -> %d, last %+v, users %v", before.Pushes, st.Pushes, st.LastPush, st.Users)
			}
		})

		t.Run("repositories of the session", func(t *testing.T) {
			status, b := a.do(http.MethodGet, repoPath, nil)
			var got struct {
				Mode              string
				MaxPerSession     int
				SessionRepository string
				Approved          []struct{ Repository, Access, ApprovedAt string }
			}
			if status != 200 || json.Unmarshal(b, &got) != nil {
				t.Fatalf("GET: status %d", status)
			}
			if got.Mode != "write" || got.MaxPerSession != 20 || got.SessionRepository != "https://"+gitHost+"/"+gitSessionRepo ||
				len(got.Approved) != 1 || got.Approved[0].Repository != otherURL || got.Approved[0].Access != "write" {
				t.Fatalf("GET = %+v", got)
			}
		})

		t.Run("another user and bad requests", func(t *testing.T) {
			b := newAPI(t, bob)
			for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
				if status, _ := b.do(m, repoPath, map[string]string{"repository": otherURL, "access": "read"}); status != 404 {
					t.Errorf("%s of another user's session: status %d, want 404", m, status)
				}
			}
			for _, c := range []struct{ repo, code string }{
				{"http://" + gitHost + "/a/b", "invalid-input"},
				{"https://user:pw@" + gitHost + "/a/b", "invalid-input"},
				{"https://" + gitHost + "/a/b?x=1", "invalid-input"},
				{"https://" + gitHost + "/a/b/.git/info", "invalid-input"},
				{"https://" + gitHost + "/a/b.git.git", "invalid-input"},
				{"https://git.example.org/a/b", "git-not-a-provider"},
			} {
				status, body := a.do(http.MethodPut, repoPath, map[string]string{"repository": c.repo, "access": "read"})
				var p Problem
				_ = json.Unmarshal(body, &p)
				if status != 400 || p.Code != c.code {
					t.Errorf("PUT %q: status %d code %q, want 400 %s", c.repo, status, p.Code, c.code)
				}
			}
		})

		t.Run("pod cannot approve", func(t *testing.T) {
			// A real session name, the provider host of the approved repository and the public route of the service:
			// the only thing between the pod and an approval is that it has no bearer token.
			req, _ := json.Marshal(map[string]string{"session": s.Name, "repository": otherURL})
			resp, err := hc.Post(s.URL+"try-approve", "application/json", bytes.NewReader(req))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var got map[string]struct {
				Status int  `json:"status"`
				Error  bool `json:"error"`
			}
			if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&got) != nil {
				t.Fatalf("try-approve: status %d", resp.StatusCode)
			}
			// Session pods may not reach the gateway at all (the egress deny list covers private ranges), so the pod
			// either gets no connection or a 401. Never a success.
			if svc := got["service"]; !svc.Error && svc.Status != 401 && svc.Status != 403 {
				t.Errorf("service answered %+v to a call without a token", svc)
			}
			// What the pod would hit if it could connect: the same PUT without a token, and with a made-up token,
			// from here, with the real session name and a provider repository.
			for name, bearer := range map[string]string{"no token": "", "made-up token": "pod-guess"} {
				r, _ := http.NewRequest(http.MethodPut, apiBase+repoPath, strings.NewReader(`{"repository":"`+otherURL+`","access":"read"}`))
				r.Header.Set("Content-Type", "application/json")
				if bearer != "" {
					r.Header.Set("Authorization", "Bearer "+bearer)
				}
				pr, err := testHTTPClient(t, false, true).Do(r)
				if err != nil {
					t.Fatal(err)
				}
				_ = pr.Body.Close()
				if pr.StatusCode != http.StatusUnauthorized {
					t.Errorf("PUT with %s: status %d, want 401", name, pr.StatusCode)
				}
			}
			if ag := got["agent"]; ag.Error || ag.Status < 400 {
				t.Errorf("agent answered %+v to a PUT on the Git path", ag)
			}
			// Neither that nor the Git requests through the agent (which carries the instance token) approved anything.
			_, b := a.do(http.MethodGet, repoPath, nil)
			var list struct{ Approved []any }
			_ = json.Unmarshal(b, &list)
			if len(list.Approved) != 1 {
				t.Errorf("approved list changed: %s", b)
			}
		})

		t.Run("delete the approval", func(t *testing.T) {
			if status, _ := a.do(http.MethodDelete, repoPath, map[string]string{"repository": otherURL}); status != 204 {
				t.Fatalf("DELETE status %d", status)
			}
			_, b := a.do(http.MethodGet, repoPath, nil)
			var got struct{ Approved []any }
			_ = json.Unmarshal(b, &got)
			if len(got.Approved) != 0 {
				t.Fatalf("approved after DELETE: %s", b)
			}
			run := pollGit(t, "second", "clone fails after the approval is removed", func(r gitRun) bool {
				f := r.failure()
				return f != nil && f.Name == "clone"
			})
			if !strings.Contains(run.failure().Stderr, "approveRepo=") {
				t.Fatalf("stderr: %s", run.failure().Stderr)
			}
		})

		t.Run("push elsewhere is denied", func(t *testing.T) {
			before := f.state(t).Repos[gitPublicRepo].Pushes
			run := gitOps(t, hc, s, "public")
			fail := run.failure()
			// Reading the public repository works anonymously, the push does not.
			if fail == nil || fail.Name != "push" || !strings.Contains(fail.Stderr, "This session may not push to") {
				t.Fatalf("steps %s failure %+v", run, fail)
			}
			if f.state(t).Repos[gitPublicRepo].Pushes != before {
				t.Fatal("a push reached the public repository")
			}
		})

		t.Run("session pod cannot reach the provider, git-proxy is open", func(t *testing.T) {
			fakePod := podIP(t, map[string]string{"app": "fake-git"})
			proxy := podIP(t, map[string]string{"app.kubernetes.io/component": "git-proxy"})
			out, err := kubectl(t, "", "exec", pod.Name, "-c", "ide", "--", "sh", "-c",
				"nc -z -w 3 "+proxy+" 8080 && echo OPEN proxy || echo CLOSED proxy\n"+
					"nc -z -w 3 "+fakePod+" 8443 && echo OPEN fake-pod || echo CLOSED fake-pod\n"+
					"getent hosts "+gitHost+" >/dev/null && echo RESOLVES\n"+
					"nc -z -w 3 "+gitHost+" 443 && echo OPEN fake-service || echo CLOSED fake-service")
			if err != nil {
				t.Fatalf("exec: %v: %s", err, out)
			}
			// OPEN proxy and RESOLVES are the positive controls: the pod has a network, a resolver and may use git-proxy.
			if !strings.Contains(out, "OPEN proxy") || !strings.Contains(out, "RESOLVES") || !strings.Contains(out, "CLOSED fake-pod") || !strings.Contains(out, "CLOSED fake-service") {
				t.Fatalf("egress of the session pod:\n%s", out)
			}
		})

		t.Run("refresh after the access token expired", func(t *testing.T) {
			time.Sleep(time.Until(connectedAt.Add(gitAccessTTL + 3*time.Second)))
			run := gitOps(t, hc, s, "session")
			if fail := run.failure(); fail != nil {
				t.Fatalf("steps %s failure %+v: the service did not refresh the token", run, fail)
			}
			st := f.state(t)
			if st.Counters["refreshes"] < 1 || st.Counters["invalidRefreshes"] != 0 || len(st.RefreshTokens) < 2 {
				t.Fatalf("fake counters %v: want a refresh with rotation and no rejected refresh token", st.Counters)
			}
		})

		t.Run("disconnect", func(t *testing.T) {
			revokes := f.state(t).Counters["revokes"]
			if status, _ := a.do(http.MethodDelete, "/git/providers/fake", nil); status != 204 {
				t.Fatalf("DELETE status %d", status)
			}
			if a.fake().Connected {
				t.Fatal("still connected")
			}
			if f.state(t).Counters["revokes"] <= revokes {
				t.Fatal("the fake did not see a revoke")
			}
			// git-proxy keeps an allowed answer with a credential for 5 seconds (allowTTL), other answers for 3.
			start := time.Now()
			var run gitRun
			eventually(t, 10*time.Second, "private fetch fails with the connect message", func() bool {
				run = gitOps(t, hc, s, "session")
				fail := run.failure()
				if fail == nil || fail.Name != "clone" || !strings.Contains(fail.Stderr, "Connect your Fake Git account") {
					t.Logf("steps %s, first failure %+v", run, fail)
					return false
				}
				return true
			})
			if d := time.Since(start); d > 8*time.Second {
				t.Errorf("the connect message took %s to show up, want 8 s at most", d)
			}
		})

		t.Run("no token, code or state outside the provider and the service", func(t *testing.T) {
			secrets := f.state(t).secrets(states)
			if len(secrets) < 5 {
				t.Fatalf("only %d values to search for", len(secrets))
			}
			// The IDE container: files and the environment of the IDE process. The search finds the Git
			// config, so a hit would be found too (positive control below).
			dirs := []string{"/home", "/work", "/etc", "/tmp", "/var", "/opt", "/srv", "/run", "/proc/1/environ"}
			grep := func(patterns string) string {
				out, _ := kubectl(t, patterns, append([]string{"exec", "-i", pod.Name, "-c", "ide", "--", "grep", "-rlasF", "-f", "-"}, dirs...)...)
				var hits []string // grep also prints errors for directories it may not read
				for _, l := range strings.Split(out, "\n") {
					if strings.HasPrefix(l, "/") {
						hits = append(hits, l)
					}
				}
				return strings.Join(hits, " ")
			}
			if got := grep("insteadOf\n"); !strings.Contains(got, "/work/.gitconfig") {
				t.Fatalf("positive control failed, grep found %q", got)
			}
			if got := grep(strings.Join(secrets, "\n") + "\n"); got != "" {
				t.Errorf("a token, code or state is in files of the IDE container: %s", got)
			}
			if out, err := kubectl(t, "", "exec", pod.Name, "-c", "ide", "--", "sh", "-c", "git config --global --list; env"); err != nil {
				t.Errorf("exec: %v", err)
			} else {
				for i, v := range secrets {
					if strings.Contains(out, v) {
						t.Errorf("value %d is in the git config or environment of the IDE", i)
					}
				}
			}

			// Logs of all pods (service, git-proxy, operator, agent of this session, fake IDE) and the objects.
			var pods corev1.PodList
			if err := kube.List(ctx(), &pods, client.InNamespace(namespace)); err != nil {
				t.Fatal(err)
			}
			for _, p := range pods.Items {
				raw, _ := json.Marshal(p)
				l, err := logsOf(ctx(), &p)
				if err != nil {
					t.Logf("logs of %s: %v", p.Name, err)
				}
				for i, v := range secrets {
					if strings.Contains(l, v) {
						t.Errorf("value %d is in the logs of pod %s", i, p.Name)
					}
					if strings.Contains(string(raw), v) {
						t.Errorf("value %d is in the spec of pod %s", i, p.Name)
					}
				}
			}
			var cr v1alpha1.IDESession
			if err := kube.Get(ctx(), client.ObjectKey{Namespace: namespace, Name: s.Name}, &cr); err != nil {
				t.Fatal(err)
			}
			var sec corev1.Secret
			if err := kube.Get(ctx(), client.ObjectKey{Namespace: namespace, Name: cr.Spec.DataSecretRef}, &sec); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(cr)
			for k, v := range sec.Data {
				if strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "secret") || strings.Contains(strings.ToLower(k), "password") {
					t.Errorf("launch data has the key %q", k)
				}
				for i, sv := range secrets {
					if strings.Contains(string(v), sv) || strings.Contains(string(raw), sv) {
						t.Errorf("value %d is in launch data key %s or the IDESession", i, k)
					}
				}
			}
			if keys := mapKeys(sec.Data); !slices.Equal(keys, []string{"GIT_MAIL", "GIT_URI", "GIT_USER"}) {
				t.Errorf("launch data keys %v", keys)
			}
		})
	})

	t.Run("session without an account", func(t *testing.T) {
		_, s, hc, _ := launchGitSession(t, bob, gitPublicRepo)
		if p := newAPI(t, bob).fake(); p.Connected {
			t.Fatal("bob is connected")
		}
		status, body := newAPI(t, bob).do(http.MethodPut, "/sessions/"+s.Name+"/git/repositories", map[string]string{"repository": "https://" + gitHost + "/" + gitOtherRepo, "access": "read"})
		var pb Problem
		_ = json.Unmarshal(body, &pb)
		if status != 409 || pb.Code != "git-not-connected" {
			t.Fatalf("PUT without an account: status %d code %q, want 409 git-not-connected", status, pb.Code)
		}
		// A public repository needs no account, and a session repository that is public clones anonymously.
		if run := gitOps(t, hc, s, "session"); run.failure() == nil || run.failure().Name != "push" {
			t.Fatalf("session repository (public): steps %s, want clone and commit to work, push to fail", run)
		}
		if run := gitOps(t, hc, s, "public"); run.failure() == nil || run.failure().Name != "push" {
			t.Fatalf("public: steps %s", run)
		}
		run := gitOps(t, hc, s, "second")
		if fail := run.failure(); fail == nil || fail.Name != "clone" || !strings.Contains(fail.Stderr, "Connect your Fake Git account") {
			t.Fatalf("private repository without an account: steps %s failure %+v", run, fail)
		}
	})

	t.Run("idle instance cannot use the proxy", func(t *testing.T) {
		waitPoolIdle(t, "warm", 2)
		old := map[string]bool{}
		for _, p := range idlePods(t, "warm") {
			old[p.Name] = true
			_ = kube.Delete(ctx(), &p)
		}
		// Idle pods from before the upgrade do not know the Git hosts: take one the pool built with the Git config.
		var idle corev1.Pod
		eventually(t, 3*time.Minute, "a new idle instance", func() bool {
			for _, p := range idlePods(t, "warm") {
				if old[p.Name] {
					continue
				}
				for _, c := range p.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						idle = p
						return true
					}
				}
			}
			return false
		})
		out, err := kubectl(t, "", "exec", idle.Name, "-c", "ide", "--", "wget", "-S", "-q", "-O", "/dev/null", "-T", "5",
			"http://127.0.0.1:8082/git/"+gitHost+"/"+gitPublicRepo+".git/info/refs?service=git-upload-pack")
		if err == nil || !strings.Contains(out, "403") {
			t.Fatalf("an idle instance answered the proxy path: err %v output %q", err, out)
		}
		// The agent answers the host list without a claim: the extension needs it early.
		if out, err := kubectl(t, "", "exec", idle.Name, "-c", "ide", "--", "wget", "-q", "-O", "-", "-T", "5", "http://127.0.0.1:8082/v1/git/hosts"); err != nil || !strings.Contains(out, gitHost) {
			t.Fatalf("hosts of an idle instance: %v %q", err, out)
		}
	})

	t.Run("foreign pods cannot reach git-proxy or the internal port of the service", func(t *testing.T) {
		proxy := podIP(t, map[string]string{"app.kubernetes.io/component": "git-proxy"})
		svc := podIP(t, map[string]string{"app.kubernetes.io/component": "service"})
		res := runProbe(t, "for t in "+proxy+":8080 "+svc+":8083; do nc -z -w 3 ${t%:*} ${t#*:} && echo OPEN $t || echo CLOSED $t; done\n"+
			"nc -z -w 3 gateway-entry.envoy-gateway-system.svc 443 && echo OPEN control || echo CLOSED control")
		if !strings.Contains(res, "OPEN control") {
			t.Fatalf("probe cannot reach the gateway, the test proves nothing:\n%s", res)
		}
		for _, p := range []string{proxy + ":8080", svc + ":8083"} {
			if !strings.Contains(res, "CLOSED "+p) {
				t.Errorf("%s is reachable from a foreign pod:\n%s", p, res)
			}
		}
	})
}

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// podIP is the address of the first running pod with the labels.
func podIP(t testing.TB, labels map[string]string) string {
	t.Helper()
	var l corev1.PodList
	if err := kube.List(ctx(), &l, client.InNamespace(namespace), client.MatchingLabels(labels)); err != nil {
		t.Fatal(err)
	}
	for _, p := range l.Items {
		if p.Status.Phase == corev1.PodRunning && p.Status.PodIP != "" && p.DeletionTimestamp == nil {
			return p.Status.PodIP
		}
	}
	t.Fatalf("no running pod with labels %v", labels)
	return ""
}
