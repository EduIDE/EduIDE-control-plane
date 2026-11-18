package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
)

const tok = "secret-token"

func newAgent(t *testing.T, idePort string) *Agent {
	t.Helper()
	return New(tok, idePort, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func portOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

func do(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var bearer = map[string]string{"Authorization": "Bearer " + tok}

func TestProxyTokenAndStrip(t *testing.T) {
	var gotHeader, gotPath string
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader, gotPath = r.Header.Get(TokenHeader), r.URL.Path
		_, _ = w.Write([]byte("ok"))
	}))
	defer ide.Close()
	p := newAgent(t, portOf(t, ide.URL)).ProxyHandler()

	for name, hdr := range map[string]map[string]string{
		"missing": nil,
		"wrong":   {TokenHeader: "nope"},
		"empty":   {TokenHeader: ""},
	} {
		if w := do(t, p, "GET", "/x", "", hdr); w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", name, w.Code)
		}
	}
	w := do(t, p, "GET", "/x/y", "", map[string]string{TokenHeader: tok})
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	if gotHeader != "" || gotPath != "/x/y" {
		t.Errorf("ide saw header %q path %q", gotHeader, gotPath)
	}
}

func TestProxyWebSocketRoundTrip(t *testing.T) {
	var sawToken string
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawToken = r.Header.Get(TokenHeader)
		c, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n')
		_, _ = rw.WriteString("echo:" + line)
		_ = rw.Flush()
	}))
	defer ide.Close()
	a := newAgent(t, portOf(t, ide.URL))
	px := httptest.NewServer(a.ProxyHandler())
	defer px.Close()

	c, err := net.Dial("tcp", strings.TrimPrefix(px.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = c.Write([]byte("GET /ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" + TokenHeader + ": " + tok + "\r\n\r\n"))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("handshake: %v %v", resp, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = c.Write([]byte("hello\n"))
	line, _ := br.ReadString('\n')
	if line != "echo:hello\n" {
		t.Fatalf("got %q", line)
	}
	if sawToken != "" {
		t.Errorf("ide saw token header")
	}
	// the IDE closed its side after echoing, so the count drops back to 0
	deadline := time.Now().Add(3 * time.Second)
	for a.conns.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := a.conns.Load(); n != 0 {
		t.Errorf("connections = %d, want 0", n)
	}
}

func TestProxyCountsOpenConnection(t *testing.T) {
	release := make(chan struct{})
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, rw, _ := http.NewResponseController(w).Hijack()
		defer func() { _ = c.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		<-release
	}))
	defer ide.Close()
	a := newAgent(t, portOf(t, ide.URL))
	px := httptest.NewServer(a.ProxyHandler())
	defer px.Close()
	c, _ := net.Dial("tcp", strings.TrimPrefix(px.URL, "http://"))
	defer func() { _ = c.Close() }()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" + TokenHeader + ": " + tok + "\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if n := do(t, a.ControlHandler(), "GET", "/v1/status", "", bearer); !strings.Contains(n.Body.String(), `"connections":1`) {
		t.Errorf("status %s", n.Body)
	}
	close(release)
}

func TestControlAuth(t *testing.T) {
	h := newAgent(t, "1").ControlHandler()
	for _, rq := range [][2]string{{"POST", "/v1/claim"}, {"GET", "/v1/status"}, {"POST", "/v1/notify"}} {
		for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer wrong"}, {"Authorization": tok}} {
			if w := do(t, h, rq[0], rq[1], `{}`, hdr); w.Code != 401 {
				t.Errorf("%v %v: got %d", rq, hdr, w.Code)
			}
		}
	}
	if w := do(t, h, "GET", "/healthz", "", nil); w.Code != 200 {
		t.Errorf("healthz %d", w.Code)
	}
}

func TestClaim(t *testing.T) {
	a := newAgent(t, "1")
	v0 := a.version
	h := a.ControlHandler()
	claim := func(s string) int {
		return do(t, h, "POST", "/v1/claim", `{"session":"`+s+`","data":{"A":"b"}}`, bearer).Code
	}
	if c := claim("s-1"); c != 200 {
		t.Fatalf("first claim %d", c)
	}
	if c := claim("s-1"); c != 200 {
		t.Errorf("same session %d", c)
	}
	if a.version != v0+1 {
		t.Errorf("idempotent claim bumped version to %d", a.version)
	}
	if c := claim("s-2"); c != 409 {
		t.Errorf("other session %d", c)
	}
	var st agentapi.Status
	_ = json.Unmarshal(do(t, h, "GET", "/v1/status", "", bearer).Body.Bytes(), &st)
	if !st.Claimed || !st.Ready || st.Session == nil || *st.Session != "s-1" || st.LastActivity != nil {
		t.Errorf("status %+v", st)
	}
}

