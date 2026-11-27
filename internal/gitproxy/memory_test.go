package gitproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

const stalledProviderEnv = "GITPROXY_STALLED_PROVIDER"

// TestStalledProviderProcess is not a test: the memory test starts the test binary again with the variable set, so the
// provider runs in its own process and its buffers do not count as memory of the proxy.
func TestStalledProviderProcess(t *testing.T) {
	mode := os.Getenv(stalledProviderEnv)
	if mode == "" {
		t.Skip("helper of TestMemoryOfStalledPushes")
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never reads the body, never answers
	}))
	srv.EnableHTTP2 = mode == "h2"
	srv.StartTLS()
	fmt.Println(srv.Listener.Addr().String())
	_, _ = io.Copy(io.Discard, os.Stdin) // lives until the parent closes the pipe
}

// TestMemoryOfStalledPushes measures what the chart limit of the git-proxy has to hold: heap plus stack of the
// process per authorised push that a stalled provider holds, over HTTP/1.1 and HTTP/2 to the provider (GitHub and
// GitLab speak HTTP/2). The git-proxy itself uses HTTP/1.1 (Transport); the h2 run shows what that saves. The figures go to the log; docs/operations/git-proxy.md quotes them.
func TestMemoryOfStalledPushes(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a second process")
	}
	for _, mode := range []string{"h1", "h2"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestStalledProviderProcess$")
			cmd.Env = append(os.Environ(), stalledProviderEnv+"="+mode)
			in, _ := cmd.StdinPipe()
			out, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = in.Close(); _ = cmd.Wait() }()
			addr, err := bufio.NewReader(out).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			addr = strings.TrimSpace(addr)

			tr := Transport(nil).(*http.Transport)
			if tr.Protocols.HTTP2() {
				t.Fatal("the transport of the git-proxy must speak HTTP/1.1 to the provider")
			}
			if mode == "h2" {
				tr.Protocols.SetHTTP2(true) // what the default transport would do with GitHub and GitLab
			}
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- a measurement against a local test server
			tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}
			h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", anonymous(), tr, slog.New(slog.DiscardHandler))
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)

			const instances = 20
			n := instances * maxInFlight
			settle := func() (heap, stack uint64) {
				runtime.GC()
				runtime.GC()
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				return m.HeapAlloc, m.StackInuse
			}
			// One request first, so the one-time cost of the transport is not counted per request.
			warm := func(i int) net.Conn {
				c, err := net.Dial("tcp", srv.Listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				_, _ = fmt.Fprintf(c, "POST /h.test/o/r/git-receive-pack HTTP/1.1\r\nHost: x\r\nContent-Length: 1073741824\r\nAuthorization: Bearer t\r\n%s: i%d\r\n\r\n%s",
					gitapi.InstanceHeader, i, strings.Repeat("p", 64<<10))
				return c
			}
			warm(instances + 1)
			waitRunning(t, h, 1)
			heap0, stack0 := settle()
			// In batches: 321 connections at once overflow the listen queue of some systems (macOS).
			for i := range n {
				warm(i / maxInFlight)
				if (i+1)%20 == 0 {
					waitRunning(t, h, i+2)
				}
			}
			waitRunning(t, h, n+1)
			time.Sleep(time.Second) // let the buffers of the streams fill
			heap1, stack1 := settle()
			// Signed: a stack that shrank (or a GC that freed more than the requests added) must not wrap around.
			heapPer, stackPer := max(float64(int64(heap1)-int64(heap0)), 0)/float64(n), max(float64(int64(stack1)-int64(stack0)), 0)/float64(n)
			t.Logf("%s upstream: %d stalled pushes, heap %.0f KiB + stack %.0f KiB = %.0f KiB per request", mode, n, heapPer/1024, stackPer/1024, (heapPer+stackPer)/1024)
			// Far above the measured figures, so noise passes; a change that makes a request cost megabytes does not.
			if heapPer+stackPer > 1<<20 {
				t.Errorf("%.0f KiB per stalled push", (heapPer+stackPer)/1024)
			}
		})
	}
}

func waitRunning(t *testing.T, h *Handler, want int) {
	t.Helper()
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		h.running.mu.Lock()
		got := 0
		for _, c := range h.running.n {
			got += c
		}
		h.running.mu.Unlock()
		if got >= want {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%d of %d requests in flight", got, want)
		}
	}
}
