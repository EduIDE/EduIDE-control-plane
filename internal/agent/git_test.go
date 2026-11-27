package agent

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

func gitAgent(t *testing.T, proxyURL string, claimed bool) *Agent {
	t.Helper()
	a := newAgent(t, "3000")
	a.PathPrefix = "/inst1/"
	if proxyURL != "" {
		if err := a.EnableGit(proxyURL, []string{"github.com", "gitlab.example.org"}, "https://ide.example.org"); err != nil {
			t.Fatal(err)
		}
	}
	if claimed {
		do(t, a.ControlHandler(), "POST", "/v1/claim", `{"session":"s1","data":{}}`, map[string]string{"Authorization": "Bearer " + tok})
	}
	return a
}

func TestGitFeatureOff(t *testing.T) {
	a := gitAgent(t, "", true)
	if rec := do(t, a.LocalHandler(), "GET", "/git/github.com/o/r/info/refs?service=git-upload-pack", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/git/ with the feature off: %d", rec.Code)
	}
	rec := do(t, a.LocalHandler(), "GET", "/v1/git/hosts", "", nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"hosts":[]}` {
		t.Errorf("hosts with the feature off: %d %s", rec.Code, rec.Body)
	}
}

func TestGitHosts(t *testing.T) {
	a := gitAgent(t, "http://127.0.0.1:1", false)
	rec := do(t, a.LocalHandler(), "GET", "/v1/git/hosts", "", nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"hosts":["github.com","gitlab.example.org"],"landingUrl":"https://ide.example.org"}` {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestGitRouteClaimedAndIdle(t *testing.T) {
	var got *http.Request
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("WWW-Authenticate", "Basic")
		w.Header().Set("Set-Cookie", "a=b")
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	path := "/git/github.com/o/r.git/info/refs?service=git-upload-pack"
	hdr := map[string]string{
		"Authorization": "Basic Zm9vOmJhcg==", "Cookie": "eduide-id=x; a=b", "Proxy-Authorization": "Basic eA==",
		gitapi.InstanceHeader: "other-instance", TokenHeader: "x", "Git-Protocol": "version=2",
	}

	idle := gitAgent(t, up.URL, false)
	if rec := do(t, idle.LocalHandler(), "GET", path, "", hdr); rec.Code != http.StatusForbidden || got != nil {
		t.Fatalf("idle instance: %d, upstream called: %v", rec.Code, got != nil)
	}

	a := gitAgent(t, up.URL, true)
	rec := do(t, a.LocalHandler(), "GET", path, "", hdr)
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("claimed: %d %s", rec.Code, rec.Body)
	}
	if got.URL.Path != "/github.com/o/r.git/info/refs" || got.URL.RawQuery != "service=git-upload-pack" {
		t.Errorf("forwarded as %s?%s", got.URL.Path, got.URL.RawQuery)
	}
	if got.Header.Get("Authorization") != "Bearer "+tok || got.Header.Get(gitapi.InstanceHeader) != "inst1" {
		t.Errorf("instance credentials: %v", got.Header)
	}
	for _, h := range []string{"Cookie", "Proxy-Authorization", TokenHeader} {
		if got.Header.Get(h) != "" {
			t.Errorf("%s reached the proxy", h)
		}
	}
	if got.Header.Get("Git-Protocol") != "version=2" {
		t.Error("Git-Protocol was dropped")
	}
	if rec.Header().Get("WWW-Authenticate") != "" || rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("response headers reach the IDE: %v", rec.Header())
	}
}

