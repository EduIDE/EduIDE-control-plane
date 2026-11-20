package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func activeSessions(t *testing.T, k client.Client) int {
	t.Helper()
	var l v1alpha1.IDESessionList
	if err := k.List(context.Background(), &l); err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range l.Items {
		if isActive(&l.Items[i]) {
			n++
		}
	}
	return n
}

// launchParallel sends n launches of alice with n different launch keys at once, launch i through handlers[i%len].
func launchParallel(handlers []http.Handler, n int) []int {
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			body := fmt.Sprintf(`{"template":"java","data":{"GIT_URI":"https://git.example.org/r%d.git"}}`, i)
			req := httptest.NewRequest("POST", basePath+"/sessions", bytes.NewReader([]byte(body)))
			req.Header.Set("Authorization", "Bearer alice")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handlers[i%len(handlers)].ServeHTTP(rec, req)
			codes[i] = rec.Code
		})
	}
	wg.Wait()
	return codes
}

func count(codes []int, want int) (n int) {
	for _, c := range codes {
		if c == want {
			n++
		}
	}
	return n
}

// Ten launches at once with ten different launch keys: the limit is not a check-then-create race.
func TestSessionLimitHoldsForParallelLaunches(t *testing.T) {
	e := newEnv(t)
	codes := launchParallel([]http.Handler{e.handler}, 10)
	limit := e.srv.cfg.SessionsPerUser
	if got := activeSessions(t, e.k); got != limit || count(codes, 201) != limit || count(codes, 409) != 10-limit {
		t.Fatalf("%d sessions with limit %d, status codes %v", got, limit, codes)
	}
}

// Two replicas do not share the lock. All launches pass the count before any of them creates (the gate makes
// that interleaving certain); the check after the create still keeps the user at the limit.
func TestSessionLimitHoldsAcrossReplicas(t *testing.T) {
	const n = 10
	k := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(javaTemplate()).Build()
	var arrived sync.WaitGroup
	arrived.Add(n)
	gate := make(chan struct{})
	go func() { arrived.Wait(); close(gate) }()
	w := interceptor.NewClient(k, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, o ...client.ListOption) error {
		if _, ok := l.(*v1alpha1.IDESessionList); ok {
			select {
			case <-gate:
			default:
				arrived.Done()
				<-gate
			}
		}
		return c.List(ctx, l, o...)
	}})
	handlers := make([]http.Handler, n)
	for i := range handlers {
		handlers[i] = NewServer(testConfig(), k, w, NewHub(), []byte("salt")).Handler(verifier)
	}
	codes := launchParallel(handlers, n)
	limit := testConfig().SessionsPerUser
	if got := activeSessions(t, k); got > limit || count(codes, 201) != got || count(codes, 201)+count(codes, 409) != n {
		t.Fatalf("%d sessions with limit %d, status codes %v", got, limit, codes)
	}
}

