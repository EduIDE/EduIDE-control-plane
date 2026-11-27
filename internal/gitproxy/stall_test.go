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
	}{
		{"401 without a token", "/h.test/o/r/git-upload-pack", "", anonymous(), false, false},
		{"push with a made-up token is denied", "/h.test/o/r/git-receive-pack", "t", deny, false, false},
		{"path outside the allowlist", "/h.test/api/v3/user", "t", anonymous(), false, false},
		{"over the rate limit", "/h.test/o/r/git-upload-pack", "t", anonymous(), true, false},
		{"authorised, client stalls mid-body", "/h.test/o/r/git-upload-pack", "t", anonymous(), false, false},
		{"authorised, provider answers early", "/h.test/o/r/git-upload-pack", "t", anonymous(), false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := rt
			if tc.early {
				rt = func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("early")), Request: r}, nil
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

// The expired read deadline must not hurt a well-behaved client on a kept-alive connection. The server reads the
// connection in the background while a request has no body or a finished one; a deadline that expires then makes that
// read fail, and the server cancels the context of the connection, so every later request on it starts cancelled.
func TestKeepAliveSurvivesTheReadDeadline(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
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
	defer srv.Close()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	auth := "Authorization: Bearer t\r\n" + gitapi.InstanceHeader + ": i\r\n"
	type keepAliveCase struct {
		req  string
		want int
	}
	tests := []keepAliveCase{
		{"GET /h.test/o/r/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: x\r\n" + auth + "\r\n", 200},
		{"POST /h.test/o/r/git-upload-pack HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n" + auth + "\r\nxxxxxxxxxx", 200},
		// denied before the body was read, with a small body that arrives with the headers
		{"POST /h.test/o/r/git-upload-pack HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\nxxxxxxxxxx", 401},
		{"GET /h.test/o/r/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: x\r\n" + auth + "\r\n", 200},
	}
	// The race is tight, so many requests on one connection; a repository of its own each, so the decision cache
	// does not skip the authorize call that would see the cancelled context.
	for i := range 150 { // 300 requests stay below the burst of the client limiter
		tests = append(tests,
			keepAliveCase{fmt.Sprintf("GET /h.test/o/r%d/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: x\r\n%s\r\n", i, auth), 200},
			keepAliveCase{fmt.Sprintf("POST /h.test/o/r%d/git-upload-pack HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n%s\r\nxxxxxxxxxx", i, auth), 200})
	}
	for i, tc := range tests {
		_, _ = fmt.Fprint(c, tc.req)
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("request %d on the same connection: %v", i+1, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want || resp.Close {
			t.Fatalf("request %d: status %d (want %d), close=%v", i+1, resp.StatusCode, tc.want, resp.Close)
		}
	}
	if cancelled.Load() != 0 {
		t.Errorf("%d requests started with a cancelled context", cancelled.Load())
	}
}
