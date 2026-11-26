package gitproxy

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// The upstream path always carries ".git": GitLab answers a repository without it with a 301 (info/refs) or a 422 (POST).
func TestUpstreamPathIsTheRepositoryWithGitPlusFixedSuffix(t *testing.T) {
	var paths []string
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", anonymous(), rt, slog.New(slog.DiscardHandler))
	h.ServeHTTP(httptest.NewRecorder(), gitReq("GET", "/h.test/o/r.git/info/refs?service=git-upload-pack&x=y", nil))
	h.ServeHTTP(httptest.NewRecorder(), gitReq("GET", "/h.test/o/r/info/refs?service=git-upload-pack", nil))
	h.ServeHTTP(httptest.NewRecorder(), gitReq("POST", "/h.test/g/s/r/git-receive-pack", strings.NewReader("x")))
	h.ServeHTTP(httptest.NewRecorder(), gitReq("POST", "/h.test/g/s/r.git/git-upload-pack", strings.NewReader("x")))
	h.ServeHTTP(httptest.NewRecorder(), gitReq("GET", "/h.test/o/r/.git/info/refs?service=git-upload-pack", nil))
	want := []string{
		"/o/r.git/info/refs?service=git-upload-pack", "/o/r.git/info/refs?service=git-upload-pack",
		"/g/s/r.git/git-receive-pack?", "/g/s/r.git/git-upload-pack?",
	}
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
// processing is the limit for the wait after the request body; zero keeps the default.
func idleRig(t *testing.T, idle, processing time.Duration, upstream http.HandlerFunc) *httptest.Server {
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
	if processing > 0 {
		h.processing = processing
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// postSlowly sends a body of n bytes, one byte per step. With stallAt >= 0 the upload pauses for stall there.
func postSlowly(url string, n int, step time.Duration, stallAt int, stall time.Duration) (int, []byte, error) {
	pr, pw := io.Pipe()
	go func() {
		for i := range n {
			_, _ = pw.Write([]byte("x"))
			time.Sleep(step)
			if i == stallAt {
				time.Sleep(stall)
			}
		}
		_ = pw.Close()
	}()
	req, _ := http.NewRequest("POST", url+"/h.test/o/r/git-upload-pack", pr)
	req.Header.Set(gitapi.InstanceHeader, "i")
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	return res.StatusCode, b, err
}

// The ratios are generous on purpose: a step is a twenty-fourth of the idle time, so a slow CI machine does not
// turn a moving transfer into an idle one.
func TestIdleDeadlineCutsStalledTransfersOnly(t *testing.T) {
	const idle = 600 * time.Millisecond
	const step = 25 * time.Millisecond
	const steps = 30 // 750 ms in total, longer than the idle time
	srv := idleRig(t, idle, 0, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		for i := range steps {
			_, _ = w.Write([]byte("data"))
			w.(http.Flusher).Flush()
			time.Sleep(step)
			if r.Header.Get("User-Agent") == "stall" && i == 1 {
				time.Sleep(3 * idle)
			}
		}
	})
	download := func(stall bool) (int, []byte, error) {
		req, _ := http.NewRequest("POST", srv.URL+"/h.test/o/r/git-upload-pack", strings.NewReader("x"))
		req.Header.Set(gitapi.InstanceHeader, "i")
		req.Header.Set("Authorization", "Bearer t")
		if stall {
			req.Header.Set("User-Agent", "stall")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer func() { _ = res.Body.Close() }()
		b, err := io.ReadAll(res.Body)
		return res.StatusCode, b, err
	}
	start := time.Now()
	if code, b, err := download(false); err != nil || code != 200 || len(b) != 4*steps {
		t.Fatalf("a slow but steady download fails: %d, %d bytes, %v", code, len(b), err)
	}
	if time.Since(start) < idle {
		t.Fatal("the test transfer is not longer than the idle deadline")
	}
	if _, b, err := download(true); err == nil && len(b) == 4*steps {
		t.Error("a stalled download is not cut")
	}
	if code, b, err := postSlowly(srv.URL, steps, step, -1, 0); err != nil || code != 200 || len(b) != 4*steps {
		t.Fatalf("a slow but steady upload fails: %d, %d bytes, %v", code, len(b), err)
	}
	if _, b, err := postSlowly(srv.URL, steps, step, 1, 3*idle); err == nil && len(b) == 4*steps {
		t.Error("a stalled upload is not cut")
	}
}

// After the request body the provider works without a byte on the wire (it unpacks a push). That is not a stall.
func TestSilentProviderAfterTheUploadIsNotCut(t *testing.T) {
	const idle = 200 * time.Millisecond
	srv := idleRig(t, idle, time.Minute, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(5 * idle)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		_, _ = w.Write([]byte("done"))
	})
	code, b, err := postSlowly(srv.URL, 3, 10*time.Millisecond, -1, 0)
	if err != nil || code != 200 || string(b) != "done" {
		t.Fatalf("the silent phase after the upload is cut: %d %q %v", code, b, err)
	}
}

// A provider that never answers is freed after the processing limit, and the client gets the message as text.
func TestProviderThatNeverAnswersIsCutAfterTheProcessingLimit(t *testing.T) {
	const processing = 300 * time.Millisecond
	srv := idleRig(t, time.Minute, processing, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	start := time.Now()
	code, b, err := postSlowly(srv.URL, 3, 10*time.Millisecond, -1, 0)
	if err != nil || code != 200 || !strings.Contains(string(b), "The transfer stalled") {
		t.Fatalf("got %d %q %v", code, b, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the limit did not cut")
	}
}

// deadlineCounter counts the calls to the connection deadlines, like the connection of a server does.
type deadlineCounter struct {
	*httptest.ResponseRecorder
	calls atomic.Int64
}

func (d *deadlineCounter) SetReadDeadline(time.Time) error  { d.calls.Add(1); return nil }
func (d *deadlineCounter) SetWriteDeadline(time.Time) error { d.calls.Add(1); return nil }

// The provider answers before the upload ends. A read of the request body after the handler returned must not
// move the deadlines of the finished request or re-arm its timer.
func TestLateBodyReadAfterTheHandlerReturnedArmsNothing(t *testing.T) {
	var late io.ReadCloser
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		late = r.Body // the transport still holds the body when it answers
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("early")), Request: r}, nil
	})
	h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", anonymous(), rt, slog.New(slog.DiscardHandler))
	h.idle = 50 * time.Millisecond
	w := &deadlineCounter{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(w, gitReq("POST", "/h.test/o/r/git-upload-pack", strings.NewReader(strings.Repeat("x", 1000))))
	if w.Body.String() != "early" || late == nil {
		t.Fatalf("setup: %q", w.Body)
	}
	calls := w.calls.Load()
	if _, err := io.Copy(io.Discard, late); err != nil { // the late read: progress and the end of the body
		t.Fatal(err)
	}
	time.Sleep(3 * h.idle) // a re-armed timer would fire in this time
	if w.calls.Load() != calls {
		t.Errorf("%d deadline calls after the handler returned", w.calls.Load()-calls)
	}
}
