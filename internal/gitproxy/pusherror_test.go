package gitproxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/EduIDE/EduIDE-control-plane/internal/config"
	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

func command(caps string) string {
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/main\x00" + caps
	return pktLine(line) + "0000PACK"
}

// shallow is what git sends first when it pushes from a shallow clone: one pkt-line per shallow commit.
func shallow(n int) string {
	var b strings.Builder
	for range n {
		b.WriteString(pktLine("shallow " + strings.Repeat("a", 40) + "\n"))
	}
	return b.String()
}

func TestPushFormOf(t *testing.T) {
	tests := []struct {
		name, body, encoding string
		want                 pushForm
	}{
		{"side-band-64k", command(" report-status side-band-64k agent=git/2"), "", formSideband},
		{"side-band", command(" report-status side-band"), "", formSideband},
		{"no side-band", command(" report-status agent=git/2"), "", formStatus},
		{"probe of a large push", "0000", "", formERR},
		{"empty", "", "", formERR},
		{"not a pkt-line", "hello world", "", formERR},
		{"compressed", command(" side-band-64k"), "gzip", formERR},
		{"short line", "0009abc", "", formERR},
		{"shallow clone with side-band", shallow(2) + command(" report-status side-band-64k"), "", formSideband},
		{"shallow clone without side-band", shallow(1) + command(" report-status"), "", formStatus},
		{"only shallow lines, then a flush", shallow(2) + "0000", "", formERR},
		{"shallow lines, then nothing", shallow(2), "", formERR},
		{"shallow lines fill the buffer", shallow(peekSize/50+10) + command(" side-band-64k"), "", formStatus},
		{"a first command longer than the buffer", command(" side-band-64k " + strings.Repeat("x", peekSize)), "", formStatus},
		{"a command that just fits", command(strings.Repeat("x", peekSize-200) + " side-band-64k"), "", formSideband},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/h/o/r/git-receive-pack", strings.NewReader(tc.body))
			if tc.encoding != "" {
				r.Header.Set("Content-Encoding", tc.encoding)
			}
			if got := pushFormOf(r, http.NewResponseController(rec)); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
			if b, _ := io.ReadAll(r.Body); string(b) != tc.body {
				t.Errorf("the body changed: %q", b)
			}
		})
	}
}

func TestErrorAnswer(t *testing.T) {
	push := Target{Op: OpReceivePack, Service: "git-receive-pack"}
	code, _, body := errorAnswer(push, formSideband, 429, "slow  down")
	if code != 200 || string(body) != "000f\x03slow down\n0000" {
		t.Errorf("side-band: %d %q", code, body)
	}
	code, ct, body := errorAnswer(push, formStatus, 429, "slow down")
	if code != 429 || !strings.HasPrefix(ct, "text/plain") || string(body) != "slow down\n" {
		t.Errorf("status: %d %s %q", code, ct, body)
	}
	for _, tgt := range []Target{push, {Op: OpUploadPack, Service: "git-upload-pack"}, {Op: OpInfoRefs, Service: "git-receive-pack"}} {
		if code, _, body := errorAnswer(tgt, formERR, 429, "x"); code != 200 || !strings.Contains(string(body), "ERR x") {
			t.Errorf("%+v: %d %q", tgt, code, body)
		}
	}
	// Other operations keep the ERR line whatever the form says.
	if _, _, body := errorAnswer(Target{Op: OpUploadPack, Service: "git-upload-pack"}, formSideband, 0, "x"); !strings.Contains(string(body), "ERR x") {
		t.Errorf("upload-pack: %q", body)
	}
}

func TestAuthorizeRetriesARateLimitOnce(t *testing.T) {
	var calls atomic.Int64
	limited := func(until int64) *httptest.Server {
		calls.Store(0)
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) <= until {
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte(`{"allow":true,"reason":"anonymous"}`))
		}))
	}
	srv := limited(1)
	defer srv.Close()
	c := &ServiceClient{URL: srv.URL, HTTP: srv.Client(), wait: time.Millisecond}
	if r, err := c.Authorize(t.Context(), "t", gitapi.AuthorizeRequest{}); err != nil || !r.Allow || calls.Load() != 2 {
		t.Errorf("one 429: %+v %v after %d calls", r, err, calls.Load())
	}
	srv2 := limited(100)
	defer srv2.Close()
	c = &ServiceClient{URL: srv2.URL, HTTP: srv2.Client(), wait: time.Millisecond}
	if _, err := c.Authorize(t.Context(), "t", gitapi.AuthorizeRequest{}); err == nil || calls.Load() != 2 {
		t.Errorf("a persistent 429 is retried once, then an error: %v after %d calls", err, calls.Load())
	}
}

