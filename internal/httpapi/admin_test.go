package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/config"
)

const (
	traceID    = "0af7651916cd43dd8448eb211c80319c"
	secretVal  = "secret-token-value"
	adminRole  = "eduide-admin"
	studentSrc = "https://git.example.org/course/ex01-ab12cde.git"
)

var adminNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

var adminVerifier = fakeVerifier{
	"root":    {Owner: "root", Roles: []string{"other", adminRole}},
	"teacher": {Owner: "teacher", Roles: []string{"other"}},
	"alice":   alice,
}

func newAdminEnv(t *testing.T, mutate func(*config.Config), objs ...client.Object) *env {
	t.Helper()
	cfg := testConfig()
	cfg.Deployment, cfg.Hosts = "test", config.Hosts{Service: "ide.example.org", Instance: "s.example.org", Webview: "webview.s.example.org"}
	cfg.OIDC = config.OIDC{IssuerURL: "https://kc.example.org/realms/r", ClientIDs: []string{"landing"}, UserClaim: "preferred_username",
		Admin: &config.AdminRole{RolePath: "realm_access.roles", Role: adminRole}}
	cfg.Admin = config.Admin{RateLimitPerMinute: 1000, Links: config.AdminLinks{Jaeger: "https://jaeger.example.org"}}
	cfg.Workspaces.Retention = &metav1.Duration{Duration: 14 * 24 * time.Hour}
	size := resource.MustParse("2Gi")
	cfg.Workspaces.DefaultSize = &size
	if mutate != nil {
		mutate(cfg)
	}
	k := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(append([]client.Object{javaTemplate()}, objs...)...).Build()
	srv := NewServer(cfg, k, k, NewHub(), []byte("salt"))
	srv.now, srv.hub.now = func() time.Time { return adminNow }, func() time.Time { return adminNow }
	return &env{t: t, srv: srv, handler: srv.Handler(adminVerifier), k: k}
}

func adminPool(name string, min, target, max, ready int32, pressure bool) *v1alpha1.IDEPool {
	p := &v1alpha1.IDEPool{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec:       v1alpha1.IDEPoolSpec{TemplateRef: "java", MinIdle: min, TargetIdle: target, MaxInstances: max},
		Status:     v1alpha1.IDEPoolStatus{IdleReady: ready},
	}
	if pressure {
		p.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionCapacityPressure, Status: metav1.ConditionTrue}}
	}
	return p
}

func event(name, kind, regarding, controller, typ, reason, note string, at time.Time) *eventsv1.Event {
	return &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}, EventTime: metav1.NewMicroTime(at),
		Regarding: corev1.ObjectReference{Kind: kind, Name: regarding, Namespace: testNS}, ReportingController: controller, Type: typ, Reason: reason, Note: note,
	}
}

