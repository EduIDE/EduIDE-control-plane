package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// publicPaths are all field paths GET /status may contain (U17). Adding a field means a decision, not a test edit.
var publicPaths = []string{
	"overall", "updatedAt", "parts", "parts.signIn", "parts.starting", "parts.workspaces",
	"templates", "templates[].name", "templates[].displayName", "templates[].logoUrl", "templates[].startEstimate",
	"templates[].startEstimate.none", "templates[].startEstimate.none.seconds",
	"templates[].startEstimate.volume", "templates[].startEstimate.volume.seconds", "templates[].warm",
	// Only with configured Git providers (ADR 0023): what the landing page needs for its connect buttons.
	"gitProviders", "gitProviders[].name", "gitProviders[].type", "gitProviders[].host", "gitProviders[].displayName",
}

func jsonPaths(prefix string, v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			jsonPaths(p, c, out)
		}
	case []any:
		for _, c := range x {
			jsonPaths(prefix+"[]", c, out)
		}
	}
}

func publicGet(t *testing.T, e *env, hdr ...string) (PublicStatus, *httptest.ResponseRecorder) {
	t.Helper()
	rec := e.do("GET", "/status", "", nil, hdr...)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	return decode[PublicStatus](t, rec), rec
}

func TestPublicStatusAllowlist(t *testing.T) {
	objs := fixture()
	e := newAdminEnv(t, nil, objs...)
	// real data: starts of several users with warm and volume samples, failures, an idle ready pool
	hub := e.srv.hub
	hub.starts.add("java", startSample{at: adminNow.Add(-time.Minute), dur: 800 * time.Millisecond, warm: true})
	hub.starts.add("java", startSample{at: adminNow.Add(-2 * time.Minute), dur: 20 * time.Second, volume: true})
	failed := session("s-ffffffffff", "carol", "java", "Failed")
	failed.Status.Message, failed.Status.ErrorCode = "node ip-10-0-0-7 is full for user carol", "capacity-exhausted"
	failed.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, LastTransitionTime: metav1.NewTime(adminNow.Add(-time.Minute))}}
	hub.onSessionStart(nil, failed)
	p := adminPool("java-pool", 1, 4, 10, 3, false)
	p.Name = "java-pool-secret-name"
	if err := e.k.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	// the fixture workspace is old and has no phase; make it not pending for this test
	for _, n := range []string{"ws-w1", "ws-w2"} {
		var w v1alpha1.IDEWorkspace
		_ = e.k.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: n}, &w)
		w.Status.Phase = v1alpha1.WorkspaceReady
		_ = e.k.Status().Update(context.Background(), &w)
	}

	st, rec := publicGet(t, e)
	body := rec.Body.String()

	var generic any
	if err := json.Unmarshal(rec.Body.Bytes(), &generic); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	jsonPaths("", generic, got)
	var extra []string
	for p := range got {
		if !slices.Contains(publicPaths, p) {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Fatalf("fields outside the allowlist: %v\n%s", extra, body)
	}
	for _, must := range []string{"overall", "updatedAt", "parts.signIn", "parts.starting", "parts.workspaces", "templates[].name", "templates[].displayName", "templates[].logoUrl", "templates[].startEstimate.none.seconds", "templates[].startEstimate.volume.seconds", "templates[].warm"} {
		if !got[must] {
			t.Errorf("expected field %s missing: %s", must, body)
		}
	}
	for _, bad := range []string{"alice", "bob", "carol", "s-aaaaaaaaaa", "s-ffffffffff", "ws-w1", "ws-w2", "java-pool", "ip-10-0-0-7", "capacity-exhausted", "s.example.org", "kc.example.org", "ownerHash", "inst-secret-id", "ab12cde", secretVal} {
		if strings.Contains(body, bad) {
			t.Errorf("response contains %q: %s", bad, body)
		}
	}
	if len(st.Templates) != 1 || !st.Templates[0].Warm || st.Templates[0].StartEstimate.None == nil || st.Templates[0].StartEstimate.Volume == nil {
		t.Fatalf("%+v", st.Templates)
	}
}