func TestClaimBadBodies(t *testing.T) {
	h := newAgent(t, "1").ControlHandler()
	big := `{"session":"s","data":{"k":"` + strings.Repeat("x", maxClaimBytes) + `"}}`
	for body, want := range map[string]int{big: 413, `{`: 400, `{"data":{}}`: 400, `{"session":"s","data":{"a":1}}`: 400} {
		if w := do(t, h, "POST", "/v1/claim", body, bearer); w.Code != want {
			t.Errorf("%.30s: got %d want %d", body, w.Code, want)
		}
	}
}

func TestStatusActivityFromHeartbeatOnly(t *testing.T) {
	a := newAgent(t, "1")
	var st agentapi.Status
	get := func() {
		st = agentapi.Status{}
		_ = json.Unmarshal(do(t, a.ControlHandler(), "GET", "/v1/status", "", bearer).Body.Bytes(), &st)
	}
	get()
	if st.LastActivity != nil {
		t.Fatal("activity before heartbeat")
	}
	if w := do(t, a.LocalHandler(), "POST", "/v1/heartbeat", "", nil); w.Code != 204 {
		t.Fatal(w.Code)
	}
	get()
	if st.LastActivity == nil || time.Since(*st.LastActivity) > time.Minute {
		t.Errorf("lastActivity %v", st.LastActivity)
	}
}

func poll(t *testing.T, a *Agent, q string) (agentapi.LocalSession, time.Duration) {
	t.Helper()
	start := time.Now()
	w := do(t, a.LocalHandler(), "GET", "/v1/session?"+q, "", nil)
	var s agentapi.LocalSession
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s, time.Since(start)
}

func TestLongPoll(t *testing.T) {
	a := newAgent(t, "1")
	v0 := a.version
	after := func(n int) string { return "after=" + strconv.Itoa(v0+n) }
	if s, d := poll(t, a, after(0)+"&timeout=1"); s.Version != v0 || s.Claimed || d < 900*time.Millisecond {
		t.Errorf("timeout poll: %+v after %v", s, d)
	}
	type res struct {
		s agentapi.LocalSession
		d time.Duration
	}
	run := func(q string, trigger func()) res {
		ch := make(chan res)
		go func() { s, d := poll(t, a, q); ch <- res{s, d} }()
		time.Sleep(100 * time.Millisecond)
		trigger()
		return <-ch
	}
	r := run(after(0)+"&timeout=30", func() {
		do(t, a.ControlHandler(), "POST", "/v1/claim", `{"session":"s","data":{"A":"b"}}`, bearer)
	})
	if r.s.Version != v0+1 || !r.s.Claimed || (*r.s.Data)["A"] != "b" || r.d > 5*time.Second {
		t.Errorf("claim poll: %+v %v", r.s, r.d)
	}
	r = run(after(1)+"&timeout=30", func() {
		do(t, a.ControlHandler(), "POST", "/v1/notify", `{"kind":"idle-warning","message":"bye"}`, bearer)
	})
	if r.s.Version != v0+2 || r.s.Notices == nil || (*r.s.Notices)[0].Message != "bye" {
		t.Errorf("notify poll: %+v", r.s)
	}
	if s, d := poll(t, a, "after=0&timeout=30"); s.Version != v0+2 || d > time.Second {
		t.Errorf("immediate poll: %+v %v", s, d)
	}
}

func TestNotifyValidation(t *testing.T) {
	h := newAgent(t, "1").ControlHandler()
	for _, body := range []string{`{"kind":"bogus","message":"x"}`, `{"kind":"info","message":"` + strings.Repeat("x", 1025) + `"}`, `x`} {
		if w := do(t, h, "POST", "/v1/notify", body, bearer); w.Code != 400 {
			t.Errorf("%.30s: %d", body, w.Code)
		}
	}
}

func TestReadyzFlips(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portOf(t, "http://"+l.Addr().String())
	_ = l.Close()
	a := newAgent(t, port)
	if w := do(t, a.ControlHandler(), "GET", "/readyz", "", nil); w.Code != 503 {
		t.Fatalf("closed port: %d", w.Code)
	}
	l, err = net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Skip("port taken: ", err)
	}
	defer func() { _ = l.Close() }()
	time.Sleep(1100 * time.Millisecond) // cache TTL
	if w := do(t, a.ControlHandler(), "GET", "/readyz", "", nil); w.Code != 200 {
		t.Errorf("open port: %d", w.Code)
	}
}

func FuzzDecodeClaim(f *testing.F) {
	f.Add(`{"session":"s","data":{"a":"b"}}`)
	f.Add(`{"session":"s","traceparent":"x"}`)
	f.Add(`[`)
	f.Fuzz(func(t *testing.T, in string) {
		req, err := DecodeClaim(bytes.NewReader([]byte(in)))
		if err == nil && req.Session == "" {
			t.Fatal("accepted empty session")
		}
		if len(in) > maxClaimBytes && err == nil {
			t.Fatal("accepted oversized body")
		}
	})
}

