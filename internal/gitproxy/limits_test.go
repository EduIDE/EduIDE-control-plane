package gitproxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

func TestClientLimiter(t *testing.T) {
	c := newClientLimiter()
	now := time.Now()
	c.now = func() time.Time { return now }
	for i := range clientBurst {
		if !c.allow("10.0.0.1") {
			t.Fatalf("request %d of a burst is refused", i)
		}
	}
	if c.allow("10.0.0.1") {
		t.Error("the burst is not bounded")
	}
	if !c.allow("10.0.0.2") {
		t.Error("one client locks out another")
	}
	now = now.Add(time.Second)
	if !c.allow("10.0.0.1") {
		t.Error("the bucket does not refill")
	}
}

func TestClientLimiterIsBounded(t *testing.T) {
	c := newClientLimiter()
	now := time.Now()
	c.now = func() time.Time { return now }
	for i := range maxClients + 500 {
		c.allow(net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)).String())
	}
	if len(c.m) > maxClients {
		t.Fatalf("%d entries", len(c.m))
	}
	// Buckets that refilled count as free, so a later client gets in.
	now = now.Add(time.Minute)
	if !c.allow("192.0.2.1") {
		t.Error("a new client is refused although all buckets are full again")
	}
}

func TestSlots(t *testing.T) {
	var s slots
	for range maxInFlight {
		if !s.acquire("a") {
			t.Fatal("below the cap")
		}
	}
	if s.acquire("a") {
		t.Error("above the cap")
	}
	if !s.acquire("b") {
		t.Error("another instance is blocked")
	}
	s.release("a")
	if !s.acquire("a") {
		t.Error("a released slot is not reusable")
	}
	for range maxInFlight {
		s.release("a")
	}
	s.release("b")
	if len(s.n) != 0 {
		t.Errorf("entries stay: %v", s.n)
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func anonymous() Authorizer {
	return azFunc(func(context.Context, string, gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
		return gitapi.AuthorizeResponse{Allow: true, Reason: gitapi.ReasonAnonymous}, nil
	})
}

func gitReq(method, path string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set(gitapi.InstanceHeader, "i")
	r.Header.Set("Authorization", "Bearer t")
	return r
}

func TestUpstreamPathIsTheNormalisedRepositoryPlusFixedSuffix(t *testing.T) {
	var paths []string
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", anonymous(), rt, slog.New(slog.DiscardHandler))
	h.ServeHTTP(httptest.NewRecorder(), gitReq("GET", "/h.test/o/r.git/info/refs?service=git-upload-pack&x=y", nil))
	h.ServeHTTP(httptest.NewRecorder(), gitReq("POST", "/h.test/g/s/r/git-receive-pack", strings.NewReader("x")))
	h.ServeHTTP(httptest.NewRecorder(), gitReq("GET", "/h.test/o/r/.git/info/refs?service=git-upload-pack", nil))
	want := []string{"/o/r/info/refs?service=git-upload-pack", "/g/s/r/git-receive-pack?"}
	if strings.Join(paths, " ") != strings.Join(want, " ") {
		t.Errorf("upstream saw %v, want %v", paths, want)
	}
}

func TestPerClientRateLimitAnswers429(t *testing.T) {
	h := newTestHandler(anonymous())
	var last int
	for range clientBurst + 5 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("last status %d", last)
	}
}

func TestErrorMessageIsCapped(t *testing.T) {
	body := string(errorBody(Target{Op: OpUploadPack, Service: "git-upload-pack"}, strings.Repeat("ä", 40000)))
	if len(body) > maxErrMessage+16 {
		t.Fatalf("body of %d bytes", len(body))
	}
	if n := len(body); body[:4] != pktHex(n) {
		t.Errorf("pkt-line length %q does not match %d", body[:4], n)
	}
}

func pktHex(n int) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[n>>12&15], hex[n>>8&15], hex[n>>4&15], hex[n&15]})
}

func TestConcurrencyCapPerInstance(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, maxInFlight)
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", anonymous(), rt, slog.New(slog.DiscardHandler))
	var wg sync.WaitGroup
	for range maxInFlight {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), gitReq("GET", "/h.test/o/r/info/refs?service=git-upload-pack", nil))
		}()
	}
	for range maxInFlight {
		<-started
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gitReq("GET", "/h.test/o/r/info/refs?service=git-upload-pack", nil))
	if !strings.Contains(rec.Body.String(), "Too many Git requests") {
		t.Errorf("no cap: %q", rec.Body)
	}
	other := gitReq("GET", "/h.test/o/r/info/refs?service=git-upload-pack", nil)
	other.Header.Set(gitapi.InstanceHeader, "other")
	go func() { <-started }()
	close(release)
	wg.Wait()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, other)
	if strings.Contains(rec.Body.String(), "Too many") {
		t.Error("the slots are not released")
	}
}

// idleRig runs the handler on a real listener in front of a fake provider.
func idleRig(t *testing.T, idle time.Duration, upstream http.HandlerFunc) *httptest.Server {
	t.Helper()
	up := httptest.NewTLSServer(upstream)
	t.Cleanup(up.Close)
	tr := &http.Transport{
		DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, n, up.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, // #nosec G402 -- test fake
	}
	h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", anonymous(), tr, slog.New(slog.DiscardHandler))
	h.idle = idle
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestIdleDeadlineCutsStalledTransfersOnly(t *testing.T) {
	const idle = 300 * time.Millisecond
	const step = 60 * time.Millisecond
	srv := idleRig(t, idle, func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		for i := range 10 { // a slow download: 600 ms in total, never idle for long
			_, _ = w.Write([]byte("data"))
			w.(http.Flusher).Flush()
			time.Sleep(step)
			if r.Header.Get("User-Agent") == "stall" && i == 1 {
				time.Sleep(3 * idle)
			}
		}
		_ = n
	})
	post := func(agent string, stallUpload bool) (int, error) {
		pr, pw := io.Pipe()
		go func() {
			for i := range 10 { // a slow upload of 600 ms
				_, _ = pw.Write([]byte("x"))
				time.Sleep(step)
				if stallUpload && i == 1 {
					time.Sleep(3 * idle)
				}
			}
			_ = pw.Close()
		}()
		req, _ := http.NewRequest("POST", srv.URL+"/h.test/o/r/git-upload-pack", pr)
		req.Header.Set("User-Agent", agent)
		req.Header.Set(gitapi.InstanceHeader, "i")
		req.Header.Set("Authorization", "Bearer t")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer func() { _ = res.Body.Close() }()
		b, err := io.ReadAll(res.Body)
		if err == nil && len(b) != 40 {
			err = fmt.Errorf("got %d bytes, want 40", len(b))
		}
		return res.StatusCode, err
	}
	start := time.Now()
	if code, err := post("slow", false); err != nil || code != 200 {
		t.Fatalf("a slow but steady transfer fails: %d %v", code, err)
	}
	if time.Since(start) < idle {
		t.Fatal("the test transfer is not longer than the idle deadline")
	}
	if _, err := post("stall", false); err == nil {
		t.Error("a stalled download is not cut")
	}
	if _, err := post("slow", true); err == nil {
		t.Error("a stalled upload is not cut")
	}
}