// fixture: two sessions of different users with launch data and a trace, a workspace, pools, events, a quota.
func fixture() []client.Object {
	sa := session("s-aaaaaaaaaa", "alice", "java", "Ready")
	sa.Annotations = map[string]string{v1alpha1.AnnotationTraceparent: "00-" + traceID + "-b7ad6b7169203331-01"}
	sa.Spec.DataSecretRef = "s-aaaaaaaaaa-data"
	sa.Spec.Display = v1alpha1.Display{Source: studentSrc}
	ready := metav1.NewTime(adminNow.Add(-10 * time.Minute))
	sa.Status.Timestamps.Ready, sa.Status.URL, sa.Status.Instance = &ready, "https://s.example.org/inst-secret-id/", "inst-secret-id"
	sb := session("s-bbbbbbbbbb", "bob", "java", "Failed")
	sb.Spec.WorkspaceRef = "ws-w2"
	sb.Status.ErrorCode, sb.Status.Message = "start-failed", "image pull back-off"
	sc := session("s-cccccccccc", "bob", "java", "Starting")
	sc.Spec.Display = v1alpha1.Display{Title: "Sorting lab"}
	wsAlice := workspace("ws-w1", "alice", "java")
	wsAlice.Status.Phase = v1alpha1.WorkspaceReady
	soon := metav1.NewTime(adminNow.Add(-24*time.Hour + 48*time.Hour)) // + grace 24h = deleted in 3 days minus... see test
	wsAlice.Status.ExpiresAt = &soon
	wsBob := workspace("ws-w2", "bob", "java")
	wsBob.Status.Phase = v1alpha1.WorkspaceExpired
	past := metav1.NewTime(adminNow.Add(-2 * time.Hour))
	wsBob.Status.ExpiredAt = &past
	q := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "q", Namespace: testNS}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourceRequestsStorage: resource.MustParse("100Gi")}},
		Status: corev1.ResourceQuotaStatus{Hard: corev1.ResourceList{corev1.ResourceRequestsStorage: resource.MustParse("100Gi")}, Used: corev1.ResourceList{corev1.ResourceRequestsStorage: resource.MustParse("4Gi")}}}
	return []client.Object{
		sa, sb, sc, wsAlice, wsBob, q,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s-aaaaaaaaaa-data", Namespace: testNS}, Data: map[string][]byte{"ARTEMIS_TOKEN": []byte(secretVal), "GIT_USER": []byte("Alice A")}},
		adminPool("java-pool", 1, 4, 10, 0, false),
		event("e1", "IDEPool", "java-pool", scalerController, "Normal", "ScaledUp", "strategy reactive: targetIdle 2 to 4 (claim rate rose)", adminNow.Add(-2*time.Minute)),
		event("e2", "IDESession", "s-bbbbbbbbbb", "eduide-operator", "Warning", "Failed", "start-failed: image pull back-off", adminNow.Add(-5*time.Minute)),
		event("e3", "IDESession", "s-bbbbbbbbbb", "eduide-operator", "Warning", "Failed", "old-event: too old", adminNow.Add(-3*time.Hour)),
		event("e4", "IDESession", "s-aaaaaaaaaa", "eduide-operator", "Normal", "Ready", "ready after 1.2s", adminNow.Add(-1*time.Minute)),
	}
}

var adminRoutes = []string{"/admin/status", "/admin/pools", "/admin/sessions", "/admin/sessions/s-aaaaaaaaaa", "/admin/workspaces", "/admin/problems", "/admin/templates", "/admin/config"}