func TestVersionGrowsAcrossRestart(t *testing.T) {
	a := newAgent(t, "1")
	do(t, a.ControlHandler(), "POST", "/v1/claim", `{"session":"s","data":{}}`, bearer)
	time.Sleep(5 * time.Millisecond)
	if b := newAgent(t, "1"); b.version <= a.version {
		t.Errorf("restarted agent version %d <= %d", b.version, a.version)
	}
}

func TestClaimResetsActivity(t *testing.T) {
	a := newAgent(t, "1")
	do(t, a.LocalHandler(), "POST", "/v1/heartbeat", "", nil)
	do(t, a.ControlHandler(), "POST", "/v1/claim", `{"session":"s","data":{}}`, bearer)
	var st agentapi.Status
	_ = json.Unmarshal(do(t, a.ControlHandler(), "GET", "/v1/status", "", bearer).Body.Bytes(), &st)
	if st.LastActivity != nil {
		t.Errorf("pre-claim heartbeat leaked into status: %v", st.LastActivity)
	}
}

func TestNotifyCountsCharacters(t *testing.T) {
	h := newAgent(t, "1").ControlHandler()
	if w := do(t, h, "POST", "/v1/notify", `{"kind":"info","message":"`+strings.Repeat("ä", 1024)+`"}`, bearer); w.Code != 204 {
		t.Errorf("1024 non-ASCII characters: %d", w.Code)
	}
}

// rawProxy sends a raw HTTP/1.1 request through a real proxy listener and returns the status and the headers the IDE saw.
func rawProxy(t *testing.T, req string) (int, http.Header) {
	t.Helper()
	seen := make(chan http.Header, 1)
	ide := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen <- r.Header }))
	defer ide.Close()
	px := httptest.NewServer(newAgent(t, portOf(t, ide.URL)).ProxyHandler())
	defer px.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(px.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = c.Write([]byte(req))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	select {
	case h := <-seen:
		return resp.StatusCode, h
	default:
		return resp.StatusCode, nil
	}
}

func TestProxyHeaderTricks(t *testing.T) {
	const head = "GET / HTTP/1.1\r\nHost: x\r\n"
	for name, tc := range map[string]struct {
		hdr  string
		want int
	}{
		"lowercase":           {"x-eduide-instance-token: " + tok + "\r\n", 200},
		"duplicate wrong":     {TokenHeader + ": " + tok + "\r\n" + TokenHeader + ": x\r\n", 403},
		"duplicate same":      {TokenHeader + ": " + tok + "\r\n" + TokenHeader + ": " + tok + "\r\n", 403},
		"comma list":          {TokenHeader + ": " + tok + ", x\r\n", 403},
		"hop-by-hop":          {"Connection: " + TokenHeader + "\r\n" + TokenHeader + ": " + tok + "\r\n", 200},
		"hop-by-hop no token": {"Connection: " + TokenHeader + "\r\n", 403},
	} {
		code, seen := rawProxy(t, head+tc.hdr+"\r\n")
		if code != tc.want {
			t.Errorf("%s: got %d want %d", name, code, tc.want)
		}
		for k := range seen {
			if strings.EqualFold(k, TokenHeader) {
				t.Errorf("%s: ide saw %s", name, k)
			}
		}
	}
}

func TestProxyKeepsForwardedHeaders(t *testing.T) {
	code, seen := rawProxy(t, "GET / HTTP/1.1\r\nHost: ide.example.org\r\n"+TokenHeader+": "+tok+
		"\r\nX-Forwarded-Proto: https\r\nX-Forwarded-Host: ide.example.org\r\nX-Forwarded-For: 192.0.2.1\r\n\r\n")
	if code != 200 || seen.Get("X-Forwarded-Proto") != "https" || seen.Get("X-Forwarded-Host") != "ide.example.org" ||
		seen.Get("X-Forwarded-For") != "192.0.2.1" {
		t.Errorf("code %d, ide saw %v", code, seen)
	}
}

func TestHeartbeatDropsIdleWarnings(t *testing.T) {
	a := newAgent(t, "1")
	for _, kind := range []string{"idle-warning", "max-warning"} {
		body := `{"kind":"` + kind + `","message":"soon"}`
		if w := do(t, a.ControlHandler(), "POST", "/v1/notify", body, bearer); w.Code != 204 {
			t.Fatalf("notify %s: %d", kind, w.Code)
		}
	}
	before, _ := poll(t, a, "after=0&timeout=0")
	if before.Notices == nil || len(*before.Notices) != 2 {
		t.Fatalf("notices before: %+v", before.Notices)
	}
	do(t, a.LocalHandler(), "POST", "/v1/heartbeat", "", nil)
	after, _ := poll(t, a, "after=0&timeout=0")
	if after.Version <= before.Version {
		t.Errorf("version did not grow: %d -> %d", before.Version, after.Version)
	}
	if after.Notices == nil || len(*after.Notices) != 1 || (*after.Notices)[0].Kind != agentapi.MaxWarning {
		t.Errorf("notices after: %+v", after.Notices)
	}
}
