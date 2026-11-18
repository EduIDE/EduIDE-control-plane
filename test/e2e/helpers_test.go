//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// testHTTPClient resolves every *.e2e.test name to the gateway port on 127.0.0.1 and trusts the test CA.
func testHTTPClient(t testing.TB, jar bool, follow bool) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(envOr("E2E_CA_FILE", ".e2e/certs/ca.crt"))
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	d := &net.Dialer{Timeout: 10 * time.Second}
	c := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				if strings.HasSuffix(host, ".e2e.test") {
					addr = net.JoinHostPort("127.0.0.1", hostPort)
				}
				return d.DialContext(ctx, network, addr)
			},
		},
	}
	if jar {
		c.Jar, _ = cookiejar.New(nil)
	}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

type user struct{ name, password string }

var (
	alice = user{"alice", "alice"}
	bob   = user{"bob", "bob"}
)

// token is the access token of the public client by password grant.
func token(t testing.TB, u user) string {
	t.Helper()
	resp, err := testHTTPClient(t, false, true).PostForm(issuer+"/protocol/openid-connect/token", url.Values{
		"grant_type": {"password"}, "client_id": {"eduide-landing"},
		"username": {u.name}, "password": {u.password}, "scope": {"openid"},
	})
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || json.Unmarshal(b, &out) != nil || out.AccessToken == "" {
		t.Fatalf("token for %s: status %d", u.name, resp.StatusCode)
	}
	return out.AccessToken
}

// claims decodes the payload of a JWT without verifying it (the test only reads its own token).
func claims(t testing.TB, jwt string) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var formAction = regexp.MustCompile(`<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"`)

// browserLogin does what a browser does at the gateway: it opens a URL without cookies, follows the
// redirects to the Keycloak login page, posts the credentials and follows the redirects back.
// The returned client holds the cookies of the gateway and of Keycloak.
func browserLogin(t testing.TB, u user, startURL string) *http.Client {
	t.Helper()
	c := testHTTPClient(t, true, true)
	resp, err := c.Get(startURL)
	if err != nil {
		t.Fatalf("login start: %v", err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	m := formAction.FindSubmatch(page)
	if m == nil {
		t.Fatalf("no Keycloak login form after %s (status %d, host %s)", startURL, resp.StatusCode, resp.Request.URL.Host)
	}
	resp, err = c.PostForm(html.UnescapeString(string(m[1])), url.Values{"username": {u.name}, "password": {u.password}, "credentialId": {""}})
	if err != nil {
		t.Fatalf("login post: %v", err)
	}
	_ = resp.Body.Close()
	if resp.Request.URL.Host == keycloakHost {
		t.Fatalf("login of %s did not leave Keycloak (status %d)", u.name, resp.StatusCode)
	}
	return c
}

// ---- REST API -------------------------------------------------------------------------------

type Session struct {
	Name          string    `json:"name"`
	Template      string    `json:"template"`
	Workspace     string    `json:"workspace"`
	Phase         string    `json:"phase"`
	URL           string    `json:"url"`
	StartStrategy string    `json:"startStrategy"`
	ErrorCode     string    `json:"errorCode"`
	Message       string    `json:"message"`
	CreatedAt     time.Time `json:"createdAt"`
	ReadyAt       time.Time `json:"readyAt"`
}

type Problem struct {
	Status   int       `json:"status"`
	Code     string    `json:"code"`
	Sessions []Session `json:"sessions"`
}

type LaunchRequest struct {
	Template    string            `json:"template"`
	Persistence string            `json:"persistence,omitempty"`
	Workspace   string            `json:"workspace,omitempty"`
	Data        map[string]string `json:"data,omitempty"`
}

type api struct {
	t     testing.TB
	c     *http.Client
	token string
}

func newAPI(t testing.TB, u user) *api {
	return &api{t: t, c: testHTTPClient(t, false, true), token: token(t, u)}
}

// do sends a request and returns status and body.
func (a *api) do(method, path string, body any) (int, []byte) {
	a.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, apiBase+path, r)
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// launch posts a launch request; the session is deleted when the test ends.
func (a *api) launch(req LaunchRequest) (int, Session, Problem) {
	a.t.Helper()
	status, b := a.do(http.MethodPost, "/sessions", req)
	var s Session
	var p Problem
	if status == 200 || status == 201 {
		_ = json.Unmarshal(b, &s)
		a.t.Cleanup(func() { a.release(s.Name) })
	} else {
		_ = json.Unmarshal(b, &p)
	}
	return status, s, p
}

func (a *api) mustLaunch(req LaunchRequest) Session {
	a.t.Helper()
	status, s, p := a.launch(req)
	if status != 201 {
		a.t.Fatalf("launch %+v: status %d code %q", req, status, p.Code)
	}
	return s
}

func (a *api) get(name string) (int, Session) {
	a.t.Helper()
	status, b := a.do(http.MethodGet, "/sessions/"+name, nil)
	var s Session
	_ = json.Unmarshal(b, &s)
	return status, s
}

// release deletes a session and waits until it is gone, so the session limit stays free for the next test.
func (a *api) release(name string) {
	status, _ := a.do(http.MethodDelete, "/sessions/"+name, nil)
	if status != 204 && status != 404 {
		a.t.Logf("release %s: status %d", name, status)
		return
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, s := a.get(name); s.Name == "" {
			return
		}
		time.Sleep(time.Second)
	}
	a.t.Logf("release %s: still listed after 90s", name)
}

// waitPhase polls until the session has one of the phases and returns it with the elapsed time.
func (a *api) waitPhase(name string, timeout time.Duration, phases ...string) Session {
	a.t.Helper()
	deadline := time.Now().Add(timeout)
	var last Session
	for time.Now().Before(deadline) {
		_, last = a.get(name)
		for _, p := range phases {
			if last.Phase == p {
				return last
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	a.t.Fatalf("session %s: phase %q (%s) after %s, wanted %v", name, last.Phase, last.Message, timeout, phases)
	return last
}

// sseEvents reads the events of a session until the stream closes or the context ends.
func (a *api) sseEvents(ctx context.Context, name string) ([]Session, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/sessions/"+name+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "text/event-stream")
	hc := testHTTPClient(a.t, false, true)
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return nil, fmt.Errorf("events: status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var out []Session
	var event string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:") && event == "session":
			var s Session
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &s) == nil {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// ---- session URL helpers --------------------------------------------------------------------

// dataState is the answer of the fake IDE; it never contains values.
type dataState struct {
	Claimed bool     `json:"claimed"`
	Keys    []string `json:"keys"`
	SHA256  string   `json:"sha256"`
	Notices []string `json:"notices"`
}

// fetchDataState reads /data-state of a session through the gateway with a logged-in client.
func fetchDataState(c *http.Client, sessionURL string) (dataState, int, error) {
	var ds dataState
	resp, err := c.Get(sessionURL + "data-state")
	if err != nil {
		return ds, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 200 {
		_ = json.NewDecoder(resp.Body).Decode(&ds)
	}
	return ds, resp.StatusCode, nil
}

func eventually(t testing.TB, timeout time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %s: %s", timeout, what)
}
