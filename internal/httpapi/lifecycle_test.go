package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // creation time of the test sessions

func mt(d time.Duration) *metav1.Time { x := metav1.NewTime(t0.Add(d)); return &x }

func TestStopsAt(t *testing.T) {
	short := javaTemplate()
	short.Spec.Timeouts = v1alpha1.Timeouts{Idle: &metav1.Duration{Duration: 10 * time.Minute}, Max: &metav1.Duration{Duration: 2 * time.Hour}}
	tests := []struct {
		name   string
		tpl    *v1alpha1.IDETemplate
		mod    func(*v1alpha1.IDESession)
		want   time.Duration
		reason SessionStopReason
		none   bool
	}{
		{"default idle from the agent ready time", nil, func(s *v1alpha1.IDESession) { s.Status.Timestamps.AgentReady = mt(time.Minute) }, 61 * time.Minute, Idle, false},
		{"claim time without agent", nil, func(s *v1alpha1.IDESession) { s.Status.Timestamps.Claimed = mt(2 * time.Minute) }, 62 * time.Minute, Idle, false},
		{"last activity wins", nil, func(s *v1alpha1.IDESession) {
			s.Status.Timestamps.AgentReady, s.Status.LastActivity = mt(time.Minute), mt(30*time.Minute)
		}, 90 * time.Minute, Idle, false},
		{"max lifetime is earlier", nil, func(s *v1alpha1.IDESession) {
			s.Status.Timestamps.AgentReady, s.Status.LastActivity = mt(time.Minute), mt(23*time.Hour+30*time.Minute)
		}, 24 * time.Hour, Max, false},
		{"template timeouts", short, func(s *v1alpha1.IDESession) { s.Status.Timestamps.AgentReady = mt(time.Minute) }, 11 * time.Minute, Idle, false},
		{"template max", short, func(s *v1alpha1.IDESession) {
			s.Status.Timestamps.AgentReady, s.Status.LastActivity = mt(time.Minute), mt(115*time.Minute)
		}, 2 * time.Hour, Max, false},
		{"not claimed yet", nil, func(*v1alpha1.IDESession) {}, 0, "", true},
		{"failed", nil, func(s *v1alpha1.IDESession) {
			s.Status.Phase, s.Status.Timestamps.Claimed = v1alpha1.SessionFailed, mt(0)
		}, 0, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := session("s", "alice", "java", "Ready")
			tc.mod(s)
			got := sessionToAPI(s, tc.tpl)
			if tc.none {
				if got.StopsAt != nil || got.StopReason != nil {
					t.Fatalf("want none, got %v %v", got.StopsAt, got.StopReason)
				}
				return
			}
			if got.StopsAt == nil || !got.StopsAt.Equal(t0.Add(tc.want)) || *got.StopReason != tc.reason {
				t.Fatalf("got %v %v, want %v %s", got.StopsAt, got.StopReason, t0.Add(tc.want), tc.reason)
			}
		})
	}
}

func TestDeletesAt(t *testing.T) {
	grace := 24 * time.Hour
	ws := workspace("w", "alice", "java")
	ws.Status.ExpiresAt = mt(14 * 24 * time.Hour)
	if got := deletesAt(ws, false, grace); got == nil || !got.Equal(t0.Add(15*24*time.Hour)) {
		t.Errorf("idle workspace: %v", got)
	}
	if deletesAt(ws, true, grace) != nil {
		t.Error("a running session cancels the deletion")
	}
	ws.Status.ExpiredAt = mt(15 * 24 * time.Hour)
	if got := deletesAt(ws, false, grace); !got.Equal(t0.Add(16 * 24 * time.Hour)) {
		t.Errorf("expired workspace counts from expiredAt: %v", got)
	}
	ws.Annotations = map[string]string{v1alpha1.AnnotationRetain: "true"}
	if deletesAt(ws, false, grace) != nil {
		t.Error("retained workspace is never deleted")
	}
	if deletesAt(workspace("w2", "alice", "java"), false, grace) != nil {
		t.Error("no expiresAt yet")
	}
}

func TestLifecycleTimesInAPI(t *testing.T) {
	run := session("s-run", "alice", "java", "Ready")
	run.Spec.WorkspaceRef = "w-1"
	run.Status.Timestamps.AgentReady = mt(time.Minute)
	busy := workspace("w-1", "alice", "java")
	busy.Status.ExpiresAt = mt(14 * 24 * time.Hour)
	idle := workspace("w-2", "alice", "java")
	idle.Status.ExpiresAt = mt(14 * 24 * time.Hour)
	e := newEnv(t, run, busy, idle)

	sess := decode[Session](t, e.do("GET", "/sessions/s-run", "alice", nil))
	if sess.StopsAt == nil || !sess.StopsAt.Equal(t0.Add(61*time.Minute)) || *sess.StopReason != Idle {
		t.Errorf("session: %+v", sess)
	}
	wsList := decode[[]Workspace](t, e.do("GET", "/workspaces", "alice", nil))
	if wsList[0].DeletesAt != nil || wsList[1].DeletesAt == nil || !wsList[1].DeletesAt.Equal(t0.Add(15*24*time.Hour)) {
		t.Errorf("workspaces: %+v", wsList)
	}
	o := decode[Overview](t, e.do("GET", "/me/overview", "alice", nil))
	for _, it := range o.Items {
		switch it.Kind {
		case OverviewItemKindSession:
			if it.StopsAt == nil || it.DeletesAt != nil {
				t.Errorf("session item: %+v", it)
			}
		default:
			if it.DeletesAt == nil || it.StopsAt != nil {
				t.Errorf("workspace item: %+v", it)
			}
		}
	}
}

