package main

import (
	"io"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitpolicy"
)

// The internal authorize listener has a ReadTimeout, so a client that sends headers and stalls its body (with no
// token, or with a body that never ends) cannot hold a connection or a handler beyond it. The test shortens the
// timeout to keep itself fast and checks the production value separately.
func TestInternalServerReleasesStalledBodies(t *testing.T) {
	srv := internalServer(&gitpolicy.Handler{})
	if srv.ReadTimeout <= 0 || srv.ReadTimeout > 30*time.Second {
		t.Fatalf("ReadTimeout %v: a stalled body would hold the connection", srv.ReadTimeout)
	}
	srv.ReadTimeout = 300 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	base := runtime.NumGoroutine()
	for name, auth := range map[string]string{"no token": "", "token, body never ends": "Authorization: Bearer x\r\n"} {
		t.Run(name, func(t *testing.T) {
			var conns []net.Conn
			for range 20 {
				c, err := net.Dial("tcp", ln.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				_, _ = io.WriteString(c, "POST "+gitapi.AuthorizePath+" HTTP/1.1\r\nHost: x\r\nContent-Length: 100000\r\n"+auth+"\r\n{\"instance\":")
				conns = append(conns, c)
			}
			for _, c := range conns {
				_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
				if _, err := io.Copy(io.Discard, c); err != nil {
					t.Fatalf("the connection stays open: %v", err)
				}
			}
		})
	}
	for end := time.Now().Add(10 * time.Second); runtime.NumGoroutine() > base+3; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%d goroutines, baseline %d", runtime.NumGoroutine(), base)
		}
	}
}
