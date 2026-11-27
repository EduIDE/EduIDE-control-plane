// Command fake-git is a Git provider for e2e tests. It speaks the GitLab flavour of OAuth (authorization code
// with PKCE, rotating refresh tokens, revoke), GET /api/v4/user, and Git smart HTTP for three seeded repositories:
//
//	course/ex1    private, the session repository of the tests
//	course/other  private, readable by the test account but not the session repository
//	open/public   public: anonymous reads, pushes need a token
//
// Private repositories and every push need basic auth with user oauth2 and a currently valid access token.
// A second listener (introspection, plain HTTP) shows what the fake saw, including the token values it issued,
// so tests can search logs and containers for them. It is test-only and never logs a token, code or state.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	accountID    = 4242
	accountLogin = "e2euser"
	accountName  = "E2E User"
	maxPushBody  = 8 << 20
)

// repos are seeded at startup; the bool is "private".
var repos = map[string]bool{"course/ex1": true, "course/other": true, "open/public": false}

type grant struct {
	revoked bool
}

type refreshToken struct {
	g    *grant
	used bool
}

type accessToken struct {
	g       *grant
	expires time.Time
}

type code struct {
	challenge, redirect string
	used                bool
}

type repoStats struct {
	Requests   int            `json:"requests"`
	Users      map[string]int `json:"users"`      // basic auth user name of a request, "" for none
	ValidToken int            `json:"validToken"` // requests with a currently valid access token
	Rejected   int            `json:"rejected"`   // answered 401
	Pushes     int            `json:"pushes"`
	LastPush   *push          `json:"lastPush,omitempty"`
}

type push struct {
	Ref string `json:"ref"`
	New string `json:"new"`
}

type server struct {
	clientID, clientSecret string
	// redirectURI is the one registered redirect URI of the application. A real provider refuses any other.
	redirectURI   string
	ttl           time.Duration
	root, httpCGI string

	mu       sync.Mutex
	codes    map[string]*code
	access   map[string]*accessToken
	refresh  map[string]*refreshToken
	issued   struct{ Codes, Access, Refresh []string }
	counters map[string]int
	stats    map[string]*repoStats
	now      func() time.Time
}