func TestEnableGitValidates(t *testing.T) {
	a := newAgent(t, "3000")
	if err := a.EnableGit("http://x:1", nil, ""); err == nil {
		t.Error("no instance name")
	}
	a.PathPrefix = "/i/"
	for _, u := range []string{"", "ftp://x", "http://", "not a url"} {
		if err := a.EnableGit(u, nil, ""); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

// A client that sends headers and stalls its body must not hold a connection or a goroutine once the agent
// answered. net/http discards the unread body after the handler returns with no deadline; the listener has no
// ReadTimeout on purpose. Needs a real server.
func TestStalledBodyOnTheGitRouteHoldsNothing(t *testing.T) {
	// The git-proxy answers without reading the body, like a denial, and ends the discard of the rest like the real one.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer func() { _ = http.NewResponseController(w).SetReadDeadline(time.Now()) }()
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer upstream.Close()
	for name, claimed := range map[string]bool{"unclaimed instance": false, "answer of the git-proxy": true} {
		t.Run(name, func(t *testing.T) {
			a := gitAgent(t, upstream.URL, claimed)
			srv := httptest.NewUnstartedServer(a.LocalHandler())
			srv.Config.IdleTimeout = 500 * time.Millisecond // as in cmd/agent: the idle connection after the answer is bounded
			srv.Start()
			t.Cleanup(srv.Close)                                                                    // runs after the client connections are closed
			get, err := http.Get(srv.URL + "/git/github.com/o/r/info/refs?service=git-upload-pack") // warm-up: connections of the proxy stay
			if err != nil {
				t.Fatal(err)
			}
			_ = get.Body.Close()
			base := runtime.NumGoroutine()
			var conns []net.Conn
			for range 10 {
				c, err := net.Dial("tcp", srv.Listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				_, _ = io.WriteString(c, "POST /git/github.com/o/r/git-upload-pack HTTP/1.1\r\nHost: x\r\nContent-Length: 100000\r\n\r\nxxxxxxxxxx")
				conns = append(conns, c)
			}
			for _, c := range conns {
				// The client gets the answer although its body never ends, and the server then ends the idle connection.
				_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
				resp, err := http.ReadResponse(bufio.NewReader(c), nil)
				if err != nil {
					t.Fatalf("no answer while the body stalls: %v", err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("status %d", resp.StatusCode)
				}
				if _, err := io.Copy(io.Discard, c); err != nil {
					t.Fatalf("the connection stays open: %v", err)
				}
			}
			for end := time.Now().Add(10 * time.Second); runtime.NumGoroutine() > base+3; time.Sleep(50 * time.Millisecond) {
				if time.Now().After(end) {
					t.Fatalf("%d goroutines, baseline %d", runtime.NumGoroutine(), base)
				}
			}
		})
	}
}

// Three POSTs through one client: a large streamed answer arrives whole every time, also when the git-proxy answers
// before it reads the body. The agent only ever expires the read deadline; a response is never cut.
func TestGitPostsStreamWholeAnswers(t *testing.T) {
	const size = 8 << 20
	for name, readBody := range map[string]bool{"git-proxy reads the body": true, "git-proxy answers early": false} {
		t.Run(name, func(t *testing.T) {
			chunk := bytes.Repeat([]byte("p"), 32<<10)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if readBody {
					_, _ = io.Copy(io.Discard, r.Body)
				}
				for sent := 0; sent < size; sent += len(chunk) {
					_, _ = w.Write(chunk)
				}
			}))
			defer upstream.Close()
			a := gitAgent(t, upstream.URL, true)
			srv := httptest.NewServer(a.LocalHandler())
			defer srv.Close()
			client := &http.Client{Transport: &http.Transport{}, Timeout: 20 * time.Second}
			defer client.CloseIdleConnections()
			for i := range 3 {
				resp, err := client.Post(srv.URL+"/git/github.com/o/r/git-upload-pack", "application/x-git-upload-pack-request", bytes.NewReader(bytes.Repeat([]byte("w"), 1<<20)))
				if err != nil {
					t.Fatalf("POST %d: %v", i+1, err)
				}
				n, err := io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if err != nil || n != size || resp.StatusCode != 200 {
					t.Fatalf("POST %d: status %d, %d of %d bytes, error %v", i+1, resp.StatusCode, n, size, err)
				}
				if !resp.Close {
					t.Errorf("POST %d: the connection of a request with a body stays open", i+1)
				}
			}
			// A request without a body keeps its connection.
			resp, err := client.Get(srv.URL + "/git/github.com/o/r/info/refs?service=git-upload-pack")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.Close {
				t.Error("GET got Connection: close")
			}
		})
	}
}
