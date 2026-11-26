package gitproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduIDE/EduIDE-control-plane/internal/gitapi"
)

func command(caps string) string {
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/main\x00" + caps
	return pktLine(line) + "0000PACK"
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
	for _, tc := range []struct {
		name, body string
		code       int
		want       string
	}{
		{"side-band-64k", command(" report-status side-band-64k"), 200, "\x03not here\n"},
		{"no side-band", command(" report-status"), 403, "not here\n"},
		{"probe of a large push", "0000", 200, "ERR not here\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, gitReq("POST", "/h.test/o/r/git-receive-pack", strings.NewReader(tc.body)))
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("%d %q", rec.Code, rec.Body)
			}
		})
	}
}