func main() {
	ttl, err := time.ParseDuration(env("FAKE_GIT_ACCESS_TTL", "75s"))
	if err != nil {
		log.Fatal(err)
	}
	s := newServer(env("FAKE_GIT_CLIENT_ID", "e2e-client"), env("FAKE_GIT_CLIENT_SECRET", "e2e-secret"), ttl, env("FAKE_GIT_ROOT", "/data"))
	s.redirectURI = env("FAKE_GIT_REDIRECT_URI", s.redirectURI)
	if err := s.seed(); err != nil {
		log.Fatal(err)
	}
	go func() {
		log.Fatal((&http.Server{Addr: ":9000", Handler: s.introspection(), ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
	}()
	log.Printf("fake-git listening on :8443 (introspection :9000), access token lifetime %s", ttl)
	srv := &http.Server{Addr: ":8443", Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServeTLS(env("FAKE_GIT_TLS_CERT", "/tls/tls.crt"), env("FAKE_GIT_TLS_KEY", "/tls/tls.key")))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func newServer(id, secret string, ttl time.Duration, root string) *server {
	return &server{clientID: id, clientSecret: secret, redirectURI: "https://landing.test/git/callback", ttl: ttl, root: root, now: time.Now,
		codes: map[string]*code{}, access: map[string]*accessToken{}, refresh: map[string]*refreshToken{},
		counters: map[string]int{}, stats: map[string]*repoStats{}}
}

func random(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.tokenEndpoint)
	mux.HandleFunc("POST /oauth/revoke", s.revoke)
	mux.HandleFunc("GET /api/v4/user", s.user)
	mux.HandleFunc("/", s.git)
	return logPaths(mux)
}

// logPaths logs method and path only: queries and headers carry codes, state and tokens.
func logPaths(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// authorize answers like a user who is logged in at the provider and clicks "Authorize".
func (s *server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(s.redirectURI)
	if q.Get("client_id") != s.clientID || q.Get("redirect_uri") != s.redirectURI || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || err != nil || redirect.Host == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	c := random("gc-")
	s.mu.Lock()
	s.codes[c] = &code{challenge: q.Get("code_challenge"), redirect: s.redirectURI}
	s.issued.Codes = append(s.issued.Codes, c)
	s.counters["authorizes"]++
	s.mu.Unlock()
	rq := redirect.Query()
	rq.Set("code", c)
	rq.Set("state", q.Get("state"))
	redirect.RawQuery = rq.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *server) tokenEndpoint(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.PostForm.Get("client_id") != s.clientID ||
		subtle.ConstantTimeCompare([]byte(r.PostForm.Get("client_secret")), []byte(s.clientSecret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	invalid := func() { writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"}) }
	var g *grant
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		c := s.codes[r.PostForm.Get("code")]
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if c == nil || c.used || c.redirect != r.PostForm.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
			s.counters["invalidExchanges"]++
			invalid()
			return
		}
		c.used = true
		g = &grant{}
		s.counters["exchanges"]++
	case "refresh_token":
		rt := s.refresh[r.PostForm.Get("refresh_token")]
		if rt == nil || rt.used || rt.g.revoked {
			s.counters["invalidRefreshes"]++
			invalid()
			return
		}
		rt.used = true // rotation: a refresh token works once
		g = rt.g
		s.counters["refreshes"]++
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	a, rf := random("gat-"), random("grt-")
	s.access[a] = &accessToken{g: g, expires: s.now().Add(s.ttl)}
	s.refresh[rf] = &refreshToken{g: g}
	s.issued.Access = append(s.issued.Access, a)
	s.issued.Refresh = append(s.issued.Refresh, rf)
	writeJSON(w, http.StatusOK, map[string]any{"access_token": a, "refresh_token": rf, "token_type": "Bearer",
		"expires_in": int64(s.ttl.Seconds()), "created_at": s.now().Unix()})
}

// revoke kills the whole grant, like GitLab does for an access token with its refresh token.
func (s *server) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	t := r.PostForm.Get("token")
	s.mu.Lock()
	s.counters["revokes"]++
	if a := s.access[t]; a != nil {
		a.g.revoked = true
	}
	if rt := s.refresh[t]; rt != nil {
		rt.g.revoked = true
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{})
}

// valid reports whether the access token is known, not expired and not revoked. Caller holds s.mu.
func (s *server) valid(t string) bool {
	a := s.access[t]
	return a != nil && !a.g.revoked && s.now().Before(a.expires)
}

func (s *server) user(w http.ResponseWriter, r *http.Request) {
	t, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	ok := s.valid(t)
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "401 Unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": accountID, "username": accountLogin, "name": accountName})
}

// git serves /{group}/{project}.git/... with git http-backend after the auth rules of the fake. Like GitLab, it
// answers a path without ".git" with a 301 to the ".git" form on GET and with a 422 on POST, before any auth
// rule. A client that leaves the suffix out (the git-proxy did once) fails here instead of on a real host.
func (s *server) git(w http.ResponseWriter, r *http.Request) {
	repo, tail, hasGit, ok := repoOf(r.URL.Path)
	private, known := repos[repo]
	if !ok || !known {
		http.NotFound(w, r)
		return
	}
	if !hasGit {
		if r.Method == http.MethodGet {
			u := *r.URL
			u.Path = "/" + repo + ".git" + tail
			http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
			return
		}
		http.Error(w, "the repository path needs the .git suffix", http.StatusUnprocessableEntity)
		return
	}
	user, pass, hasAuth := r.BasicAuth()
	isPush := tail == "/git-receive-pack" || r.URL.Query().Get("service") == "git-receive-pack"
	s.mu.Lock()
	st := s.stats[repo]
	if st == nil {
		st = &repoStats{Users: map[string]int{}}
		s.stats[repo] = st
	}
	st.Requests++
	if !hasAuth {
		user = ""
	}
	st.Users[user]++
	valid := hasAuth && user == "oauth2" && s.valid(pass)
	if valid {
		st.ValidToken++
	}
	denied := (private || isPush) && !valid
	if denied {
		st.Rejected++
	}
	s.mu.Unlock()
	if denied {
		w.Header().Set("WWW-Authenticate", `Basic realm="fake-git"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	r.URL.Path = "/" + repo + ".git" + tail // the layout of the bare repositories
	if tail == "/git-receive-pack" && r.Method == http.MethodPost {
		if !s.recordPush(w, r, repo) {
			return
		}
	}
	(&cgi.Handler{
		Path: s.httpCGI,
		Env: []string{"GIT_PROJECT_ROOT=" + s.root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=oauth2", "HOME=" + os.TempDir(),
			"GIT_CONFIG_NOSYSTEM=1"},
		InheritEnv: []string{"PATH"},
	}).ServeHTTP(w, r)
}

// repoOf splits "/group/project[.git]/info/refs" and the two service paths into "group/project", the tail and
// whether the path had ".git".
func repoOf(p string) (repo, tail string, hasGit, ok bool) {
	for _, t := range []string{"/info/refs", "/git-upload-pack", "/git-receive-pack"} {
		if r, found := strings.CutSuffix(p, t); found {
			repo, hasGit = strings.CutSuffix(strings.TrimPrefix(r, "/"), ".git")
			return repo, t, hasGit, true
		}
	}
	return "", "", false, false
}

// recordPush notes the first command of the push (old new ref) and hands the body on unchanged.
func (s *server) recordPush(w http.ResponseWriter, r *http.Request, repo string) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPushBody))
	if err != nil {
		http.Error(w, "push too large", http.StatusRequestEntityTooLarge)
		return false
	}
	r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	r.Header.Del("Transfer-Encoding")
	plain := body
	if r.Header.Get("Content-Encoding") == "gzip" {
		if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			plain, _ = io.ReadAll(io.LimitReader(zr, 4096))
		}
	}
	if p, ok := firstCommand(plain); ok {
		s.mu.Lock()
		s.stats[repo].Pushes++
		s.stats[repo].LastPush = &p
		s.mu.Unlock()
	}
	return true
}

// firstCommand reads "<old> <new> <ref>\0<capabilities>" from the first pkt-line of a receive-pack request.
func firstCommand(b []byte) (push, bool) {
	if len(b) < 4 {
		return push{}, false
	}
	n, err := strconv.ParseUint(string(b[:4]), 16, 16)
	if err != nil || n < 4 || int(n) > len(b) {
		return push{}, false
	}
	line, _, _ := strings.Cut(string(b[4:n]), "\x00")
	f := strings.Fields(line)
	if len(f) != 3 {
		return push{}, false
	}
	return push{Ref: f[2], New: f[1]}, true
}

// introspection is the test-only view of what the fake saw. It includes the issued token values on purpose.
func (s *server) introspection() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_test/state", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"counters": s.counters, "repos": s.stats, "accessTokenTTLSeconds": int64(s.ttl.Seconds()),
			"codes": s.issued.Codes, "accessTokens": s.issued.Access, "refreshTokens": s.issued.Refresh,
		})
	})
	return mux
}

// seed creates the bare repositories with one commit on main.
func (s *server) seed() error {
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return fmt.Errorf("git --exec-path: %w", err)
	}
	s.httpCGI = filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	work, err := os.MkdirTemp("", "seed")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	run := func(dir string, args ...string) error {
		c := exec.Command("git", append([]string{"-c", "user.name=seed", "-c", "user.email=seed@fake-git.invalid", "-c", "init.defaultBranch=main"}, args...)...)
		c.Dir = dir
		if b, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, b)
		}
		return nil
	}
	for name := range repos {
		bare := filepath.Join(s.root, name+".git")
		if _, err := os.Stat(bare); err == nil { // a restart of the container keeps the volume
			continue
		}
		src := filepath.Join(work, strings.ReplaceAll(name, "/", "-"))
		if err := os.MkdirAll(src, 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# "+name+"\n"), 0o600); err != nil {
			return err
		}
		for _, a := range [][]string{{"init"}, {"add", "."}, {"commit", "-m", "seed"}} {
			if err := run(src, a...); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(bare), 0o750); err != nil {
			return err
		}
		for _, a := range [][]string{{"init", "--bare", bare}, {"--git-dir", bare, "config", "http.receivepack", "true"}, {"push", bare, "main"}} {
			if err := run(src, a...); err != nil {
				return err
			}
		}
	}
	return nil
}
