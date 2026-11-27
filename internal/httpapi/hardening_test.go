package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/auth"
)

// stream opens an event stream and returns its status and a func that closes it.
func stream(t *testing.T, url, token string) (int, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url+basePath+"/me/overview/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	closeIt := func() { cancel(); _ = resp.Body.Close() }
	t.Cleanup(closeIt)
	return resp.StatusCode, closeIt
}

func TestStreamLimitPerUser(t *testing.T) {
	e := newEnv(t)
	e.srv.maxStreams = 2
	ts := httptest.NewServer(e.handler)
	t.Cleanup(ts.Close) // registered first, so it runs after the streams are closed; Close waits for open requests
	first, closeFirst := stream(t, ts.URL, "alice")
	second, _ := stream(t, ts.URL, "alice")
	third, _ := stream(t, ts.URL, "alice")
	if first != 200 || second != 200 || third != 429 {
		t.Fatalf("alice: %d %d %d, want 200 200 429", first, second, third)
	}
	if bob, _ := stream(t, ts.URL, "bob"); bob != 200 {
		t.Fatalf("bob is not held up by alice: %d", bob)
	}
	// A closed stream gives its place back.
	closeFirst()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if code, _ := stream(t, ts.URL, "alice"); code == 200 {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("no place after a stream closed: %d", code)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A client that stops reading must not pin the stream (goroutine, subscription, place) until TCP gives up.
func TestStreamEndsWhenTheClientStopsReading(t *testing.T) {
	e := newEnv(t)
	e.srv.writeWait = 200 * time.Millisecond
	big := strings.Repeat("x", 32<<20) // more than the socket buffers hold
	done := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		r = r.WithContext(auth.WithUser(r.Context(), auth.User{Owner: "alice"}))
		e.srv.stream(w, r, "alice", "big", func(context.Context) (any, bool, *apiError) { return big, false, nil })
	}))
	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	defer func() { _ = c.Close() }() // runs before ts.Close, which waits for open requests
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select { // the client never reads
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream is still open: the write has no deadline")
	}
	if n := e.srv.hub.count(); n != 0 {
		t.Fatalf("%d subscriptions left", n)
	}
	if _, ok := e.srv.openStream("alice"); !ok || len(e.srv.streams) != 1 {
		t.Fatalf("the place of the stream was not given back: %v", e.srv.streams)
	}
}

// A body that arrives slower than bodyWait ends the request instead of holding the handler open.
func TestSlowRequestBodyIsCutOff(t *testing.T) {
	for _, path := range []string{"/sessions", "/workspaces"} {
		e := newEnv(t)
		e.srv.bodyWait = 200 * time.Millisecond
		ts := httptest.NewServer(e.handler)
		c, err := net.Dial("tcp", ts.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = fmt.Fprintf(c, "POST %s%s HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer alice\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"template\":", basePath, path)
		start := time.Now()
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode != 400 || time.Since(start) > 5*time.Second {
			t.Errorf("%s: status %d after %s, want 400 shortly after the body deadline", path, resp.StatusCode, time.Since(start))
		}
		_ = resp.Body.Close()
		_ = c.Close()
		ts.Close()
	}
}

// The admin template list reads the templates twice from the cache; one can disappear in between.
func TestAdminTemplatesSurvivesATemplateDeletedBetweenReads(t *testing.T) {
	e := newAdminEnv(t, nil)
	lists := 0
	e.srv.r = interceptor.NewClient(e.k.(client.WithWatch), interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, o ...client.ListOption) error {
		if _, ok := l.(*v1alpha1.IDETemplateList); ok {
			if lists++; lists > 1 {
				return nil // the template is gone by the second read
			}
		}
		return c.List(ctx, l, o...)
	}})
	rec := e.do("GET", "/admin/templates", "root", nil)
	if rec.Code != 200 || len(decode[[]AdminTemplate](t, rec)) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// The admin API shows the timeouts the operator applies: a template that sets only idle gets a warning before it.
func TestAdminTemplatesShowResolvedTimeouts(t *testing.T) {
	e := newAdminEnv(t, nil)
	var tpl v1alpha1.IDETemplate
	if err := e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: "java"}, &tpl); err != nil {
		t.Fatal(err)
	}
	tpl.Spec.Timeouts.Idle = &metav1.Duration{Duration: 30 * time.Minute}
	if err := e.k.Update(context.Background(), &tpl); err != nil {
		t.Fatal(err)
	}
	ts := decode[[]AdminTemplate](t, e.do("GET", "/admin/templates", "root", nil))
	if len(ts) != 1 || ts[0].Timeouts.IdleSeconds != 1800 || ts[0].Timeouts.WarningSeconds != 1500 || ts[0].Timeouts.MaxSeconds != 86400 {
		t.Fatalf("%+v", ts)
	}
}

// A handler that forgets the content type must not let Go sniff user data into text/html.
func TestResponseWithoutContentTypeIsNotSniffedAsHTML(t *testing.T) {
	h := secureAndProblemFallback("max-age=1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><script>alert(1)</script>" + r.URL.Query().Get("x")))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/?x=%3Cb%3E", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("content type %q, headers %v", ct, rec.Header())
	}
	// A handler that sets one keeps it, and a 204 gets none.
	h = secureAndProblemFallback("max-age=1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("%v", rec.Header())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/", nil))
	if rec.Code != 204 || rec.Header().Get("Content-Type") != "" {
		t.Errorf("%d %v", rec.Code, rec.Header())
	}
}
