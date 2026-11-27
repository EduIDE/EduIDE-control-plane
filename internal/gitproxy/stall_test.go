package gitproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

// stallConn opens a connection, sends complete headers with a Content-Length and a few body bytes, and stalls.
func stallConn(t *testing.T, addr, path, auth string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	hdr := "POST " + path + " HTTP/1.1\r\nHost: x\r\nContent-Length: 100000\r\n"
	if auth != "" {
		hdr += "Authorization: Bearer t\r\n" + gitapi.InstanceHeader + ": i\r\n"
	}
	if _, err := io.WriteString(c, hdr+"\r\nxxxxxxxxxx"); err != nil {
		t.Fatal(err)
	}
	return c
}

// waitGoroutines polls until the goroutine count is back at the baseline (plus a little noise).
func waitGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > base+3 {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines, baseline %d: stalled requests still hold a handler", runtime.NumGoroutine(), base)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A client that sends headers and stalls its body must not hold a connection or a goroutine after the handler
// answered. net/http discards the unread body after the handler returns with no deadline of its own; the handler
// sets an expired read deadline to end that. Needs a real server: a ResponseRecorder never discards anything.
func TestStalledBodyHoldsNothingOnAnyPath(t *testing.T) {
	deny := azFunc(func(context.Context, string, gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
		return gitapi.AuthorizeResponse{Reason: gitapi.ReasonUnauthorized, Message: "no"}, nil
	})
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		_, err := io.ReadAll(r.Body) // blocks while the client stalls
		return nil, err
	})
	tests := []struct {
		name, path, auth string
		az               Authorizer
		limited          bool
		early            bool // the provider answers before it reads the body
		real             bool // through a real http.Transport to a real provider that never reads the body
	}{
		{"401 without a token", "/h.test/o/r/git-upload-pack", "", anonymous(), false, false, false},
		{"push with a made-up token is denied", "/h.test/o/r/git-receive-pack", "t", deny, false, false, false},
		{"path outside the allowlist", "/h.test/api/v3/user", "t", anonymous(), false, false, false},
		{"over the rate limit", "/h.test/o/r/git-upload-pack", "t", anonymous(), true, false, false},
		{"authorised, client stalls mid-body", "/h.test/o/r/git-upload-pack", "t", anonymous(), false, false, false},
		{"authorised, provider answers early", "/h.test/o/r/git-upload-pack", "t", anonymous(), false, true, false},
		{"authorised, real transport, provider answers early", "/h.test/o/r/git-upload-pack", "t", anonymous(), false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := rt
			if tc.early {
				rt = func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("early")), Request: r}, nil
				}
			}
			if tc.real {
				prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "early") }))
				t.Cleanup(prov.Close)
				tr := &http.Transport{}
				t.Cleanup(tr.CloseIdleConnections)
				rt = func(r *http.Request) (*http.Response, error) {
					r.URL.Scheme, r.URL.Host = "http", prov.Listener.Addr().String()
					return tr.RoundTrip(r)
				}
			}
			h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", tc.az, rt, slog.New(slog.DiscardHandler))
			h.idle = 300 * time.Millisecond
			h.clients.now = func() time.Time { return time.Unix(1, 0) } // no refill
			if tc.limited {
				for range clientBurst {
					h.clients.allow("127.0.0.1")
				}
			}
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close) // runs after the client connections are closed
			base := runtime.NumGoroutine()
			var conns []net.Conn
			for range 20 {
				conns = append(conns, stallConn(t, srv.Listener.Addr().String(), tc.path, tc.auth))
			}
			for _, c := range conns {
				// The client sees an answer or the end of the connection, never silence.
				_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
				if _, err := io.Copy(io.Discard, c); err != nil {
					t.Fatalf("the connection stays open: %v", err)
				}
			}
			waitGoroutines(t, base)
		})
	}
}

// halfBody sends half of its bytes, waits, and sends the rest, like a slow git client.
type halfBody struct {
	data []byte
	pos  int
	wait time.Duration
}

func (b *halfBody) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	if b.pos == len(b.data)/2 {
		time.Sleep(b.wait)
	}
	end := len(b.data) / 2
	if b.pos >= end {
		end = len(b.data)
	}
	n := copy(p, b.data[b.pos:end])
	b.pos += n
	return n, nil
}