func TestVariantsAsTemplateData(t *testing.T) {
	tpl := javaTemplate()
	tpl.Spec.InjectableKeys = append(tpl.Spec.InjectableKeys, v1alpha1.InjectableKey{Name: "TEMPLATE", Format: v1alpha1.FormatToken})
	tpl.Status.Image = &v1alpha1.ImageMetadata{Version: "1.2.3", Variants: []v1alpha1.ImageVariant{{ID: "maven", Name: "Maven"}}}
	e := newEnv(t)
	if err := e.k.Delete(context.Background(), javaTemplate()); err != nil {
		t.Fatal(err)
	}
	if err := e.k.Create(context.Background(), tpl); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("POST", "/sessions", "alice", launchBody(map[string]any{"data": map[string]string{"GIT_URI": gitURI, "TEMPLATE": "maven"}})); rec.Code != 201 {
		t.Fatalf("variant: %d %s", rec.Code, rec.Body)
	}
	p := problemCode(t, e, "POST", "/sessions", "alice", launchBody(map[string]any{"data": map[string]string{"GIT_URI": gitURI, "TEMPLATE": "bazel"}}), 400)
	if p.Code != CodeInvalidInput {
		t.Errorf("code %s", p.Code)
	}
}

func TestTemplatesCarryImageMetadata(t *testing.T) {
	tpl := javaTemplate()
	tpl.Status.Image = &v1alpha1.ImageMetadata{
		Version:  "1.2.3",
		Variants: []v1alpha1.ImageVariant{{ID: "maven", Name: "Maven", Description: "d", LogoURL: "https://example.org/m.svg"}},
		Includes: []v1alpha1.IncludedGroup{{Group: "Languages", Items: []v1alpha1.IncludedItem{{Name: "Java", Version: "21"}}}},
	}
	e := newEnv(t)
	_ = e.k.Delete(context.Background(), javaTemplate())
	_ = e.k.Create(context.Background(), tpl)
	got := decode[[]Template](t, e.do("GET", "/templates", "alice", nil))[0]
	if *got.ImageVersion != "1.2.3" || (*got.Variants)[0].Id != "maven" || *(*got.Variants)[0].LogoUrl == "" ||
		(*got.Includes)[0].Items[0].Name != "Java" || *(*got.Includes)[0].Items[0].Version != "21" {
		t.Errorf("template: %+v", got)
	}
	plain := newEnv(t)
	if g := decode[[]Template](t, plain.do("GET", "/templates", "alice", nil))[0]; g.Variants != nil || g.Includes != nil || g.ImageVersion != nil || g.StartEstimate != nil {
		t.Errorf("a template without metadata carries none: %+v", g)
	}
}

func readySession(name, template, strategy string, createdAgo, startTook time.Duration, workspace string) *v1alpha1.IDESession {
	now := time.Now()
	s := session(name, "alice", template, "Ready")
	s.CreationTimestamp = metav1.NewTime(now.Add(-createdAgo))
	s.Spec.WorkspaceRef = workspace
	s.Status.StartStrategy = v1alpha1.StartStrategy(strategy)
	rt := metav1.NewTime(now.Add(-createdAgo + startTook))
	s.Status.Timestamps.Ready = &rt
	return s
}