// rawDo sends a request without the spec check, for routes that are not in the spec.
func rawDo(e *env, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, basePath+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminDisabledWithoutConfig(t *testing.T) {
	e := newAdminEnv(t, func(c *config.Config) { c.OIDC.Admin = nil }, fixture()...)
	for _, p := range append(adminRoutes, "/admin/status/events", "/admin/sessions/events", "/admin/nothing") {
		for _, tok := range []string{"", "root", "alice"} {
			if rec := rawDo(e, "GET", p, tok); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"not-found"`) {
				t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
			}
		}
	}
	// The user API stays up.
	if rec := e.do("GET", "/sessions", "alice", nil); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}

func TestAdminAuth(t *testing.T) {
	e := newAdminEnv(t, nil, fixture()...)
	for _, p := range adminRoutes {
		problemCode(t, e, "GET", p, "", nil, 401)
		problemCode(t, e, "GET", p, "nobody", nil, 401)
		if pr := problemCode(t, e, "GET", p, "alice", nil, 403); pr.Code != CodeForbidden {
			t.Errorf("%s: %s", p, pr.Code)
		}
		problemCode(t, e, "GET", p, "teacher", nil, 403)
		if rec := e.do("GET", p, "root", nil); rec.Code != 200 {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		if rec := rawDo(e, m, "/admin/sessions/s-aaaaaaaaaa", "root"); rec.Code != 405 && rec.Code != 404 {
			t.Errorf("%s answered %d", m, rec.Code)
		}
	}
	// Streams need the role as well.
	if status, _, _ := openStream(t, e, "/admin/status/events", "alice"); status != 403 {
		t.Errorf("stream %d", status)
	}
}

func TestAdminRateLimit(t *testing.T) {
	e := newAdminEnv(t, func(c *config.Config) { c.Admin.RateLimitPerMinute = 2 }, fixture()...)
	for i, want := range []int{200, 200, 429} {
		if rec := e.do("GET", "/admin/config", "root", nil); rec.Code != want {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	if pr := problemCode(t, e, "GET", "/admin/config", "root", nil, 429); pr.Code != CodeRateLimited {
		t.Fatal(pr.Code)
	}
}

// No response of any admin endpoint names a user, a launch data value, an instance id or a git source.
func TestAdminPrivacy(t *testing.T) {
	e := newAdminEnv(t, nil, fixture()...)
	for _, p := range append(adminRoutes, "/admin/sessions?limit=500", "/admin/problems?since=1h") {
		body := e.do("GET", p, "root", nil).Body.String()
		for _, bad := range []string{"alice", "Alice", "bob", "Bob", "example.org/alice", "@example.org", secretVal, "inst-secret-id", "ab12cde", "course/ex01", "preferred_username\":\"a"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s leaks %q: %s", p, bad, body)
			}
		}
	}
	// the owner appears as the hash only
	list := decode[AdminSessionList](t, e.do("GET", "/admin/sessions", "root", nil))
	for _, s := range list.Items {
		if len(s.OwnerHash) != 40 {
			t.Errorf("owner hash %q", s.OwnerHash)
		}
	}
}

func TestAdminSessions(t *testing.T) {
	e := newAdminEnv(t, nil, fixture()...)
	list := decode[AdminSessionList](t, e.do("GET", "/admin/sessions", "root", nil))
	if list.Total != 3 || len(list.Items) != 3 || list.Next != nil || list.Items[0].Name != "s-aaaaaaaaaa" {
		t.Fatalf("%+v", list)
	}
	a := list.Items[0]
	if a.TraceId == nil || *a.TraceId != traceID || a.TraceUrl == nil || *a.TraceUrl != "https://jaeger.example.org/trace/"+traceID {
		t.Errorf("trace %+v", a)
	}
	if a.Title == nil || *a.Title != "ex01" || a.Persistence != "none" || a.StopsAt == nil {
		t.Errorf("session %+v", a)
	}
	if list.Items[1].Persistence != "volume" || deref(list.Items[1].ErrorCode) != "start-failed" {
		t.Errorf("failed session %+v", list.Items[1])
	}

	for q, want := range map[string]int{
		"?phase=Failed": 1, "?persistence=volume": 1, "?persistence=none": 2, "?template=nope": 0,
		"?q=SORTING": 1, "?q=ex01": 1, "?q=s-bb": 1, "?phase=Ready&persistence=none": 1,
	} {
		if got := decode[AdminSessionList](t, e.do("GET", "/admin/sessions"+q, "root", nil)); got.Total != want || len(got.Items) != want {
			t.Errorf("%s: %d items, want %d", q, got.Total, want)
		}
	}
	// paging by cursor
	p1 := decode[AdminSessionList](t, e.do("GET", "/admin/sessions?limit=2", "root", nil))
	if len(p1.Items) != 2 || p1.Next == nil || p1.Total != 3 {
		t.Fatalf("page 1 %+v", p1)
	}
	p2 := decode[AdminSessionList](t, e.do("GET", "/admin/sessions?limit=2&cursor="+*p1.Next, "root", nil))
	if len(p2.Items) != 1 || p2.Next != nil || p2.Items[0].Name != "s-cccccccccc" {
		t.Fatalf("page 2 %+v", p2)
	}
	for _, bad := range []string{"?limit=0", "?limit=501", "?cursor=***", "?phase=Bogus"} {
		if rec := e.do("GET", "/admin/sessions"+bad, "root", nil); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}

func TestAdminSessionDetail(t *testing.T) {
	e := newAdminEnv(t, nil, fixture()...)
	d := decode[AdminSessionDetail](t, e.do("GET", "/admin/sessions/s-aaaaaaaaaa", "root", nil))
	if strings.Join(d.LaunchDataKeys, ",") != "ARTEMIS_TOKEN,GIT_USER" || d.Timestamps == nil || d.Timestamps.Ready == nil || d.TraceId == nil {
		t.Fatalf("%+v", d)
	}
	if d := decode[AdminSessionDetail](t, e.do("GET", "/admin/sessions/s-cccccccccc", "root", nil)); len(d.LaunchDataKeys) != 0 {
		t.Fatalf("keys %v", d.LaunchDataKeys)
	}
	if pr := problemCode(t, e, "GET", "/admin/sessions/s-nope", "root", nil, 404); pr.Code != CodeNotFound {
		t.Fatal(pr.Code)
	}
}

func TestAdminWorkspaces(t *testing.T) {
	// ws-w1: expires in 2 days, grace 24h: deleted in 3 days (state expiring), but a running session would make it inUse.
	e := newAdminEnv(t, nil, fixture()...)
	l := decode[AdminWorkspaceList](t, e.do("GET", "/admin/workspaces", "root", nil))
	if l.Total != 2 || l.Summary.Total != 2 || l.Summary.Expired != 1 || l.Summary.ExpiringIn3Days != 1 || l.Summary.InUse != 0 || l.Summary.RequestedBytes != 4<<30 {
		t.Fatalf("%+v", l.Summary)
	}
	if l.Summary.Quota == nil || l.Summary.Quota.HardBytes != 100<<30 || l.Summary.Quota.UsedBytes != 4<<30 {
		t.Fatalf("quota %+v", l.Summary.Quota)
	}
	if l.Items[0].Name != "ws-w1" || l.Items[0].State != AdminWorkspaceStateExpiring || l.Items[1].State != AdminWorkspaceStateExpired {
		t.Fatalf("%+v", l.Items)
	}
	// filters change the page, not the summary
	f := decode[AdminWorkspaceList](t, e.do("GET", "/admin/workspaces?state=expired", "root", nil))
	if f.Total != 1 || f.Summary.Total != 2 || f.Items[0].Name != "ws-w2" {
		t.Fatalf("%+v", f)
	}
	if f := decode[AdminWorkspaceList](t, e.do("GET", "/admin/workspaces?q="+l.Items[0].OwnerHash[:8], "root", nil)); f.Total != 1 {
		t.Fatalf("hash search %d", f.Total)
	}
	// a running session makes the workspace inUse
	busy := session("s-dddddddddd", "alice", "java", "Ready")
	busy.Spec.WorkspaceRef = "ws-w1"
	if err := e.k.Create(context.Background(), busy); err != nil {
		t.Fatal(err)
	}
	b := decode[AdminWorkspaceList](t, e.do("GET", "/admin/workspaces?state=inUse", "root", nil))
	if b.Total != 1 || b.Summary.InUse != 1 || b.Summary.ExpiringIn3Days != 0 {
		t.Fatalf("%+v", b)
	}
}

func TestAdminWorkspaceWithoutQuota(t *testing.T) {
	e := newAdminEnv(t, nil, workspace("ws-x", "alice", "java"))
	if l := decode[AdminWorkspaceList](t, e.do("GET", "/admin/workspaces", "root", nil)); l.Summary.Quota != nil {
		t.Fatalf("%+v", l.Summary)
	}
}

func TestAdminPoolsAndStatus(t *testing.T) {
	objs := append(fixture(), adminPool("full-pool", 0, 2, 4, 2, false), adminPool("pressed", 2, 2, 4, 0, true))
	e := newAdminEnv(t, func(c *config.Config) { c.Admin.ScalerInterval = &metav1.Duration{Duration: time.Minute} }, objs...)
	// the pool has been empty for 7 minutes
	e.srv.hub.emptySince["java-pool"] = adminNow.Add(-7 * time.Minute)

	pools := decode[AdminPools](t, e.do("GET", "/admin/pools", "root", nil))
	by := map[string]AdminPool{}
	for _, p := range pools.Pools {
		by[p.Name] = p
	}
	if by["java-pool"].State != AdminPoolStateEmpty || by["full-pool"].State != AdminPoolStateOk || by["pressed"].State != AdminPoolStatePressure || by["java-pool"].EmptySince == nil {
		t.Fatalf("%+v", pools.Pools)
	}
	if d := by["java-pool"].LastDecision; d == nil || d.Reason != ScaledUp || !strings.Contains(d.Message, "reactive") || len(pools.Decisions) != 1 {
		t.Fatalf("decision %+v", pools.Decisions)
	}

	st := decode[AdminStatus](t, e.do("GET", "/admin/status", "root", nil))
	if st.RunningSessions != 1 || st.StartingSessions != 1 || st.IdleReady != 2 || !st.CapacityPressure || st.Stats != nil || st.NodePools != nil {
		t.Fatalf("%+v", st)
	}
	// java-pool wants max(1,4)=4, full-pool 2, pressed 2
	if st.IdleTarget != 8 {
		t.Errorf("idleTarget %d", st.IdleTarget)
	}
	codes := map[AdminWarningCode]string{}
	for _, w := range st.Warnings {
		codes[w.Code] = deref(w.Pool) + w.Message
	}
	if !strings.Contains(codes[PoolEmpty], "java-pool") || !strings.Contains(codes[PoolEmpty], "7 min") || !strings.Contains(codes[CapacityPressure], "pressed") {
		t.Fatalf("warnings %v", codes)
	}
	if _, ok := codes[ScalerSilent]; ok {
		t.Fatalf("the scaler wrote an event 2 minutes ago, interval 1 minute: %v", codes)
	}
}

func TestAdminScalerSilent(t *testing.T) {
	cfg := func(c *config.Config) { c.Admin.ScalerInterval = &metav1.Duration{Duration: time.Minute} }
	silent := func(e *env) bool {
		for _, w := range decode[AdminStatus](t, e.do("GET", "/admin/status", "root", nil)).Warnings {
			if w.Code == ScalerSilent {
				return true
			}
		}
		return false
	}
	pool := adminPool("p", 0, 0, 4, 0, false)
	// last scaler event 3 minutes ago, interval 1 minute
	if !silent(newAdminEnv(t, cfg, pool, event("e", "IDEPool", "p", scalerController, "Normal", "ScaledUp", "x", adminNow.Add(-3*time.Minute)))) {
		t.Error("no warning for a silent scaler")
	}
	if silent(newAdminEnv(t, cfg, pool, event("e", "IDEPool", "p", scalerController, "Normal", "Held", "x", adminNow.Add(-90*time.Second)))) {
		t.Error("warning within 2 intervals")
	}
	// events of the operator do not count as scaler events
	if !silent(newAdminEnv(t, cfg, pool, event("e", "IDEPool", "p", "eduide-operator", "Normal", "ScaledUp", "x", adminNow))) {
		t.Error("operator event counted")
	}
	// without a configured scaler there is nothing to warn about
	if silent(newAdminEnv(t, nil, pool)) {
		t.Error("warning without a scaler")
	}
}

func TestAdminProblems(t *testing.T) {
	e := newAdminEnv(t, nil, fixture()...)
	p := decode[AdminProblems](t, e.do("GET", "/admin/problems", "root", nil))
	if p.Since != "24h" || p.Codes != nil || len(p.Events) != 1 {
		t.Fatalf("%+v", p)
	}
	ev := p.Events[0]
	if deref(ev.Code) != "start-failed" || ev.Message != "image pull back-off" || deref(ev.Session) != "s-bbbbbbbbbb" || deref(ev.Template) != "java" || len(deref(ev.OwnerHash)) != 40 {
		t.Fatalf("%+v", ev)
	}
	if rec := e.do("GET", "/admin/problems?since=7d", "root", nil); rec.Code != 400 {
		t.Fatalf("since=7d: %d", rec.Code)
	}
}

func TestAdminTemplatesAndConfig(t *testing.T) {
	e := newAdminEnv(t, nil, adminPool("java-pool", 1, 4, 10, 0, false))
	ts := decode[[]AdminTemplate](t, e.do("GET", "/admin/templates", "root", nil))
	if len(ts) != 1 {
		t.Fatalf("%+v", ts)
	}
	tpl := ts[0]
	if tpl.Pool == nil || tpl.Pool.Name != "java-pool" || tpl.Timeouts.IdleSeconds != 3600 || tpl.Timeouts.MaxSeconds != 86400 || tpl.Timeouts.WarningSeconds != 3000 || tpl.RetentionSeconds != nil {
		t.Fatalf("%+v", tpl)
	}
	keys := map[string]AdminInjectableKey{}
	for _, k := range tpl.InjectableKeys {
		keys[k.Name] = k
	}
	if len(keys) != 5 || keys["GIT_URI"].Format == nil || *keys["GIT_URI"].Format != HTTPSURL || !keys["GIT_URI"].Required || keys["GIT_USER"].Source != UserName {
		t.Fatalf("keys %+v", keys)
	}

	c := decode[AdminConfig](t, e.do("GET", "/admin/config", "root", nil))
	if c.Oidc.AdminRolePath != "realm_access.roles" || c.Oidc.AdminRole != adminRole || c.RateLimit.AdminPerMinute != 1000 || c.LaunchDataMaxBytes != 8192 || !c.Telemetry.UserIdsHashed ||
		c.Admin.PrometheusConfigured || c.Admin.Links == nil || deref(c.Admin.Links.Jaeger) != "https://jaeger.example.org" || c.Workspaces.DefaultSizeBytes != 2<<30 {
		t.Fatalf("%+v", c)
	}
	if body := e.do("GET", "/admin/config", "root", nil).Body.String(); strings.Contains(body, "salt") {
		t.Fatalf("config leaks the salt: %s", body)
	}
}

func TestTraceID(t *testing.T) {
	for in, want := range map[string]string{
		"00-" + traceID + "-b7ad6b7169203331-01": traceID,
		"":                                       "",
		"00-00000000000000000000000000000000-b7ad6b7169203331-01": "",
		"00-" + strings.ToUpper(traceID) + "-b7ad6b7169203331-01": "",
		"garbage": "",
		"00-" + traceID + "-b7ad6b7169203331-01-extra": "",
		"00-" + traceID[:30] + "-b7ad6b7169203331-01":  "",
	} {
		if got := traceIDOf(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	e := newAdminEnv(t, func(c *config.Config) {
		c.Admin.Links = config.AdminLinks{Jaeger: "https://j.example.org", TraceURLTemplate: "https://t.example.org/?id={traceId}"}
	})
	if u := e.srv.traceURL("abc"); u == nil || *u != "https://t.example.org/?id=abc" {
		t.Fatalf("%v", u)
	}
	e = newAdminEnv(t, func(c *config.Config) { c.Admin.Links = config.AdminLinks{} })
	if e.srv.traceURL("abc") != nil {
		t.Fatal("link without config")
	}
}

func TestAdminStatusStream(t *testing.T) {
	old := adminEventGap
	adminEventGap = 300 * time.Millisecond
	t.Cleanup(func() { adminEventGap = old })
	e := newAdminEnv(t, nil, fixture()...)
	e.srv.ping = 40 * time.Millisecond
	status, ctype, r := openStream(t, e, "/admin/status/events", "root")
	if status != 200 || ctype != "text/event-stream" {
		t.Fatalf("%d %s", status, ctype)
	}
	if l, _ := r.next(t); l != "event: status" {
		t.Fatalf("first line %q", l)
	}
	if l, _ := r.next(t); !strings.Contains(l, `"runningSessions":1`) {
		t.Fatalf("initial %q", l)
	}
	r.next(t)

	// A burst of changes yields one event after the gap, and the pings go on in between.
	start := time.Now()
	for i := 0; i < 5; i++ {
		s := session("s-new"+strings.Repeat("x", i), "alice", "java", "Ready")
		if err := e.k.Create(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		e.srv.hub.onEvent(s)
	}
	var firstAt time.Duration
	for {
		l, ok := r.next(t)
		if !ok {
			t.Fatal("stream ended")
		}
		if strings.HasPrefix(l, "data: ") {
			firstAt = time.Since(start)
			if !strings.Contains(l, `"runningSessions":6`) {
				t.Fatalf("burst not coalesced: %q", l)
			}
			break
		}
	}
	if firstAt < 200*time.Millisecond {
		t.Errorf("event after %v, the gap is 300ms", firstAt)
	}
	e.srv.hub.mu.Lock()
	n := len(e.srv.hub.subs[adminKey])
	e.srv.hub.mu.Unlock()
	if n != 1 {
		t.Fatalf("subscribers %d", n)
	}
}

func TestAdminStreamsDoNotLeak(t *testing.T) {
	e := newAdminEnv(t, nil, fixture()...)
	ts := httptest.NewServer(e.handler)
	defer ts.Close()
	for _, p := range []string{"/admin/status/events", "/admin/sessions/events"} {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+basePath+p, nil)
		req.Header.Set("Authorization", "Bearer root")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
		cancel()
		_ = resp.Body.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.srv.hub.mu.Lock()
		n := len(e.srv.hub.subs[adminKey])
		e.srv.hub.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("admin streams leave subscriptions behind")
}

func TestHubTracksEmptyPools(t *testing.T) {
	h := NewHub()
	now := adminNow
	h.now = func() time.Time { return now }
	p := adminPool("p", 0, 2, 4, 0, false)
	h.onEvent(p)
	now = now.Add(3 * time.Minute)
	h.onEvent(p) // still empty: the first sighting stays
	if since, ok := h.poolEmptySince("p"); !ok || !since.Equal(adminNow) {
		t.Fatalf("%v %v", since, ok)
	}
	p.Status.IdleReady = 1
	h.onEvent(p)
	if _, ok := h.poolEmptySince("p"); ok {
		t.Fatal("not cleared")
	}
	h.onEvent(adminPool("q", 0, 2, 4, 0, true)) // under pressure the operator keeps no idle instance: not empty
	if _, ok := h.poolEmptySince("q"); ok {
		t.Fatal("pressure counted as empty")
	}
}

// --- Prometheus ---

type fakeProm struct {
	srv   *httptest.Server
	calls atomic.Int32
	auth  atomic.Value
}

func newFakeProm(t *testing.T, answer func(path, query string) (int, string)) *fakeProm {
	t.Helper()
	f := &fakeProm{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		f.auth.Store(r.Header.Get("Authorization") + r.Header.Get("Cookie"))
		code, body := answer(r.URL.Path, r.URL.Query().Get("query"))
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func vec(v string, labels ...string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(labels); i += 2 {
		m[labels[i]] = labels[i+1]
	}
	b, _ := json.Marshal(m)
	return `{"metric":` + string(b) + `,"value":[1,"` + v + `"]}`
}

func matrix(labels map[string]string, vals ...string) string {
	b, _ := json.Marshal(labels)
	pts := []string{}
	for i, v := range vals {
		pts = append(pts, fmt.Sprintf(`[%d,"%s"]`, 1800000000+i*3600, v))
	}
	return `{"metric":` + string(b) + `,"values":[` + strings.Join(pts, ",") + `]}`
}

func promAnswer(path, q string) (int, string) {
	ok := func(typ string, results ...string) (int, string) {
		return 200, `{"status":"success","data":{"resultType":"` + typ + `","result":[` + strings.Join(results, ",") + `]}}`
	}
	if strings.HasSuffix(path, "/query_range") {
		switch {
		case strings.Contains(q, "eduide_pool_instances"):
			return ok("matrix", matrix(map[string]string{"pool": "java-pool"}, "3", "2", "0"))
		case strings.Contains(q, "histogram_quantile(0.5"):
			return ok("matrix", matrix(nil, "0.4", "0.5"))
		case strings.Contains(q, "histogram_quantile(0.95"):
			return ok("matrix", matrix(nil, "1.4", "NaN"))
		case strings.Contains(q, `strategy=~"warm.*"`):
			return ok("matrix", matrix(nil, "10.2", "12"))
		case strings.Contains(q, `strategy="cold"`):
			return ok("matrix", matrix(nil, "3", "4"))
		}
		return ok("matrix") // failed starts: no series
	}
	switch {
	case strings.Contains(q, "by (code)"):
		return ok("vector", vec("9", "code", "capacity-exhausted"), vec("4.2", "code", "start-failed"), vec("0", "code", "template-not-found"))
	case strings.Contains(q, "by (le, template, persistence)"):
		return ok("vector", vec("1.4", "template", "java", "persistence", "none"), vec("25", "template", "java", "persistence", "volume"), vec("NaN", "template", "c", "persistence", "none"))
	case strings.Contains(q, "eduide_capacity_pressure"):
		return ok("vector", vec("1", "node_pool", "ide-general"), vec("0", "node_pool", "ide-gpu"))
	case strings.Contains(q, "startup_seconds_count"):
		return ok("vector", vec("212.4"))
	case strings.Contains(q, "failures_total"):
		return ok("vector", vec("3"))
	case strings.Contains(q, "kube_pod_container_resource_requests"):
		return ok("vector", vec("71.2"))
	case strings.Contains(q, "kube_node_info"):
		return ok("vector", vec("9"))
	}
	return 400, `{"status":"error","error":"unexpected query"}`
}

func TestAdminWithPrometheus(t *testing.T) {
	f := newFakeProm(t, promAnswer)
	e := newAdminEnv(t, func(c *config.Config) { c.Admin.PrometheusURL = f.srv.URL }, fixture()...)
	st := decode[AdminStatus](t, e.do("GET", "/admin/status", "root", nil))
	s := st.Stats
	if s == nil || *s.StartsLastHour != 212 || *s.FailuresLastHour != 3 || *s.NodeCount != 9 || *s.CpuRequestsPercent != 71.2 {
		t.Fatalf("stats %+v", s)
	}
	if s.StartP95 == nil || len(*s.StartP95) != 2 || (*s.StartP95)[0].Template != "java" || (*s.StartP95)[1].Persistence != "volume" || (*s.StartP95)[1].Seconds != 25 {
		t.Fatalf("p95 %+v", s.StartP95)
	}
	if s.StartsPerHour == nil || len(*s.StartsPerHour) != 2 || (*s.StartsPerHour)[0].Warm != 10 || (*s.StartsPerHour)[0].Cold != 3 || (*s.StartsPerHour)[0].Failed != 0 {
		t.Fatalf("starts %+v", s.StartsPerHour)
	}
	if s.StartLatency == nil || len(*s.StartLatency) != 2 || *(*s.StartLatency)[0].P50 != 0.4 || *(*s.StartLatency)[0].P95 != 1.4 || (*s.StartLatency)[1].P95 != nil {
		t.Fatalf("latency %+v", s.StartLatency)
	}
	if s.PoolFree == nil || (*s.PoolFree)[0].Pool != "java-pool" || len((*s.PoolFree)[0].Points) != 3 {
		t.Fatalf("pool free %+v", s.PoolFree)
	}
	if st.NodePools == nil || len(*st.NodePools) != 2 || (*st.NodePools)[0].Name != "ide-general" || !(*st.NodePools)[0].Pressure || (*st.NodePools)[1].Pressure {
		t.Fatalf("node pools %+v", st.NodePools)
	}

	p := decode[AdminProblems](t, e.do("GET", "/admin/problems?since=1h", "root", nil))
	if p.Codes == nil || len(*p.Codes) != 2 || (*p.Codes)[0].Code != "capacity-exhausted" || (*p.Codes)[0].Count != 9 || (*p.Codes)[1].Count != 4 {
		t.Fatalf("codes %+v", p.Codes)
	}
	// no credentials, and a second request within the cache time asks nothing new
	if a, _ := f.auth.Load().(string); a != "" {
		t.Errorf("credentials sent: %q", a)
	}
	n := f.calls.Load()
	e.do("GET", "/admin/status", "root", nil)
	if f.calls.Load() != n {
		t.Errorf("%d new queries within the cache time", f.calls.Load()-n)
	}
	// the config shows the URL, a user API response never does
	if c := decode[AdminConfig](t, e.do("GET", "/admin/config", "root", nil)); !c.Admin.PrometheusConfigured || deref(c.Admin.PrometheusURL) != f.srv.URL {
		t.Errorf("%+v", c.Admin)
	}
	if body := e.do("GET", "/templates", "alice", nil).Body.String(); strings.Contains(body, f.srv.URL) {
		t.Error("prometheus URL in a user response")
	}
}

func TestAdminPrometheusFailureHidesStats(t *testing.T) {
	for name, answer := range map[string]func(string, string) (int, string){
		"error status": func(string, string) (int, string) { return 500, "boom" },
		"error body":   func(string, string) (int, string) { return 200, `{"status":"error","error":"bad"}` },
		"not json":     func(string, string) (int, string) { return 200, `<html>` },
		"too large":    func(string, string) (int, string) { return 200, strings.Repeat("x", promMaxBody+10) },
	} {
		f := newFakeProm(t, answer)
		e := newAdminEnv(t, func(c *config.Config) { c.Admin.PrometheusURL = f.srv.URL }, fixture()...)
		st := decode[AdminStatus](t, e.do("GET", "/admin/status", "root", nil))
		if st.Stats != nil || st.NodePools != nil || st.RunningSessions != 1 {
			t.Errorf("%s: %+v", name, st)
		}
		if p := decode[AdminProblems](t, e.do("GET", "/admin/problems", "root", nil)); p.Codes != nil {
			t.Errorf("%s: codes %+v", name, p.Codes)
		}
	}
}

func TestPromClientDoesNotFollowRedirects(t *testing.T) {
	var hit atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Store(true) }))
	defer other.Close()
	f := newFakeProm(t, nil)
	f.srv.Config.Handler = http.RedirectHandler(other.URL, http.StatusFound)
	if _, err := newPromClient(f.srv.URL).Instant(context.Background(), "up"); err == nil || hit.Load() {
		t.Fatalf("err %v, redirect followed: %v", err, hit.Load())
	}
}

func TestPromClientTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newPromClient(slow.URL).Instant(ctx, "up")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
	if strings.Contains(err.Error(), slow.URL) {
		t.Fatal("the URL is in the error")
	}
}