// rawProvider answers every POST at once with a 401 and Connection: close, without reading the body. A GET gets 200.
func rawProvider(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				br := bufio.NewReader(c)
				for {
					req, err := http.ReadRequest(br)
					if err != nil {
						return
					}
					if req.Method == http.MethodPost {
						_, _ = io.WriteString(c, "HTTP/1.1 401 Unauthorized\r\nContent-Length: 4\r\nConnection: close\r\n\r\nnope")
						return
					}
					_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

// A request with a body never reuses its connection, so the expired read deadline cannot poison a later request:
// net/http cancels the context of a connection whose background read fails, and a flag that guessed whether that
// read runs raced with net/http. A real client reuses its pool, and every later request must work. Each case is a
// way the provider ends a request before the body is read to its end (the reviewer's reproductions).
func TestRequestsAfterAnEarlyAnswerStillWork(t *testing.T) {
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	refused := closed.Addr().String()
	_ = closed.Close()
	early := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "early") // never reads the body
	}))
	t.Cleanup(early.Close)
	tests := []struct {
		name      string
		post, get string // the provider address per method; an error answer of a Git request is a 200 as well
	}{
		{"provider refuses the connection", refused, ""},
		{"provider answers without reading the body", early.Listener.Addr().String(), ""},
		{"client waits half way, provider rejects with Connection: close", rawProvider(t), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			get := tc.get
			if get == "" {
				get = early.Listener.Addr().String()
			}
			tr := &http.Transport{}
			rt := rtFunc(func(r *http.Request) (*http.Response, error) {
				r.URL.Scheme, r.URL.Host = "http", get
				if r.Method == http.MethodPost {
					r.URL.Host = tc.post
				}
				return tr.RoundTrip(r)
			})
			var cancelled atomic.Int64
			az := azFunc(func(ctx context.Context, _ string, _ gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
				if ctx.Err() != nil {
					cancelled.Add(1)
				}
				return gitapi.AuthorizeResponse{Allow: true, Reason: gitapi.ReasonAnonymous}, nil
			})
			h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", az, rt, slog.New(slog.DiscardHandler))
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			client := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
			t.Cleanup(client.CloseIdleConnections)
			do := func(method, path string, body io.Reader, want int) {
				t.Helper()
				req, _ := http.NewRequest(method, srv.URL+path, body)
				if body != nil {
					req.ContentLength = 20
				}
				req.Header.Set("Authorization", "Bearer t")
				req.Header.Set(gitapi.InstanceHeader, "i")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", method, path, err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != want {
					t.Fatalf("%s %s: status %d, want %d", method, path, resp.StatusCode, want)
				}
				if body != nil && !resp.Close {
					t.Fatalf("%s %s: the connection of a request with a body stays open", method, path)
				}
			}
			for i := range 100 { // 200 requests stay below the burst of the client limiter
				do("POST", fmt.Sprintf("/h.test/o/r%d/git-upload-pack", i), &halfBody{data: []byte(strings.Repeat("x", 20)), wait: 5 * time.Millisecond}, http.StatusOK)
				do("GET", fmt.Sprintf("/h.test/o/r%d/info/refs?service=git-upload-pack", i), nil, http.StatusOK)
			}
			if n := cancelled.Load(); n != 0 {
				t.Errorf("%d requests started with a cancelled context", n)
			}
		})
	}
}

// A request without a body keeps its connection.
func TestRequestWithoutBodyKeepsItsConnection(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})
	h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", anonymous(), rt, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var conns atomic.Int64
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
		conns.Add(1)
		return (&net.Dialer{}).DialContext(ctx, n, a)
	}}}
	t.Cleanup(client.CloseIdleConnections)
	for range 5 {
		req, _ := http.NewRequest("GET", srv.URL+"/h.test/o/r/info/refs?service=git-upload-pack", nil)
		req.Header.Set("Authorization", "Bearer t")
		req.Header.Set(gitapi.InstanceHeader, "i")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.Close {
			t.Fatal("a request without a body got Connection: close")
		}
	}
	if conns.Load() != 1 {
		t.Errorf("%d connections for 5 requests without a body", conns.Load())
	}
}