func TestWorkspaceLimit(t *testing.T) {
	e := newEnv(t)
	e.srv.cfg.Workspaces.PerUser = 2
	ws := func(user string, i int) *httptest.ResponseRecorder {
		return e.do("POST", "/workspaces", user, fmt.Sprintf(`{"template":"java","display":{"source":"https://git.example.org/r%d.git"}}`, i))
	}
	var first Workspace
	for i := range 2 {
		rec := ws("alice", i)
		if rec.Code != 201 {
			t.Fatalf("workspace %d: %d %s", i, rec.Code, rec.Body)
		}
		if i == 0 {
			first = decode[Workspace](t, rec)
		}
	}
	if p := problemCode(t, e, "POST", "/workspaces", "alice", `{"template":"java","display":{"source":"https://git.example.org/r2.git"}}`, 409); p.Code != CodeWorkspaceLimit {
		t.Fatalf("code %s", p.Code)
	}
	// A launch that would create a workspace hits the same limit and leaves no session behind.
	launch := map[string]any{"template": "java", "persistence": "volume", "data": map[string]string{"GIT_URI": "https://git.example.org/r3.git"}}
	if p := problemCode(t, e, "POST", "/sessions", "alice", launch, 409); p.Code != CodeWorkspaceLimit {
		t.Fatalf("code %s", p.Code)
	}
	if n := activeSessions(t, e.k); n != 0 {
		t.Fatalf("%d sessions", n)
	}
	// An existing workspace is still returned, other users are not affected, and a deleted one frees its place.
	if rec := ws("alice", 0); rec.Code != 201 || decode[Workspace](t, rec).Name != first.Name {
		t.Fatalf("existing workspace: %d %s", rec.Code, rec.Body)
	}
	if rec := ws("bob", 2); rec.Code != 201 {
		t.Fatalf("bob: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("DELETE", "/workspaces/"+first.Name, "alice", nil); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := ws("alice", 2); rec.Code != 201 {
		t.Fatalf("after delete: %d %s", rec.Code, rec.Body)
	}
}

// A replica whose cache has not seen the workspace yet returns it, also when the user is at the limit.
func TestWorkspaceLimitDoesNotCountTheRequestedWorkspace(t *testing.T) {
	k := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(javaTemplate()).Build()
	stale := interceptor.NewClient(k, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if _, ok := o.(*v1alpha1.IDEWorkspace); ok {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "ideworkspaces"}, key.Name)
		}
		return c.Get(ctx, key, o, opts...)
	}})
	cfg := testConfig()
	cfg.Workspaces.PerUser = 1
	srv := NewServer(cfg, stale, k, NewHub(), []byte("salt"))
	e := &env{t: t, srv: srv, handler: srv.Handler(verifier), k: k}
	var names []string
	for range 2 {
		rec := e.do("POST", "/workspaces", "alice", `{"template":"java"}`)
		if rec.Code != 201 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		names = append(names, decode[Workspace](t, rec).Name)
	}
	if names[0] != names[1] {
		t.Fatalf("names %v", names)
	}
}

// The wait in DeleteWorkspace polls the API server: one delete per user at a time, the next one gets 429.
func TestDeleteWorkspaceOneAtATimePerUser(t *testing.T) {
	s := session("s-a", "alice", "java", "Ready")
	s.Spec.WorkspaceRef = "w-a"
	s.Finalizers = []string{"test/hold"} // the session never goes away, so the first delete waits
	e := newEnv(t, s, workspace("w-a", "alice", "java"), workspace("w-b", "bob", "java"))
	e.srv.stopWait, e.srv.stopPoll = 500*time.Millisecond, 10*time.Millisecond
	done := make(chan int)
	go func() { done <- e.do("DELETE", "/workspaces/w-a", "alice", nil).Code }()
	time.Sleep(100 * time.Millisecond)
	problemCode(t, e, "DELETE", "/workspaces/w-a", "alice", nil, 429)
	if rec := e.do("DELETE", "/workspaces/w-b", "bob", nil); rec.Code != 204 {
		t.Fatalf("bob is not held up by alice: %d %s", rec.Code, rec.Body)
	}
	if code := <-done; code != 500 {
		t.Fatalf("first delete: %d", code)
	}
	// Once it is over, the user can delete again.
	problemCode(t, e, "DELETE", "/workspaces/w-a", "alice", nil, 500)
}

func TestDeleteWorkspaceSharesTheLaunchBudget(t *testing.T) {
	e := newEnv(t, workspace("w-a", "alice", "java"), workspace("w-b", "alice", "java"))
	e.srv.cfg.RateLimit.LaunchesPerMinute = 1
	problemCode(t, e, "DELETE", "/workspaces/w-missing", "alice", nil, 404) // unknown names cost nothing
	if rec := e.do("DELETE", "/workspaces/w-a", "alice", nil); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	problemCode(t, e, "DELETE", "/workspaces/w-b", "alice", nil, 429)
}
