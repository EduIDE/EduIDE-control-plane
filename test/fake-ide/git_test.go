package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func TestConfigureGitWritesInsteadOf(t *testing.T) {
	needGit(t)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/git/hosts" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"hosts":["git.test","bad host"]}`))
	}))
	defer agent.Close()
	i := &ide{agent: agent.URL}
	if err := i.configureGit(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := run(t, "", "config", "--global", "--get-all", "url."+agent.URL+"/git/git.test/.insteadOf")
	if got != "https://git.test/" {
		t.Fatalf("insteadOf = %q", got)
	}
	if out := run(t, "", "config", "--global", "--list"); strings.Contains(out, "bad host") {
		t.Fatalf("invalid host written: %s", out)
	}
}

func TestGitTestCloneCommitPush(t *testing.T) {
	needGit(t)
	srv := t.TempDir()
	bare := filepath.Join(srv, "course", "other.git")
	seed := t.TempDir()
	run(t, seed, "init", "-q", "-b", "main")
	run(t, seed, "-c", "user.name=s", "-c", "user.email=s@x", "commit", "-q", "--allow-empty", "-m", "seed")
	run(t, "", "init", "-q", "-b", "main", "--bare", bare)
	run(t, seed, "push", "-q", bare, "main")
	// "https://git.test/" stands in for the agent: same rewrite mechanism, a local path as the server.
	run(t, "", "config", "--global", "url.file://"+srv+"/.insteadOf", "https://git.test/")

	i := &ide{gitHosts: []string{"git.test"}}
	i.state.Data = map[string]string{"GIT_USER": "Ada", "GIT_MAIL": "ada@example.invalid", "GIT_URI": "https://other.test/x/y.git"}
	srvIDE := httptest.NewServer(i.handler())
	defer srvIDE.Close()

	post := func(target string) (int, []gitStep) {
		resp, err := http.Post(srvIDE.URL+"/git-test?target="+target, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct{ Steps []gitStep }
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.Steps
	}
	status, steps := post("second")
	if status != 200 || len(steps) != 4 {
		t.Fatalf("status %d steps %+v", status, steps)
	}
	for _, s := range steps {
		if s.Exit != 0 {
			t.Fatalf("step %+v", s)
		}
	}
	if got := run(t, bare, "log", "-1", "--format=%an <%ae>", "main"); got != "Ada <ada@example.invalid>" {
		t.Fatalf("pushed commit author %q", got)
	}
	if status, _ := post("elsewhere"); status != http.StatusBadRequest {
		t.Fatalf("unknown target: %d", status)
	}
	if status, _ := post("session"); status != http.StatusConflict {
		t.Fatalf("GIT_URI on a foreign host: %d", status)
	}
}

func TestScrub(t *testing.T) {
	for in, bad := range map[string]string{
		"fatal: unable to access 'https://oauth2:gat-0123456789abcdef0123@host/x.git'": "gat-0123",
		"remote: Authorization: Bearer abc":                                            "abc",
		"error: gat-0123456789abcdef0123456789abcdef denied":                           "gat-0123",
	} {
		if out := scrub(in); strings.Contains(out, bad) {
			t.Errorf("scrub(%q) = %q still has %q", in, out, bad)
		}
	}
	msg := "remote error: EduIDE uses your Fake Git account only for the repository this session was started with (fake-git.eduide-e2e.svc/course/ex1)."
	if scrub(msg) != msg {
		t.Errorf("scrub changed a policy message: %q", scrub(msg))
	}
}

func TestTryApproveSendsNoCredentialsAndReportsStatusOnly(t *testing.T) {
	var sawAuth, sawMethod string
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth, sawMethod = r.Header.Get("Authorization"), r.Method
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer svc.Close()
	agent := httptest.NewServer(http.NotFoundHandler())
	defer agent.Close()
	t.Setenv("FAKE_IDE_SERVICE_URL", svc.URL)
	i := &ide{agent: agent.URL, gitHosts: []string{"git.test"}}
	srv := httptest.NewServer(i.handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/try-approve", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got map[string]attempt
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["service"].Status != 401 || got["agent"].Status != 404 || sawAuth != "" || sawMethod != http.MethodPut {
		t.Fatalf("result %+v, service saw %q %q", got, sawMethod, sawAuth)
	}
	svc.Close()
	resp2, err := http.Post(srv.URL+"/try-approve", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	got = nil
	_ = json.NewDecoder(resp2.Body).Decode(&got)
	if !got["service"].Error {
		t.Fatalf("unreachable service: %+v", got)
	}
}
