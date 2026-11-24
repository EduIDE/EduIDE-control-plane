package gitproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/agent"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

// These tests run a real git binary through the real agent and git-proxy handlers against a fake provider
// (git http-backend behind httptest) and a fake authorize service:
//
//	git --http--> agent local handler --http--> git-proxy --https--> fake provider (git http-backend)
//	                                               |
//	                                               +--http--> fake authorize service

const (
	host         = "git.test"
	instanceName = "inst1"
	instanceTok  = "instance-token-abcdef"
	upstreamTok  = "upstream-secret-token-xyz"
	landing      = "https://landing.test"
	displayName  = "Test Git"
)

// seenRequest is what the fake provider received.
type seenRequest struct{ path, auth string }

type provider struct {
	root string
	cgi  *cgi.Handler
	mu   sync.Mutex
	seen []seenRequest
}

func newProvider(t *testing.T, gitExec string) *provider {
	root := t.TempDir()
	return &provider{root: root, cgi: &cgi.Handler{Stderr: io.Discard,
		Path: filepath.Join(gitExec, "git-http-backend"), Root: "/", InheritEnv: []string{"PATH", "HOME"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1",
			// push without REMOTE_USER, and a push body larger than the CGI default buffer for chunked requests
			"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=http.receivepack", "GIT_CONFIG_VALUE_0=true",
			"GIT_CONFIG_KEY_1=http.maxRequestBuffer", "GIT_CONFIG_VALUE_1=500m"},
	}}
}

