package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The Git side of the fake IDE (ADR 0023). Like the extension it reads the provider hosts from the agent and
// routes Git requests for them through the agent, which forwards them to the git-proxy. It never holds a
// credential: the Git client in this container has none and is never asked for one.

const gitTimeout = 60 * time.Second

// gitHostsAnswer is GET /v1/git/hosts of the agent.
type gitHostsAnswer struct {
	Hosts []string `json:"hosts"`
}

// setupGit waits for the agent, then writes one url.<agent>/git/<host>/.insteadOf entry per provider host.
func (i *ide) setupGit(ctx context.Context) {
	for ctx.Err() == nil {
		if err := i.configureGit(ctx); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
}

func (i *ide) configureGit(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, i.agent+"/v1/git/hosts", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var a gitHostsAnswer
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&a) != nil {
		return errors.New("hosts not available")
	}
	for _, h := range a.Hosts {
		if !hostRE.MatchString(h) {
			continue
		}
		key := "url." + i.agent + "/git/" + h + "/.insteadOf"
		if _, out, code := i.git(ctx, "", nil, "config", "--global", "--replace-all", key, "https://"+h+"/"); code != 0 {
			return fmt.Errorf("git config: %s", out)
		}
	}
	i.mu.Lock()
	i.gitHosts = a.Hosts
	i.mu.Unlock()
	return nil
}

var hostRE = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

// git runs git and returns stdout, stderr and the exit code. Git never prompts.
func (i *ide) git(ctx context.Context, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), append([]string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true"}, env...)...)
	var so, se bytes.Buffer
	c.Stdout, c.Stderr = &so, &se
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return so.String(), se.String(), ee.ExitCode()
		}
		return so.String(), err.Error(), -1
	}
	return so.String(), se.String(), 0
}

type gitStep struct {
	Name   string `json:"name"`
	Exit   int    `json:"exit"`
	Stderr string `json:"stderr"`
}

// targets are the only repositories POST /git-test works on. "session" is the GIT_URI of the launch, the other
// two are fixed paths on the first provider host (the seed of test/fake-git).
var targetPaths = map[string]string{"second": "/course/other.git", "public": "/open/public.git"}

// gitURL names the repository of a target; the host must be a provider host the agent reported.
func (i *ide) gitURL(target string) (string, error) {
	i.mu.Lock()
	hosts, data := slices.Clone(i.gitHosts), i.state.Data["GIT_URI"]
	i.mu.Unlock()
	if len(hosts) == 0 {
		return "", errors.New("no git hosts")
	}
	if target != "session" {
		return "https://" + hosts[0] + targetPaths[target], nil
	}
	u, err := url.Parse(data)
	if err != nil || u.Scheme != "https" || !slices.Contains(hosts, u.Hostname()) {
		return "", errors.New("GIT_URI is not a provider repository")
	}
	return "https://" + u.Hostname() + u.Path, nil
}

// gitTest is POST /git-test?target=session|second|public: clone, commit, push, and report exit codes and
// scrubbed stderr only. Like every endpoint of the fake IDE it is reachable through the gateway by the owner of
// the session only.
func (i *ide) gitTest(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target != "session" && targetPaths[target] == "" {
		http.Error(w, "target must be session, second or public", http.StatusBadRequest)
		return
	}
	repo, err := i.gitURL(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	dir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		http.Error(w, "no temp dir", http.StatusInternalServerError)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	work := filepath.Join(dir, "repo")
	steps := []gitStep{}
	step := func(name string, env []string, dir string, args ...string) bool {
		_, se, code := i.git(r.Context(), dir, env, args...)
		steps = append(steps, gitStep{Name: name, Exit: code, Stderr: scrub(se)})
		return code == 0
	}
	i.mu.Lock()
	name, mail := i.state.Data["GIT_USER"], i.state.Data["GIT_MAIL"]
	i.mu.Unlock()
	ident := []string{"GIT_AUTHOR_NAME=" + orDefault(name, "e2e"), "GIT_COMMITTER_NAME=" + orDefault(name, "e2e"),
		"GIT_AUTHOR_EMAIL=" + orDefault(mail, "e2e@example.invalid"), "GIT_COMMITTER_EMAIL=" + orDefault(mail, "e2e@example.invalid")}
	if step("clone", nil, "", "clone", "--quiet", repo, work) {
		file := fmt.Sprintf("e2e-%d.txt", time.Now().UnixNano())
		if err := os.WriteFile(filepath.Join(work, file), []byte("e2e\n"), 0o600); err == nil &&
			step("commit", ident, work, "add", file) && step("commit", ident, work, "commit", "--quiet", "-m", "e2e commit") {
			step("push", nil, work, "push", "--quiet", "origin", "HEAD:main")
		}
	}
	writeJSON(w, map[string]any{"steps": steps})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

var (
	userinfoRE = regexp.MustCompile(`://[^/@\s]+@`)
	secretRE   = regexp.MustCompile(`(?i)(bearer|basic)\s+\S+|(authorization|token|password|secret)\S*|[A-Za-z0-9_-]{24,}`)
)

// scrub removes what looks like a credential from Git's stderr before it leaves the container.
func scrub(s string) string {
	s = userinfoRE.ReplaceAllString(s, "://[redacted]@")
	return secretRE.ReplaceAllString(s, "[redacted]")
}

type attempt struct {
	Status int  `json:"status"`
	Error  bool `json:"error"`
}

// tryApprove is POST /try-approve: code in a session pod tries to approve a repository the way a user would,
// without a bearer token, at the API of the service and at the Git path of the agent. It reports status codes
// only. A connection that fails (NetworkPolicy, TLS) counts as blocked: error true. The targets are fixed.
func (i *ide) tryApprove(w http.ResponseWriter, r *http.Request) {
	svc := env("FAKE_IDE_SERVICE_URL", "http://eduide-eduide-control-plane-service.eduide-e2e.svc:8080")
	i.mu.Lock()
	hosts := slices.Clone(i.gitHosts)
	i.mu.Unlock()
	put := func(u string) attempt {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPut, u, strings.NewReader(`{"repository":"https://example.invalid/a/b","access":"read"}`))
		if err != nil {
			return attempt{Error: true}
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return attempt{Error: true}
		}
		_ = resp.Body.Close()
		return attempt{Status: resp.StatusCode}
	}
	out := map[string]attempt{"service": put(svc + "/api/v1/sessions/s-none/git/repositories")}
	if len(hosts) > 0 {
		out["agent"] = put(i.agent + "/git/" + hosts[0] + "/course/other.git/info/refs")
	}
	writeJSON(w, out)
}
