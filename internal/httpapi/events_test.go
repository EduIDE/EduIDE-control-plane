package httpapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

type sseReader struct{ sc *bufio.Scanner }

// next returns the next line, or false when the stream ended (the request context times out after 5 s).
func (r sseReader) next(t *testing.T) (string, bool) {
	t.Helper()
	if !r.sc.Scan() {
		return "", false
	}
	return r.sc.Text(), true
}

func openStream(t *testing.T, e *env, path, token string) (int, string, sseReader) {
	t.Helper()
	ts := httptest.NewServer(e.handler)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+basePath+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp.StatusCode, resp.Header.Get("Content-Type"), sseReader{bufio.NewScanner(resp.Body)}
}

func TestWatchSession(t *testing.T) {
	e := newEnv(t, session("s-a", "alice", "java", "Pending"))
	e.srv.ping = 50 * time.Millisecond
	status, ctype, r := openStream(t, e, "/sessions/s-a/events", "alice")
	if status != 200 || ctype != "text/event-stream" {
		t.Fatalf("%d %s", status, ctype)
	}
	line := func() string {
		l, ok := r.next(t)
		if !ok {
			t.Fatal("stream ended early")
		}
		return l
	}
	if l := line(); l != "event: session" {
		t.Fatalf("first line %q", l)
	}
	if l := line(); !strings.Contains(l, `"phase":"Pending"`) {
		t.Fatalf("initial state %q", l)
	}
	line() // blank line ends the event
	if l := line(); l != ": ping" {
		t.Fatalf("expected ping, got %q", l)
	}
	line()

	// A change pushed through the hub reaches the stream without polling.
	var cur v1alpha1.IDESession
	_ = e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: "s-a"}, &cur)
	cur.Status.Phase, cur.Status.URL = v1alpha1.SessionReady, "https://ide.example.org/abc/"
	if err := e.k.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
	e.srv.hub.onEvent(&cur)
	for {
		l := line()
		if strings.HasPrefix(l, "data: ") {
			if !strings.Contains(l, `"phase":"Ready"`) || !strings.Contains(l, "ide.example.org") {
				t.Fatalf("update %q", l)
			}
			break
		}
	}
	// The stream ends after Ready.
	for {
		if _, ok := r.next(t); !ok {
			return
		}
	}
}

func TestWatchSessionOfOtherUser(t *testing.T) {
	e := newEnv(t, session("s-a", "alice", "java", "Pending"))
	status, _, _ := openStream(t, e, "/sessions/s-a/events", "bob")
	if status != 404 {
		t.Fatalf("%d", status)
	}
}

func TestWatchOverview(t *testing.T) {
	e := newEnv(t, workspace("w-1", "alice", "java"))
	_, _, r := openStream(t, e, "/me/overview/events", "alice")
	if l, _ := r.next(t); l != "event: overview" {
		t.Fatalf("first line %q", l)
	}
	if l, _ := r.next(t); !strings.Contains(l, `"kind":"workspace"`) {
		t.Fatalf("initial %q", l)
	}
	r.next(t)

	if err := e.k.Create(context.Background(), session("s-new", "alice", "java", "Pending")); err != nil {
		t.Fatal(err)
	}
	e.srv.hub.onEvent(session("s-new", "alice", "java", "Pending"))
	for {
		l, ok := r.next(t)
		if !ok {
			t.Fatal("stream ended")
		}
		if strings.HasPrefix(l, "data: ") {
			if !strings.Contains(l, `"kind":"session"`) {
				t.Fatalf("update %q", l)
			}
			return
		}
	}
}

func TestHubNotifiesOnlyTheOwner(t *testing.T) {
	h := NewHub()
	a, cancelA := h.subscribe(v1alpha1.OwnerHash("alice"))
	b, cancelB := h.subscribe(v1alpha1.OwnerHash("bob"))
	h.onEvent(session("s", "alice", "java", ""))
	h.onEvent(session("s", "alice", "java", "")) // coalesces
	select {
	case <-a:
	default:
		t.Fatal("alice not notified")
	}
	select {
	case <-b:
		t.Fatal("bob notified")
	default:
	}
	cancelA()
	cancelB()
	if len(h.subs) != 0 {
		t.Fatal("subscriptions leak")
	}
}