// ServeHTTP behaves like GitHub for the cases the tests need: /priv/ needs basic auth, pushes always do, and an
// unauthorized answer is a 401 with WWW-Authenticate, which would make git ask for a password.
func (p *provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.seen = append(p.seen, seenRequest{r.URL.Path, r.Header.Get("Authorization")})
	p.mu.Unlock()
	push := r.URL.Query().Get("service") == "git-receive-pack" || strings.HasSuffix(r.URL.Path, "/git-receive-pack")
	if strings.HasPrefix(r.URL.Path, "/priv/") || push {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+upstreamTok))
		if r.Header.Get("Authorization") != want {
			w.Header().Set("WWW-Authenticate", `Basic realm="provider"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	segs := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(segs) == 3 && !strings.HasSuffix(segs[1], ".git") {
		segs[1] += ".git"
		r.URL.Path = "/" + strings.Join(segs, "/")
	}
	if r.ContentLength < 0 {
		// Go's CGI handler refuses chunked bodies. Spool to disk (not to memory) and give the length.
		f, err := os.CreateTemp(p.root, "body-*")
		if err == nil {
			defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
			n, cerr := io.Copy(f, r.Body)
			if _, serr := f.Seek(0, io.SeekStart); cerr != nil || serr != nil {
				http.Error(w, "spool failed", http.StatusInternalServerError)
				return
			}
			r.Body, r.ContentLength, r.TransferEncoding = f, n, nil
		}
	}
	p.cgi.ServeHTTP(w, r)
}

func (p *provider) auths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, s := range p.seen {
		out = append(out, s.auth)
	}
	return out
}

func (p *provider) reset() { p.mu.Lock(); p.seen = nil; p.mu.Unlock() }

// authorizeFake implements the service side of the contract with the push rule, a switchable account and the
// approvals of one session (mode "" means off).
type authorizeFake struct {
	connected   atomic.Bool
	sessionRepo atomic.Value // string, "host/path" or ""
	calls       atomic.Int64
	mu          sync.Mutex
	mode        string
	approved    map[string]string // "host/path" -> "read" or "write"
}

func (a *authorizeFake) approve(repo, access string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.approved == nil {
		a.approved = map[string]string{}
	}
	if access == "" {
		delete(a.approved, repo)
		return
	}
	a.approved[repo] = access
}

func (a *authorizeFake) access(host, repo string) string {
	sr, _ := a.sessionRepo.Load().(string)
	sh, sp, _ := strings.Cut(sr, "/")
	if sr != "" && gitapi.SameRepository(host, repo, sh, sp) {
		return "write"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.approved[host+"/"+gitapi.NormalizeRepo(repo)]
}

func (a *authorizeFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.calls.Add(1)
	if r.Header.Get("Authorization") != "Bearer "+instanceTok {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	var req gitapi.AuthorizeRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	sr, _ := a.sessionRepo.Load().(string)
	acct := a.connected.Load()
	a.mu.Lock()
	mode := a.mode
	a.mu.Unlock()
	have := a.access(req.Host, req.Repository)
	if mode == "" {
		have = ""
		if sr != "" && gitapi.SameRepository(req.Host, req.Repository, strings.SplitN(sr, "/", 2)[0], strings.SplitN(sr, "/", 2)[1]) {
			have = "write"
		}
	}
	resp := gitapi.AuthorizeResponse{Allow: true, User: "u-123", Session: "s-1", Connected: acct, SessionRepository: sr}
	switch {
	case have == "" || have == "read" && req.Kind == gitapi.KindPush:
		approvable := acct && (mode == "write" || mode == "read" && req.Kind == gitapi.KindRead)
		switch {
		case req.Kind == gitapi.KindPush && approvable:
			resp = gitapi.AuthorizeResponse{Reason: gitapi.ReasonPushRule, User: "u-123", Session: "s-1",
				Message: gitapi.ApproveMessage(landing, "s-1", req.Host, req.Repository, "write")}
		case req.Kind == gitapi.KindPush:
			shown := "none"
			if sr != "" {
				shown = sr
			}
			resp = gitapi.AuthorizeResponse{Reason: gitapi.ReasonPushRule, User: "u-123", Session: "s-1",
				Message: "EduIDE only pushes to the repository this session was started with (" + shown + "). Start a session from " + req.Host + "/" + req.Repository + " to push there."}
		default:
			resp.Reason = gitapi.ReasonAnonymousOther
			if approvable {
				resp.ApprovalMode = mode
			}
		}
	case acct:
		resp.Reason, resp.Upstream = gitapi.ReasonOK, &gitapi.Upstream{Username: "x-access-token", Password: upstreamTok}
	case req.Kind == gitapi.KindPush:
		resp = gitapi.AuthorizeResponse{Reason: gitapi.ReasonConnect, User: "u-123", Session: "s-1", Message: gitapi.ConnectMessage(displayName, landing)}
	default:
		resp.Reason = gitapi.ReasonAnonymous
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// leakScanner fails the test when a token shows up in what git receives: headers, and the body across chunk borders.
type leakScanner struct {
	t      *testing.T
	tokens [][]byte
	mu     sync.Mutex
	bad    []string
}

func (l *leakScanner) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &scanWriter{ResponseWriter: w, l: l}
		next.ServeHTTP(sw, r)
		l.checkHeader(sw.Header())
	})
}

func (l *leakScanner) checkHeader(h http.Header) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, v := range h {
		if strings.EqualFold(k, "WWW-Authenticate") || strings.EqualFold(k, "Set-Cookie") {
			l.bad = append(l.bad, "header "+k)
		}
		for _, tok := range l.tokens {
			if bytes.Contains([]byte(strings.Join(v, ",")), tok) {
				l.bad = append(l.bad, "token in header "+k)
			}
		}
	}
}

type scanWriter struct {
	http.ResponseWriter
	l     *leakScanner
	carry []byte
	code  int
}

func (s *scanWriter) WriteHeader(c int) {
	s.code = c
	s.l.checkHeader(s.Header())
	if c == http.StatusUnauthorized {
		s.l.mu.Lock()
		s.l.bad = append(s.l.bad, "status 401 reached git")
		s.l.mu.Unlock()
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *scanWriter) Write(p []byte) (int, error) {
	for _, tok := range s.l.tokens {
		buf := append(append([]byte{}, s.carry...), p...)
		if bytes.Contains(buf, tok) {
			s.l.mu.Lock()
			s.l.bad = append(s.l.bad, "token in body")
			s.l.mu.Unlock()
		}
		if n := len(tok) - 1; len(buf) > n {
			s.carry = append(s.carry[:0], buf[len(buf)-n:]...)
		} else {
			s.carry = buf
		}
	}
	return s.ResponseWriter.Write(p)
}

func (s *scanWriter) Flush()                      { _ = http.NewResponseController(s.ResponseWriter).Flush() }
func (s *scanWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	t         *testing.T
	home      string
	agentURL  string
	prov      *provider
	az        *authorizeFake
	proxy     *Handler
	clk       *clock
	leaks     *leakScanner
	logs      *syncBuffer
	askpass   string
	askMarker string
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newRig(t *testing.T) *rig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found: the integration test needs a real git")
	}
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git --exec-path failed: " + err.Error())
	}
	gitExec := strings.TrimSpace(string(out))
	if _, err := os.Stat(filepath.Join(gitExec, "git-http-backend")); err != nil {
		t.Skip("git http-backend not found: the integration test needs it as the fake provider")
	}
	r := &rig{t: t, home: t.TempDir(), prov: newProvider(t, gitExec), az: &authorizeFake{}, clk: &clock{t: time.Now()}, logs: &syncBuffer{}}

	up := httptest.NewTLSServer(r.prov)
	t.Cleanup(up.Close)
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	tr := &http.Transport{
		// every host name goes to the fake provider; the httptest certificate is valid for example.com
		DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, n, up.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12},
	}
	azs := httptest.NewServer(r.az)
	t.Cleanup(azs.Close)
	log := slog.New(slog.NewJSONHandler(r.logs, nil))
	r.proxy = New([]config.GitProvider{{Name: "gitprov", Host: host, DisplayName: displayName}}, landing, &ServiceClient{URL: azs.URL, HTTP: azs.Client()}, tr, log)
	r.proxy.cache.now = r.clk.now
	ps := httptest.NewServer(r.proxy)
	t.Cleanup(ps.Close)

	a := agent.New(instanceTok, "3000", slog.New(slog.DiscardHandler))
	a.PathPrefix = "/" + instanceName + "/"
	if err := a.EnableGit(ps.URL, []string{host}, landing); err != nil {
		t.Fatal(err)
	}
	r.leaks = &leakScanner{t: t, tokens: [][]byte{[]byte(upstreamTok)}}
	as := httptest.NewServer(r.leaks.wrap(a.LocalHandler()))
	t.Cleanup(as.Close)
	r.agentURL = as.URL
	r.claim(a)

	r.askMarker = filepath.Join(r.home, "asked")
	r.askpass = filepath.Join(r.home, "askpass.sh")
	if err := os.WriteFile(r.askpass, []byte("#!/bin/sh\ntouch "+r.askMarker+"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	r.mustRun("", "config", "--global", "url."+r.agentURL+"/git/"+host+"/.insteadOf", "https://"+host+"/")
	t.Cleanup(func() {
		if _, err := os.Stat(r.askMarker); err == nil {
			t.Error("git asked for a credential")
		}
		r.leaks.mu.Lock()
		defer r.leaks.mu.Unlock()
		if len(r.leaks.bad) > 0 {
			t.Errorf("leaks towards git: %v", r.leaks.bad)
		}
		if strings.Contains(r.logs.String(), upstreamTok) || strings.Contains(r.logs.String(), instanceTok) {
			t.Error("a token is in the log")
		}
	})
	return r
}

func (r *rig) claim(a *agent.Agent) {
	req := httptest.NewRequest(http.MethodPost, "/v1/claim", strings.NewReader(`{"session":"s-1","data":{}}`))
	req.Header.Set("Authorization", "Bearer "+instanceTok)
	rec := httptest.NewRecorder()
	a.ControlHandler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		r.t.Fatalf("claim: %d", rec.Code)
	}
}

// run runs git in dir. The environment isolates git from the machine and wires a credential prompt that fails the test.
func (r *rig) run(dir string, args ...string) (string, error) {
	return r.runEnv(dir, true, args...)
}

func (r *rig) runEnv(dir string, noPrompt bool, args ...string) (string, error) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + r.home, "GIT_CONFIG_GLOBAL=" + filepath.Join(r.home, ".gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_ASKPASS=" + r.askpass, "SSH_ASKPASS=" + r.askpass, "LC_ALL=C",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.org", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.org"}
	if os.Getenv("GIT_TRACE_CURL") != "" {
		cmd.Env = append(cmd.Env, "GIT_TRACE_CURL=1")
	}
	if noPrompt {
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	}
	b, err := cmd.CombinedOutput()
	return string(b), err
}

func (r *rig) mustRun(dir string, args ...string) string {
	r.t.Helper()
	out, err := r.run(dir, args...)
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// seed creates the bare repository <root>/<name>.git with one commit holding the files.
func (r *rig) seed(name string, files map[string][]byte) {
	r.t.Helper()
	bare := filepath.Join(r.prov.root, name+".git")
	r.mustRun("", "init", "--bare", "-b", "main", bare)
	work := r.t.TempDir()
	r.mustRun(work, "init", "-b", "main")
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(work, f), c, 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	r.mustRun(work, "add", ".")
	r.mustRun(work, "commit", "-m", "seed")
	r.mustRun(work, "push", bare, "main")
}

func (r *rig) head(name string) string {
	return strings.TrimSpace(r.mustRun(filepath.Join(r.prov.root, name+".git"), "rev-parse", "main"))
}

// connect switches the fake account and ends the proxy's short cache, as the passing of time would.
func (r *rig) connect(on bool) {
	r.az.connected.Store(on)
	r.clk.add(time.Minute)
}

func (r *rig) commit(dir, file, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
	r.mustRun(dir, "add", ".")
	r.mustRun(dir, "commit", "-m", "change "+file)
}

func TestGitThroughAgentAndProxy(t *testing.T) {
	r := newRig(t)
	r.seed("pub/open", map[string][]byte{"README": []byte("public\n")})
	r.seed("priv/secret", map[string][]byte{"README": []byte("private\n")})
	r.seed("priv/second", map[string][]byte{"README": []byte("second\n")})
	r.az.sessionRepo.Store(host + "/priv/secret")

	for _, proto := range []string{"0", "2"} {
		t.Run("protocol v"+proto, func(t *testing.T) {
			pc := "protocol.version=" + proto
			dir := t.TempDir()

			// Anonymous clone of a public repository passes through without any credential.
			r.connect(false)
			r.prov.reset()
			r.mustRun(dir, "-c", pc, "clone", "https://"+host+"/pub/open.git", "pub")
			for _, a := range r.prov.auths() {
				if a != "" {
					t.Errorf("anonymous clone sent credentials to the provider: %q", a)
				}
			}

			// A private repository without an account: the message names the landing page, and git never prompts.
			out, err := r.runEnv(dir, proto == "0", "-c", pc, "clone", "https://"+host+"/priv/secret.git", "nope")
			if err == nil || !strings.Contains(out, "remote error: "+gitapi.ConnectMessage(displayName, landing)) {
				t.Fatalf("clone without account: err=%v\n%s", err, out)
			}

			// With an account the same clone works, and the remote URL keeps the provider's address.
			r.connect(true)
			r.prov.reset()
			r.mustRun(dir, "-c", pc, "clone", "https://"+host+"/priv/secret.git", "priv")
			if got := strings.TrimSpace(r.mustRun(filepath.Join(dir, "priv"), "config", "remote.origin.url")); got != "https://"+host+"/priv/secret.git" {
				t.Errorf("remote url %q", got)
			}
			sawBasic := false
			for _, a := range r.prov.auths() {
				sawBasic = sawBasic || a == "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+upstreamTok))
			}
			if !sawBasic {
				t.Error("the provider did not get the account credential")
			}

			// The account is used for the session repository only: another private repository is read anonymously,
			// fails with the message that says so, and no credential goes to the provider. A public one still works.
			r.prov.reset()
			out, err = r.run(dir, "-c", pc, "clone", "https://"+host+"/priv/second.git", "second")
			wantOther := "remote error: " + gitapi.OtherRepositoryMessage(displayName, host+"/priv/secret", host, "priv/second")
			if err == nil || !strings.Contains(out, wantOther) {
				t.Fatalf("clone of a second private repository: err=%v\n%s", err, out)
			}
			r.mustRun(dir, "-c", pc, "clone", "https://"+host+"/pub/open.git", "pub-with-account")
			for _, a := range r.prov.auths() {
				if a != "" {
					t.Errorf("the account credential went to the provider for another repository: %q", a)
				}
			}

			// Fetch brings a commit made at the provider.
			r.seedCommit("priv/secret", "later-"+proto, "x")
			r.mustRun(filepath.Join(dir, "priv"), "-c", pc, "fetch")
			if got := strings.TrimSpace(r.mustRun(filepath.Join(dir, "priv"), "rev-parse", "origin/main")); got != r.head("priv/secret") {
				t.Errorf("fetch got %s", got)
			}

			// Shallow clone.
			r.mustRun(dir, "-c", pc, "clone", "--depth", "1", "https://"+host+"/priv/secret.git", "shallow")
			if got := strings.TrimSpace(r.mustRun(filepath.Join(dir, "shallow"), "rev-list", "--count", "HEAD")); got != "1" {
				t.Errorf("shallow clone has %s commits", got)
			}

			// Push to the repository of the session.
			r.mustRun(filepath.Join(dir, "priv"), "pull", "-q", "--rebase")
			r.commit(filepath.Join(dir, "priv"), "mine-"+proto, "mine")
			r.mustRun(filepath.Join(dir, "priv"), "-c", pc, "push", "origin", "main")
			if want := strings.TrimSpace(r.mustRun(filepath.Join(dir, "priv"), "rev-parse", "HEAD")); r.head("priv/secret") != want {
				t.Errorf("push did not land upstream")
			}

			// Push to another repository: the rule message, and the repository stays as it was.
			before := r.head("pub/open")
			r.commit(filepath.Join(dir, "pub"), "other-"+proto, "other")
			out, err = r.run(filepath.Join(dir, "pub"), "-c", pc, "push", "origin", "main")
			want := "EduIDE only pushes to the repository this session was started with (" + host + "/priv/secret)"
			if err == nil || !strings.Contains(out, "remote error: "+want) {
				t.Fatalf("push elsewhere: err=%v\n%s", err, out)
			}
			if r.head("pub/open") != before {
				t.Error("the repository changed")
			}
		})
	}

	t.Run("push without account asks to connect", func(t *testing.T) {
		r.connect(false)
		dir := t.TempDir()
		r.mustRun(dir, "clone", "https://"+host+"/pub/open.git", "c")
		r.az.sessionRepo.Store(host + "/pub/open")
		r.commit(filepath.Join(dir, "c"), "f", "f")
		out, err := r.run(filepath.Join(dir, "c"), "push", "origin", "main")
		if err == nil || !strings.Contains(out, "remote error: "+gitapi.ConnectMessage(displayName, landing)) {
			t.Fatalf("err=%v\n%s", err, out)
		}
		r.az.sessionRepo.Store(host + "/priv/secret")
	})

	t.Run("unknown repository with an account", func(t *testing.T) {
		r.connect(true)
		r.az.sessionRepo.Store(host + "/priv/missing")
		defer r.az.sessionRepo.Store(host + "/priv/secret")
		out, err := r.run(t.TempDir(), "clone", "https://"+host+"/priv/missing.git", "x")
		if err == nil || !strings.Contains(out, "remote error: "+host+"/priv/missing: not found, or your "+displayName+" account has no access") {
			t.Fatalf("err=%v\n%s", err, out)
		}
	})

	t.Run("paths outside the allowlist and LFS", func(t *testing.T) {
		for _, tc := range []struct {
			method, path string
			code         int
		}{
			{"GET", "/git/" + host + "/priv/secret.git/HEAD", 404},
			{"GET", "/git/" + host + "/priv/secret.git/objects/info/packs", 404},
			{"GET", "/git/" + host + "/priv/secret.git/info/refs", 404}, // dumb protocol: no service parameter
			{"GET", "/git/" + host + "/priv/secret.git/info/refs?service=other", 404},
			{"POST", "/git/" + host + "/priv/secret.git/info/refs?service=git-upload-pack", 404},
			{"GET", "/git/other.test/a/b/info/refs?service=git-upload-pack", 404},
			{"GET", "/git/" + host + "/priv/../x/info/refs?service=git-upload-pack", 404},
			{"POST", "/git/" + host + "/priv/secret.git/info/lfs/objects/batch", 501},
		} {
			req, _ := http.NewRequest(tc.method, r.agentURL+tc.path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.code {
				t.Errorf("%s %s: %d, want %d", tc.method, tc.path, resp.StatusCode, tc.code)
			}
			if tc.code == 501 && (!strings.Contains(string(b), "Git LFS is not supported through EduIDE") || resp.Header.Get("Content-Type") != "application/vnd.git-lfs+json") {
				t.Errorf("LFS answer: %s %s", resp.Header.Get("Content-Type"), b)
			}
		}
	})

	t.Run("client credentials never reach the provider", func(t *testing.T) {
		r.connect(false)
		r.prov.reset()
		req, _ := http.NewRequest("GET", r.agentURL+"/git/"+host+"/pub/open.git/info/refs?service=git-upload-pack", nil)
		req.Header.Set("Authorization", "Bearer client-supplied")
		req.Header.Set("Cookie", "a=b")
		req.Header.Set("Proxy-Authorization", "Basic eHg6eHg=")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		for _, a := range r.prov.auths() {
			if a != "" {
				t.Errorf("provider saw Authorization %q", a)
			}
		}
	})

	t.Run("log lines", func(t *testing.T) {
		logs := r.logs.String()
		for _, want := range []string{`"result":"ok"`, `"kind":"push"`, `"reason":"push-rule"`, `"reason":"connect"`, `"user":"u-123"`, `"session":"s-1"`, `"provider":"gitprov"`, `"repository":"priv/secret"`} {
			if !strings.Contains(logs, want) {
				t.Errorf("log lacks %s", want)
			}
		}
		// reads that went fine are not logged: only pushes and failures
		for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
			if strings.Contains(line, `"kind":"read"`) && strings.Contains(line, `"result":"ok"`) {
				t.Errorf("a successful read is logged: %s", line)
			}
		}
	})
}

// seedCommit adds a commit to the bare repository through a temporary clone.
func (r *rig) seedCommit(name, file, content string) {
	r.t.Helper()
	bare := filepath.Join(r.prov.root, name+".git")
	work := r.t.TempDir()
	r.mustRun(work, "clone", bare, "w")
	r.commit(filepath.Join(work, "w"), file, content)
	r.mustRun(filepath.Join(work, "w"), "push", "origin", "main")
}

// A second private repository fails with the approval link; after the approval the clone works, and a push works
// only with access write. A removed approval ends it again.
func TestApprovedRepository(t *testing.T) {
	r := newRig(t)
	r.seed("priv/secret", map[string][]byte{"README": []byte("private\n")})
	r.seed("priv/second", map[string][]byte{"README": []byte("second\n")})
	r.az.sessionRepo.Store(host + "/priv/secret")
	r.az.mode = "write"
	r.connect(true)
	dir := t.TempDir()
	second := "https://" + host + "/priv/second.git"

	out, err := r.run(dir, "clone", second, "c")
	want := "remote error: " + gitapi.ApproveMessage(landing, "s-1", host, "priv/second", "read")
	if err == nil || !strings.Contains(out, want) {
		t.Fatalf("clone before approval: err=%v\n%s", err, out)
	}
	if !strings.Contains(out, "approveRepo=https%3A%2F%2F"+host+"%2Fpriv%2Fsecond") || !strings.Contains(out, "session=s-1") || strings.Contains(out, upstreamTok) {
		t.Errorf("the link is not as documented, or a token shows:\n%s", out)
	}

	r.az.approve(host+"/priv/second", "read")
	r.connect(true) // ends the proxy cache, as the passing of time would
	r.prov.reset()
	r.mustRun(dir, "clone", second, "c")
	sawBasic := false
	for _, a := range r.prov.auths() {
		sawBasic = sawBasic || a == "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+upstreamTok))
	}
	if !sawBasic {
		t.Error("an approved repository is read without the credential")
	}

	r.commit(filepath.Join(dir, "c"), "f", "f")
	out, err = r.run(filepath.Join(dir, "c"), "push", "origin", "main")
	wantPush := "remote error: " + gitapi.ApproveMessage(landing, "s-1", host, "priv/second", "write")
	if err == nil || !strings.Contains(out, wantPush) {
		t.Fatalf("push with read access: err=%v\n%s", err, out)
	}

	r.az.approve(host+"/priv/second", "write")
	r.connect(true)
	r.mustRun(filepath.Join(dir, "c"), "push", "origin", "main")
	if want := strings.TrimSpace(r.mustRun(filepath.Join(dir, "c"), "rev-parse", "HEAD")); r.head("priv/second") != want {
		t.Error("the push did not land")
	}

	r.az.approve(host+"/priv/second", "")
	r.connect(true)
	if _, err := r.run(dir, "clone", second, "again"); err == nil {
		t.Error("a removed approval still opens the repository")
	}

	// Mode read: the push refusal carries no link.
	r.az.approve(host+"/priv/second", "read")
	r.az.mu.Lock()
	r.az.mode = "read"
	r.az.mu.Unlock()
	r.connect(true)
	out, err = r.run(filepath.Join(dir, "c"), "push", "origin", "main")
	if err == nil || strings.Contains(out, "approveRepo") {
		t.Errorf("mode read: err=%v\n%s", err, out)
	}
}

func TestUnclaimedInstanceGetsNoGitPath(t *testing.T) {
	a := agent.New(instanceTok, "3000", slog.New(slog.DiscardHandler))
	a.PathPrefix = "/" + instanceName + "/"
	if err := a.EnableGit("http://127.0.0.1:1", []string{host}, landing); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.LocalHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/git/"+host+"/a/b/info/refs?service=git-upload-pack", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("idle instance: %d", rec.Code)
	}
}

// randomFile writes n incompressible bytes, so the pack is as large as the data.
func randomFile(t *testing.T, path string, n int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 1<<20)
	for written := 0; written < n; written += len(buf) {
		_, _ = rand.Read(buf)
		if _, err := f.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
}

// heapProbe samples the heap of this process, which holds the agent, the proxy and the fake provider and
// authorize service. The git client and git http-backend run as other processes. A forwarder that buffered
// a 50 MB pack would show up as a peak of that size.
type heapProbe struct {
	stop chan struct{}
	done chan struct{}
	peak uint64
}

func startHeapProbe() *heapProbe {
	p := &heapProbe{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		var m runtime.MemStats
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			runtime.ReadMemStats(&m)
			p.peak = max(p.peak, m.HeapAlloc)
			select {
			case <-p.stop:
				return
			case <-tick.C:
			}
		}
	}()
	return p
}

func (p *heapProbe) end() uint64 { close(p.stop); <-p.done; return p.peak }

func TestLargeRepositoryStreams(t *testing.T) {
	if testing.Short() {
		t.Skip("moves 100 MB")
	}
	r := newRig(t)
	const size = 50 << 20
	seedDir := t.TempDir()
	randomFile(t, filepath.Join(seedDir, "blob.bin"), size)
	bare := filepath.Join(r.prov.root, "priv", "big.git")
	r.mustRun("", "init", "--bare", "-b", "main", bare)
	r.mustRun(seedDir, "init", "-b", "main")
	r.mustRun(seedDir, "add", ".")
	r.mustRun(seedDir, "commit", "-m", "big")
	r.mustRun(seedDir, "push", bare, "main")
	r.connect(true)
	r.az.sessionRepo.Store(host + "/priv/big")

	// Collect garbage eagerly so the peak shows live data, not garbage waiting for the next cycle.
	old := debug.SetGCPercent(10)
	defer debug.SetGCPercent(old)
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	const allowed = 24 << 20 // half the pack

	dir := t.TempDir()
	probe := startHeapProbe()
	r.mustRun(dir, "clone", "https://"+host+"/priv/big.git", "big")
	cloneGrowth := int64(probe.end()) - int64(base.HeapAlloc)

	randomFile(t, filepath.Join(dir, "big", "blob2.bin"), size)
	r.mustRun(filepath.Join(dir, "big"), "add", ".")
	r.mustRun(filepath.Join(dir, "big"), "commit", "-m", "more")
	runtime.GC()
	runtime.ReadMemStats(&base)
	probe = startHeapProbe()
	r.mustRun(filepath.Join(dir, "big"), "push", "origin", "main")
	pushGrowth := int64(probe.end()) - int64(base.HeapAlloc)

	t.Logf("heap growth: clone %d KiB, push %d KiB (pack %d KiB each)", cloneGrowth>>10, pushGrowth>>10, size>>10)
	if cloneGrowth > allowed || pushGrowth > allowed {
		t.Errorf("heap grew by %d KiB (clone) and %d KiB (push), allowed %d KiB: the proxy buffers", cloneGrowth>>10, pushGrowth>>10, allowed>>10)
	}
	if want := strings.TrimSpace(r.mustRun(filepath.Join(dir, "big"), "rev-parse", "HEAD")); r.head("priv/big") != want {
		t.Error("the push did not land")
	}
	if fi, err := os.Stat(filepath.Join(dir, "big", "blob.bin")); err != nil || fi.Size() != size {
		t.Errorf("cloned file: %v", err)
	}
}