func TestPublicStatusNeedsNoTokenOthersStillDo(t *testing.T) {
	e := newAdminEnv(t, nil, fixture()...)
	if rec := rawDo(e, "GET", "/status", ""); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	if rec := rawDo(e, "GET", "/sessions", ""); rec.Code != 401 {
		t.Fatalf("%d", rec.Code)
	}
	// works with the admin API off, too
	e2 := newEnv(t)
	if rec := rawDo(e2, "GET", "/status", ""); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestPublicStatusLevels(t *testing.T) {
	now := adminNow
	ok := func(at time.Duration, dur time.Duration) startOutcome {
		return startOutcome{at: now.Add(-at), dur: dur}
	}
	bad := func(at time.Duration) startOutcome { return startOutcome{at: now.Add(-at), failed: true} }
	for name, tt := range map[string]struct {
		in   []startOutcome
		want ServiceLevel
	}{
		"no data":                     {nil, ServiceLevelOk},
		"fast":                        {[]startOutcome{ok(time.Minute, time.Second), ok(time.Minute, 2*time.Second)}, ServiceLevelOk},
		"slow p95":                    {[]startOutcome{ok(time.Minute, time.Second), ok(2*time.Minute, 61*time.Second)}, ServiceLevelSlow},
		"slow start outside 15 min":   {[]startOutcome{ok(16*time.Minute, 90*time.Second), ok(time.Minute, time.Second)}, ServiceLevelOk},
		"one slow in many is not p95": {append([]startOutcome{ok(time.Minute, 90*time.Second)}, slices.Repeat([]startOutcome{ok(time.Minute, time.Second)}, 40)...), ServiceLevelOk},
		"three failures are too few":  {[]startOutcome{bad(time.Minute), bad(time.Minute), bad(time.Minute)}, ServiceLevelOk},
		"most of four failed":         {[]startOutcome{bad(time.Minute), bad(time.Minute), bad(time.Minute), ok(time.Minute, time.Second)}, ServiceLevelDown},
		"half is not more than half":  {[]startOutcome{bad(time.Minute), bad(time.Minute), ok(time.Minute, time.Second), ok(time.Minute, time.Second)}, ServiceLevelOk},
		"old failures do not count":   {[]startOutcome{bad(6 * time.Minute), bad(7 * time.Minute), bad(8 * time.Minute), bad(9 * time.Minute), bad(10 * time.Minute)}, ServiceLevelOk},
		"down wins over slow":         {[]startOutcome{bad(time.Minute), bad(time.Minute), bad(time.Minute), ok(time.Minute, 90*time.Second)}, ServiceLevelDown},
	} {
		st := newStartStats()
		for _, o := range tt.in {
			st.addOutcome(o)
		}
		if got := st.startingLevel(now); got != tt.want {
			t.Errorf("%s: %s, want %s", name, got, tt.want)
		}
	}
}

func TestPublicStatusOverallIsTheWorstPart(t *testing.T) {
	e := newAdminEnv(t, nil)
	if st, _ := publicGet(t, e); st.Overall != ServiceLevelOk || st.Parts.SignIn != ServiceLevelOk {
		t.Fatalf("%+v", st)
	}
	e.srv.hub.starts.addOutcome(startOutcome{at: adminNow.Add(-time.Minute), dur: 2 * time.Minute})
	e.srv.signIn.Store(false)
	e.srv.pubBody = nil
	st, _ := publicGet(t, e)
	if st.Overall != ServiceLevelDown || st.Parts.SignIn != ServiceLevelDown || st.Parts.Starting != ServiceLevelSlow {
		t.Fatalf("%+v", st)
	}
	e.srv.signIn.Store(true)
	e.srv.pubBody = nil
	if st, _ := publicGet(t, e); st.Overall != ServiceLevelSlow {
		t.Fatalf("%+v", st)
	}
}

func TestPublicStatusWorkspacePending(t *testing.T) {
	young := workspace("ws-young", "alice", "java")
	young.CreationTimestamp = metav1.NewTime(adminNow.Add(-time.Minute))
	e := newAdminEnv(t, nil, young)
	if st, _ := publicGet(t, e); st.Parts.Workspaces != ServiceLevelOk {
		t.Fatalf("a workspace pending for one minute: %+v", st.Parts)
	}
	old := workspace("ws-old", "alice", "java")
	old.Status.Phase = v1alpha1.WorkspacePending
	old.CreationTimestamp = metav1.NewTime(adminNow.Add(-3 * time.Minute))
	e = newAdminEnv(t, nil, old)
	st, _ := publicGet(t, e)
	if st.Parts.Workspaces != ServiceLevelDown || st.Overall != ServiceLevelDown {
		t.Fatalf("%+v", st)
	}
	old.Status.Phase = v1alpha1.WorkspaceReady
	e = newAdminEnv(t, nil, old)
	if st, _ := publicGet(t, e); st.Parts.Workspaces != ServiceLevelOk {
		t.Fatalf("%+v", st.Parts)
	}
}

func TestPublicStatusCache(t *testing.T) {
	e := newAdminEnv(t, nil)
	now := adminNow
	e.srv.now = func() time.Time { return now }
	first, rec := publicGet(t, e)
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=15" {
		t.Fatalf("Cache-Control %q", cc)
	}
	now = now.Add(10 * time.Second)
	if again, _ := publicGet(t, e); !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatal("not served from the cache within 15 s")
	}
	now = now.Add(6 * time.Second)
	if later, _ := publicGet(t, e); !later.UpdatedAt.After(first.UpdatedAt) {
		t.Fatal("not refreshed after 15 s")
	}
}