func TestStartEstimate(t *testing.T) {
	e := newEnv(t)
	h := e.srv.hub
	rec := func(s *v1alpha1.IDESession) {
		t.Helper()
		if !h.starts.record(s) {
			t.Fatal("not recorded")
		}
	}
	est := func() *StartEstimates {
		return decode[[]Template](t, e.do("GET", "/templates", "alice", nil))[0].StartEstimate
	}
	if est() != nil {
		t.Fatal("no data, no estimate")
	}
	m := 10 * time.Minute
	rec(readySession("a", "java", "cold", m, 40*time.Second, ""))
	rec(readySession("b", "java", "cold", m, 20*time.Second, ""))
	rec(readySession("c", "java", "warm-starting", m, 30*time.Second, ""))
	rec(readySession("d", "java", "warm-ready", m, 2*time.Second, ""))
	rec(readySession("e", "java", "cold", m, 90*time.Second, "w"))
	got := est()
	if got.None == nil || got.None.Strategy != StartEstimateStrategyCold || got.None.Seconds != 30 || got.None.Samples != 3 {
		t.Errorf("cold estimate without pool: %+v", got.None)
	}
	if got.Volume == nil || got.Volume.Seconds != 90 || got.Volume.Strategy != StartEstimateStrategyCold || got.Volume.Samples != 1 {
		t.Errorf("volume estimate: %+v", got.Volume)
	}

	// An idle ready instance in the pool switches the estimate to warm starts.
	pool := &v1alpha1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: testNS}, Spec: v1alpha1.IDEPoolSpec{TemplateRef: "java"}, Status: v1alpha1.IDEPoolStatus{IdleReady: 2}}
	if err := e.k.Create(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	if g := est(); g.None.Strategy != StartEstimateStrategyWarm || g.None.Seconds != 2 || g.None.Samples != 1 {
		t.Errorf("warm estimate: %+v", g.None)
	}
}

func TestStartEstimateWindows(t *testing.T) {
	st := newStartStats()
	now := time.Now()
	all := func(startSample) bool { return true }
	add := func(ago, dur time.Duration) { st.add("java", startSample{at: now.Add(-ago), dur: dur}) }
	add(5*time.Hour, 10*time.Second)
	add(6*time.Hour, 12*time.Second)
	if st.estimate("java", now, all) != nil {
		t.Error("two samples of the long window are too few")
	}
	add(7*time.Hour, 14*time.Second)
	if e := st.estimate("java", now, all); e == nil || e.Seconds != 12 || e.Samples != 3 {
		t.Errorf("long window: %+v", e)
	}
	add(48*time.Hour, time.Hour)
	if e := st.estimate("java", now, all); e.Samples != 3 {
		t.Errorf("samples older than 24 h do not count: %+v", e)
	}
	add(time.Minute, 4*time.Second)
	if e := st.estimate("java", now, all); e.Seconds != 4 || e.Samples != 1 {
		t.Errorf("the last hour wins: %+v", e)
	}
	for range startsCap + 10 {
		add(time.Second, time.Second)
	}
	if n := len(st.byTp["java"]); n != startsCap {
		t.Errorf("ring buffer holds %d", n)
	}
}

func TestHubRecordsStartsOncePerTransition(t *testing.T) {
	h := NewHub()
	ch, cancel := h.subscribe(templatesKey)
	defer cancel()
	pending := readySession("s", "java", "cold", time.Minute, 5*time.Second, "")
	pending.Status.Phase = v1alpha1.SessionStarting
	ready := readySession("s", "java", "cold", time.Minute, 5*time.Second, "")
	h.onSessionStart(pending, ready)
	select {
	case <-ch:
	default:
		t.Fatal("templates stream not notified")
	}
	h.onSessionStart(ready, ready) // resync of a Ready session
	if n := len(h.starts.byTp["java"]); n != 1 {
		t.Fatalf("%d samples", n)
	}
	noStrategy := readySession("x", "java", "", time.Minute, time.Second, "")
	h.onSessionStart(nil, noStrategy)
	if n := len(h.starts.byTp["java"]); n != 1 {
		t.Fatalf("a start without strategy is no sample: %d", n)
	}
}

func TestWatchTemplates(t *testing.T) {
	old := templateEventGap
	templateEventGap = 300 * time.Millisecond
	t.Cleanup(func() { templateEventGap = old })
	e := newEnv(t)
	e.srv.ping = 100 * time.Millisecond
	status, ctype, r := openStream(t, e, "/templates/events", "alice")
	if status != 200 || ctype != "text/event-stream" {
		t.Fatalf("%d %s", status, ctype)
	}
	if l, _ := r.next(t); l != "event: templates" {
		t.Fatalf("first line %q", l)
	}
	if l, _ := r.next(t); !strings.Contains(l, `"name":"java"`) {
		t.Fatalf("initial %q", l)
	}
	r.next(t)

	// Two starts in a burst give one event after the gap.
	first := time.Now()
	e.srv.hub.starts.add("java", startSample{at: time.Now(), dur: 7 * time.Second})
	e.srv.hub.Notify(templatesKey)
	e.srv.hub.Notify(templatesKey)
	for {
		l, ok := r.next(t)
		if !ok {
			t.Fatal("stream ended")
		}
		if strings.HasPrefix(l, "data: ") {
			if !strings.Contains(l, `"startEstimate":{"none":{"samples":1,"seconds":7`) {
				t.Fatalf("update %q", l)
			}
			break
		}
	}
	if time.Since(first) < 200*time.Millisecond {
		t.Errorf("events came faster than the gap")
	}
}

func TestWatchTemplatesNeedsAuth(t *testing.T) {
	e := newEnv(t)
	if status, _, _ := openStream(t, e, "/templates/events", "nobody"); status != 401 {
		t.Fatalf("%d", status)
	}
}
