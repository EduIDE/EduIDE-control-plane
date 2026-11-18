package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRootAndHeaders(t *testing.T) {
	srv := httptest.NewServer((&ide{}).handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 200)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "fake-ide") {
		t.Fatalf("root does not contain fake-ide: %q", buf[:n])
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/headers", nil)
	req.Header.Set("X-Probe", "secret-value")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r2.Body.Close() }()
	var out struct{ Headers []string }
	if err := json.NewDecoder(r2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range out.Headers {
		found = found || h == "X-Probe"
	}
	if !found {
		t.Fatalf("header name missing: %v", out.Headers)
	}
}

func TestWebSocketEcho(t *testing.T) {
	srv := httptest.NewServer((&ide{}).handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil) //nolint:bodyclose // the library closes the body on upgrade
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := c.Read(ctx)
	if err != nil || string(msg) != "ping" {
		t.Fatalf("echo = %q, %v", msg, err)
	}
}

// The agent stub serves one claimed state, then holds the poll open.
func TestPollsAgentAndHidesValues(t *testing.T) {
	var hits atomic.Int32
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/heartbeat" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if hits.Add(1) > 1 {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{"version":1,"claimed":true,"data":{"B":"two","A":"one"},"notices":[{"kind":"idle-warning"}]}`))
	}))
	defer agent.Close()
	i := &ide{agent: agent.URL}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go i.poll(ctx)

	srv := httptest.NewServer(i.handler())
	defer srv.Close()
	var got map[string]any
	for range 50 {
		resp, err := http.Get(srv.URL + "/data-state")
		if err != nil {
			t.Fatal(err)
		}
		got = nil
		_ = json.NewDecoder(resp.Body).Decode(&got)
		_ = resp.Body.Close()
		if got["claimed"] == true {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	sum := sha256.Sum256([]byte("onetwo"))
	if got["claimed"] != true || got["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("state = %v", got)
	}
	keys := got["keys"].([]any)
	if len(keys) != 2 || keys[0] != "A" || keys[1] != "B" {
		t.Fatalf("keys = %v", keys)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "one") && strings.Contains(string(raw), "two") {
		t.Fatalf("values leaked: %s", raw)
	}

	resp, err := http.Post(srv.URL+"/heartbeat", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat status %d", resp.StatusCode)
	}
}