type countingReader struct {
	client.Reader
	workspaceLists atomic.Int32
}

func (c *countingReader) List(ctx context.Context, l client.ObjectList, opts ...client.ListOption) error {
	if _, ok := l.(*v1alpha1.IDEWorkspaceList); ok {
		c.workspaceLists.Add(1)
		time.Sleep(20 * time.Millisecond) // a slow build, so the requests overlap
	}
	return c.Reader.List(ctx, l, opts...)
}

// Many requests at the moment the cache expires build the answer once.
func TestPublicStatusNoStampede(t *testing.T) {
	e := newAdminEnv(t, nil)
	r := &countingReader{Reader: e.srv.r}
	e.srv.r = r
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := rawDoHdr(e, fmt.Sprintf("192.0.2.%d:1", i), ""); rec.Code != 200 {
				t.Errorf("%d", rec.Code)
			}
		}()
	}
	wg.Wait()
	if n := r.workspaceLists.Load(); n != 1 {
		t.Fatalf("built %d times", n)
	}
}

func TestPublicStatusRateLimitPerIP(t *testing.T) {
	e := newAdminEnv(t, nil)
	for i := 0; i < publicPerMinute; i++ {
		if rec := rawDoHdr(e, "10.0.0.1:1", "198.51.100.7"); rec.Code != 200 {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	if rec := rawDoHdr(e, "10.0.0.1:1", "198.51.100.7"); rec.Code != 429 {
		t.Fatalf("over the limit: %d", rec.Code)
	}
	// the client IP is the last entry: a forged first entry does not get a fresh budget
	if rec := rawDoHdr(e, "10.0.0.1:1", "203.0.113.9, 198.51.100.7"); rec.Code != 429 {
		t.Fatalf("forged first entry: %d", rec.Code)
	}
	// another client is not affected, and without the header the peer address counts
	if rec := rawDoHdr(e, "10.0.0.1:1", "198.51.100.8"); rec.Code != 200 {
		t.Fatalf("other client: %d", rec.Code)
	}
	if rec := rawDoHdr(e, "192.0.2.5:4000", ""); rec.Code != 200 {
		t.Fatalf("peer address: %d", rec.Code)
	}
	// the error is the standard problem
	rec := rawDoHdr(e, "10.0.0.1:1", "198.51.100.7")
	if rec.Header().Get("Content-Type") != "application/problem+json" || rec.Header().Get("Cache-Control") != "" {
		t.Fatalf("%v", rec.Header())
	}
}

func rawDoHdr(e *env, remote, xff string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", basePath+"/status", nil)
	req.RemoteAddr = remote
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func TestDiscoveryReachable(t *testing.T) {
	code := http.StatusOK
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { path = r.URL.Path; w.WriteHeader(code) }))
	hc := &http.Client{Timeout: time.Second}
	if !discoveryReachable(context.Background(), hc, srv.URL+"/realms/r/") || path != "/realms/r/.well-known/openid-configuration" {
		t.Fatalf("reachable issuer: %q", path)
	}
	code = http.StatusInternalServerError
	if discoveryReachable(context.Background(), hc, srv.URL) {
		t.Fatal("500 counts as reachable")
	}
	srv.Close()
	if discoveryReachable(context.Background(), hc, srv.URL) {
		t.Fatal("closed server counts as reachable")
	}
}

func TestFailuresAreRecordedOnce(t *testing.T) {
	h := NewHub()
	h.now = func() time.Time { return adminNow }
	f := session("s-x", "alice", "java", "Failed")
	h.onSessionStart(nil, f)
	h.onSessionStart(f, f) // an update of an already failed session
	h.starts.mu.Lock()
	n := len(h.starts.outcomes)
	h.starts.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d failures recorded", n)
	}
	// a failed session replayed after a restart is old: its Ready condition says so
	old := session("s-y", "alice", "java", "Failed")
	old.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, LastTransitionTime: metav1.NewTime(adminNow.Add(-time.Hour))}}
	for i := 0; i < 5; i++ {
		h.onSessionStart(nil, old)
	}
	if h.starts.startingLevel(adminNow) != ServiceLevelOk {
		t.Fatal("old failures count")
	}
}
