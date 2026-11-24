package main

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	s   *server
	srv *httptest.Server
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{s: newServer("id", "secret", 75*time.Second, t.TempDir()), now: time.Now()}
	f.s.now = func() time.Time { return f.now }
	f.srv = httptest.NewServer(f.s.handler())
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) client() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func challenge(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// authorize returns the code the fake puts into the redirect.
func (f *fixture) authorize(t *testing.T, verifier string) string {
	t.Helper()
	q := url.Values{"client_id": {"id"}, "redirect_uri": {"https://landing.test/git/callback"}, "state": {"st"}, "response_type": {"code"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}
	resp, err := f.client().Get(f.srv.URL + "/oauth/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != 302 || err != nil || loc.Host != "landing.test" || loc.Query().Get("state") != "st" || loc.Query().Get("code") == "" {
		t.Fatalf("authorize: status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

func (f *fixture) token(t *testing.T, form url.Values) (int, string) {
	t.Helper()
	form.Set("client_id", "id")
	form.Set("client_secret", "secret")
	resp, err := f.client().PostForm(f.srv.URL+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf)
	sb.Write(buf[:n])
	return resp.StatusCode, sb.String()
}

func (f *fixture) exchange(t *testing.T, code, verifier string) (int, string) {
	return f.token(t, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {"https://landing.test/git/callback"}})
}

func TestPKCEAndSingleUseCode(t *testing.T) {
	f := newFixture(t)
	code := f.authorize(t, "verifier-1")
	if status, _ := f.exchange(t, code, "wrong"); status != 400 {
		t.Fatalf("wrong verifier: %d", status)
	}
	if status, _ := f.exchange(t, code, "verifier-1"); status != 200 {
		t.Fatalf("right verifier after a failed try: %d", status)
	}
	if status, body := f.exchange(t, code, "verifier-1"); status != 400 || !strings.Contains(body, "invalid_grant") {
		t.Fatalf("second use of the code: %d %s", status, body)
	}
}

func TestRefreshRotates(t *testing.T) {
	f := newFixture(t)
	f.exchange(t, f.authorize(t, "v"), "v")
	first := f.s.issued.Refresh[0]
	if status, _ := f.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first}}); status != 200 {
		t.Fatalf("refresh: %d", status)
	}
	if status, body := f.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first}}); status != 400 || !strings.Contains(body, "invalid_grant") {
		t.Fatalf("used refresh token: %d %s", status, body)
	}
	if f.s.counters["refreshes"] != 1 || f.s.counters["invalidRefreshes"] != 1 {
		t.Fatalf("counters %v", f.s.counters)
	}
}

func TestAccessTokenExpiresAndRevokes(t *testing.T) {
	f := newFixture(t)
	f.exchange(t, f.authorize(t, "v"), "v")
	access := f.s.issued.Access[0]
	f.s.mu.Lock()
	ok := f.s.valid(access)
	f.now = f.now.Add(76 * time.Second)
	expired := !f.s.valid(access)
	f.now = f.now.Add(-76 * time.Second)
	f.s.mu.Unlock()
	if !ok || !expired {
		t.Fatalf("valid %v expired %v", ok, expired)
	}
	resp, err := f.client().PostForm(f.srv.URL+"/oauth/revoke", url.Values{"token": {access}})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("revoke: %v", err)
	}
	_ = resp.Body.Close()
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if f.s.valid(access) || f.s.counters["revokes"] != 1 {
		t.Fatal("token still valid after revoke")
	}
}

func TestPrivateRepoNeedsToken(t *testing.T) {
	f := newFixture(t)
	for path, want := range map[string]int{
		"/course/ex1.git/info/refs?service=git-upload-pack":   401,
		"/open/public.git/info/refs?service=git-receive-pack": 401,
		"/nope/none.git/info/refs?service=git-upload-pack":    404,
		"/course/ex1/info/refs?service=git-upload-pack":       401,
	} {
		resp, err := f.client().Get(f.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	if st := f.s.stats["course/ex1"]; st == nil || st.Rejected != 2 || st.Users[""] != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestFirstCommand(t *testing.T) {
	line := "0000000000000000000000000000000000000000 1111111111111111111111111111111111111111 refs/heads/main\x00report-status"
	body := []byte(strings.Repeat("0", 0) + hex4(len(line)+4) + line + "0000PACK")
	p, ok := firstCommand(body)
	if !ok || p.Ref != "refs/heads/main" || p.New != strings.Repeat("1", 40) {
		t.Fatalf("%+v %v", p, ok)
	}
	if _, ok := firstCommand([]byte("zz")); ok {
		t.Fatal("garbage accepted")
	}
}

func hex4(n int) string {
	const d = "0123456789abcdef"
	return string([]byte{d[n>>12&15], d[n>>8&15], d[n>>4&15], d[n&15]})
}

// The seeded repositories answer real git clients: public anonymously, private with a valid token only.
func TestSeededReposClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newFixture(t)
	if err := f.s.seed(); err != nil {
		t.Fatal(err)
	}
	f.exchange(t, f.authorize(t, "v"), "v")
	access := f.s.issued.Access[0]
	clone := func(u string) error {
		return exec.Command("git", "clone", "-q", u, t.TempDir()+"/c").Run()
	}
	host := strings.TrimPrefix(f.srv.URL, "http://")
	if err := clone(f.srv.URL + "/open/public.git"); err != nil {
		t.Errorf("anonymous clone of the public repository: %v", err)
	}
	if err := clone(f.srv.URL + "/course/ex1.git"); err == nil {
		t.Error("anonymous clone of a private repository worked")
	}
	if err := clone("http://oauth2:" + access + "@" + host + "/course/ex1.git"); err != nil {
		t.Errorf("clone with token: %v", err)
	}
}