func TestDeniedPushAnswersInTheFormOfTheClient(t *testing.T) {
	deny := azFunc(func(context.Context, string, gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
		return gitapi.AuthorizeResponse{Reason: gitapi.ReasonPushRule, Message: "not here"}, nil
	})
	h := newTestHandler(deny)
	// A denial by the service comes before the peek: it does not know the client and answers with an ERR line.
	for _, body := range []string{command(" report-status side-band-64k"), command(" report-status"), "0000"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, gitReq("POST", "/h.test/o/r/git-receive-pack", strings.NewReader(body)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ERR not here\n") {
			t.Errorf("%d %q", rec.Code, rec.Body)
		}
	}
}

// A denial after the peek (the provider refuses the push) answers in the form of the client.
func TestUpstreamDenialOfAPushAnswersInTheFormOfTheClient(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no")), Request: r}, nil
	})
	h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", anonymous(), rt, slog.New(slog.DiscardHandler))
	for _, tc := range []struct {
		name, body string
		code       int
		want       string
	}{
		{"side-band-64k", command(" report-status side-band-64k"), 200, "\x03"},
		{"shallow clone", shallow(3) + command(" report-status side-band-64k"), 200, "\x03"},
		{"no side-band", command(" report-status"), 403, ""},
		{"probe of a large push", "0000", 200, "ERR "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, gitReq("POST", "/h.test/o/r/git-receive-pack", strings.NewReader(tc.body)))
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) || strings.Contains(rec.Body.String(), "bad band") {
				t.Errorf("%d %q", rec.Code, rec.Body)
			}
		})
	}
}

// blockingBody is a request body that never delivers a byte and counts the reads.
type blockingBody struct {
	reads atomic.Int64
	stop  chan struct{}
}

func (b *blockingBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	<-b.stop
	return 0, io.EOF
}
func (b *blockingBody) Close() error { return nil }

// A push with a made-up token is answered at once: no peek, no buffer, no read of the body.
func TestUnauthorisedPushNeverPeeks(t *testing.T) {
	deny := azFunc(func(context.Context, string, gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
		return gitapi.AuthorizeResponse{Reason: gitapi.ReasonUnauthorized, Message: "no"}, nil
	})
	h := newTestHandler(deny)
	body := &blockingBody{stop: make(chan struct{})}
	defer close(body.stop)
	r := gitReq("POST", "/h.test/o/r/git-receive-pack", nil)
	r.Body = body
	before := peekBuffers.Load()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		done <- rec
	}()
	select {
	case rec := <-done:
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ERR no") {
			t.Errorf("%d %q", rec.Code, rec.Body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the handler waits for the body of an unauthorised push")
	}
	if peekBuffers.Load() != before || body.reads.Load() != 0 {
		t.Errorf("peek buffers %d, body reads %d", peekBuffers.Load()-before, body.reads.Load())
	}
}

// Many stalled pushes with made-up tokens allocate no peek buffer and read no body.
func TestStalledUnauthorisedPushesAllocateNothing(t *testing.T) {
	deny := azFunc(func(context.Context, string, gitapi.AuthorizeRequest) (gitapi.AuthorizeResponse, error) {
		return gitapi.AuthorizeResponse{Reason: gitapi.ReasonUnauthorized, Message: "no"}, nil
	})
	h := newTestHandler(deny)
	h.clients = &clientLimiter{m: map[string]*rate.Limiter{}, now: time.Now}
	stop := make(chan struct{})
	defer close(stop)
	const n = 400
	var reads atomic.Int64
	run := func() {
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body := &blockingBody{stop: stop}
				r := gitReq("POST", "/h.test/o/r/git-receive-pack", nil)
				r.Body = body
				r.RemoteAddr = net.IPv4(10, 0, byte(i>>8), byte(i)).String() + ":1"
				h.ServeHTTP(httptest.NewRecorder(), r)
				reads.Add(body.reads.Load())
			}()
		}
		wg.Wait()
	}
	before := peekBuffers.Load()
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	run()
	runtime.ReadMemStats(&m1)
	if d := peekBuffers.Load() - before; d != 0 || reads.Load() != 0 {
		t.Errorf("%d peek buffers, %d body reads for %d unauthorised pushes", d, reads.Load(), n)
	}
	// A 64 KiB buffer each would be 25 MiB; the bound is generous against the noise of the runtime.
	if grown := int64(m1.TotalAlloc) - int64(m0.TotalAlloc); grown > 12<<20 {
		t.Errorf("%d bytes allocated for %d unauthorised pushes", grown, n)
	}
}

// An authorised push peeks once, with the small buffer.
func TestAuthorisedPushPeeksOnce(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
	})
	h := New([]config.GitProvider{{Name: "p", Host: "h.test", DisplayName: "Host"}}, "https://landing.test", anonymous(), rt, slog.New(slog.DiscardHandler))
	before := peekBuffers.Load()
	body := shallow(2) + command(" side-band-64k") + strings.Repeat("p", 100<<10)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, gitReq("POST", "/h.test/o/r/git-receive-pack", strings.NewReader(body)))
	if rec.Body.String() != body || peekBuffers.Load()-before != 1 {
		t.Errorf("the body changed or the peek count is off: %d bytes, %d peeks", rec.Body.Len(), peekBuffers.Load()-before)
	}
}
