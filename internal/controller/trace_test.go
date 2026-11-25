package controller

import (
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

func ms(name string, at time.Time) agentapi.Milestone { return agentapi.Milestone{Name: name, At: at} }

func TestMilestoneSpans(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	got := map[string]phaseSpan{}
	for _, p := range milestoneSpans([]agentapi.Milestone{
		ms("claim-received", at(0.01)), ms("data-delivered", at(3.2)), ms("first-request", at(1)), ms("first-websocket", at(1.2)),
		ms("plugin-host-started", at(3)), ms("workspace-ready", at(5)), ms("first-input", at(8)),
	}) {
		got[p.name] = p
	}
	want := map[string][2]float64{
		"browser.connect": {0.01, 1}, "ws.connect": {1, 1.2}, "data.delivered": {3, 3.2}, "ide.load": {1, 3},
		"workspace.ready": {3, 5}, "first.input": {5, 8}, "ide.usable": {0.01, 5},
	}
	if len(got) != len(want) {
		t.Errorf("spans %v", got)
	}
	for n, w := range want {
		if g := got[n]; !g.start.Equal(at(w[0])) || !g.end.Equal(at(w[1])) {
			t.Errorf("%s: %v to %v, want %v to %v", n, g.start, g.end, at(w[0]), at(w[1]))
		}
	}

	// An instance that was idle in a pool: the plugin host started long before the browser came.
	warm := milestoneSpans([]agentapi.Milestone{ms("plugin-host-started", at(-60)), ms("first-request", at(1)), ms("workspace-ready", at(-50)), ms("claim-received", at(0))})
	for _, p := range warm {
		if p.end.Before(p.start) {
			t.Errorf("negative span %+v", p)
		}
		if p.name == "ide.load" || p.name == "workspace.ready" {
			t.Errorf("span %s must be left out when the IDE started before the browser", p.name)
		}
	}
	if len(milestoneSpans(nil)) != 0 {
		t.Error("spans without milestones")
	}
}

func TestPodPhases(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	tm := func(s int) metav1.Time { return metav1.NewTime(t0.Add(time.Duration(s) * time.Second)) }
	running := func(s int) corev1.ContainerStatus {
		return corev1.ContainerStatus{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: tm(s)}}}
	}
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: tm(0)},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: tm(1)},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: tm(9)},
			},
			ContainerStatuses: []corev1.ContainerStatus{running(4), running(6)},
		},
	}
	var names []string
	for i, ph := range podPhases(p) {
		names = append(names, ph.name)
		if want := []int{0, 1, 4, 6}[i]; !ph.start.Equal(t0.Add(time.Duration(want) * time.Second)) {
			t.Errorf("%s starts at %v", ph.name, ph.start)
		}
	}
	if !slices.Equal(names, []string{"pod.scheduled", "image.pulled", "containers.started", "pod.ready"}) {
		t.Errorf("phases %v", names)
	}
	// A pod that is not ready yet has no complete phase after its start.
	p.Status.Conditions = p.Status.Conditions[:1]
	if last := podPhases(p)[3]; !last.end.IsZero() {
		t.Errorf("pod.ready without a ready condition: %+v", last)
	}
}

func TestNextMilestoneCheck(t *testing.T) {
	r := time.Now()
	for age, want := range map[time.Duration]time.Duration{0: 31 * time.Second, 29 * time.Second: 2 * time.Second, 31 * time.Second: 90 * time.Second, 119 * time.Second: 2 * time.Second, 121 * time.Second: 0} {
		if got := nextMilestoneCheck(r.Add(age), r); got != want {
			t.Errorf("age %v: %v, want %v", age, got, want)
		}
	}
}

// recordSpans records the spans of the test. It also takes the clock back to the wall time: earlier tests move it
// forward for good, which would make a new pod look overdue.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	clk.mu.Lock()
	off := clk.off
	clk.off = 0
	clk.mu.Unlock()
	t.Cleanup(func() { clk.Step(off) })
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func attrsOf(s sdktrace.ReadOnlySpan) map[string]string {
	m := map[string]string{}
	for _, a := range s.Attributes() {
		m[string(a.Key)] = a.Value.String()
	}
	return m
}

// TestLaunchTraceStructure checks the trace of one cold start and the late spans from the agent milestones.
func TestLaunchTraceStructure(t *testing.T) {
	requireEnv(t)
	rec := recordSpans(t)
	// Check for milestones soon after Ready instead of after 30 s.
	prevChecks := milestoneChecks.Load()
	milestoneChecks.Store(&[]time.Duration{time.Second, 2 * time.Second, 4 * time.Second})
	t.Cleanup(func() { milestoneChecks.Store(prevChecks) })

	const parent = "b7ad6b7169203331"
	mustCreate(t, newTemplate("tpl-trace"))
	createSession(t, "s-trace", "tpl-trace", func(s *v1.IDESession) {
		s.Annotations = map[string]string{v1.AnnotationTraceparent: "00-0af7651916cd43dd8448eb211c80319c-" + parent + "-01"}
	})
	var st v1.IDESessionStatus
	eventually(t, "instance", func() bool { st = sessionStatus("s-trace"); return st.Instance != "" })
	// Reconciles that wait must not leave spans.
	time.Sleep(300 * time.Millisecond)
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "ide-" + st.Instance, Namespace: testNS}}
	if err := get(p); err != nil {
		t.Fatal(err)
	}
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	p.Status.PodIP, p.Status.Phase = "10.0.0.1", corev1.PodRunning
	p.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: now},
		{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: now},
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "ide", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}}}
	if err := k8s.Status().Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ready", func() bool { st = sessionStatus("s-trace"); return st.Phase == v1.SessionReady })

	// Milestones come from the agent with full precision, after the operator saw the session Ready.
	ready := clk.Now()
	agent.set(func(a *fakeAgent) {
		a.milestones = []agentapi.Milestone{ms("claim-received", ready), ms("data-delivered", ready.Add(3200*time.Millisecond)),
			ms("first-request", ready.Add(time.Second)), ms("plugin-host-started", ready.Add(3*time.Second)), ms("workspace-ready", ready.Add(5*time.Second))}
	})
	t.Cleanup(func() { agent.set(func(a *fakeAgent) { a.milestones = nil }) })
	// Only spans of this session: the shared fake agent reports the same milestones for every live session.
	count := func(name string) (n int) {
		for _, s := range rec.Ended() {
			if s.Name() == name && attrsOf(s)["eduide.session"] == "s-trace" {
				n++
			}
		}
		return n
	}
	eventually(t, "ide.usable span", func() bool { return count("ide.usable") == 1 })
	time.Sleep(500 * time.Millisecond) // a further poll must not repeat spans

	allowed := []string{"start", "claim", "instance.create", "pod.scheduled", "image.pulled", "containers.started", "pod.ready", "agent.claim", "status.ready",
		"browser.connect", "data.delivered", "ide.load", "workspace.ready", "ide.usable"}
	counts := map[string]int{}
	var start sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		attrs := attrsOf(s)
		if attrs["eduide.session"] != "s-trace" {
			continue
		}
		counts[s.Name()]++
		if s.Name() == "start" {
			start = s
		}
		for k, v := range attrs {
			for _, leak := range []string{"alice", "git.example.org", "https://"} {
				if strings.Contains(v, leak) {
					t.Errorf("%s attribute %s leaks %q", s.Name(), k, leak)
				}
			}
		}
		if attrs["eduide.template"] != "tpl-trace" || attrs["eduide.strategy"] != "cold" || attrs["eduide.persistence"] != "none" {
			t.Errorf("%s attributes %v", s.Name(), attrs)
		}
		if !slices.Contains(allowed, s.Name()) {
			t.Errorf("unexpected span %s", s.Name())
		}
	}
	if start == nil {
		t.Fatal("no start span")
	}
	if counts["session.reconcile"] != 0 {
		t.Errorf("reconcile spans in the launch trace: %v", counts)
	}
	for _, n := range allowed {
		if want := 1; counts[n] != want && n != "containers.started" && n != "pod.ready" {
			t.Errorf("span %s x%d, want %d (all: %v)", n, counts[n], want, counts)
		}
	}
	if start.Parent().SpanID().String() != parent {
		t.Errorf("start parent %v", start.Parent())
	}
	for _, s := range rec.Ended() {
		if attrsOf(s)["eduide.session"] != "s-trace" {
			continue
		}
		switch s.Name() {
		case "claim", "instance.create", "status.ready", "agent.claim", "pod.scheduled", "image.pulled", "containers.started", "pod.ready":
			if s.Parent().SpanID() != start.SpanContext().SpanID() {
				t.Errorf("%s is not a child of start", s.Name())
			}
		case "browser.connect", "data.delivered", "ide.load", "workspace.ready", "ide.usable":
			if s.Parent().SpanID().String() != parent {
				t.Errorf("%s parent %v", s.Name(), s.Parent())
			}
		}
		if s.Name() != "start" && s.Parent().SpanID() == start.SpanContext().SpanID() {
			if s.StartTime().Before(start.StartTime()) || s.EndTime().After(start.EndTime()) {
				t.Errorf("%s (%v to %v) is outside start (%v to %v)", s.Name(), s.StartTime(), s.EndTime(), start.StartTime(), start.EndTime())
			}
		}
		if s.Name() == "pod.scheduled" && attrsOf(s)["eduide.timestamp_resolution"] != "1s" {
			t.Errorf("pod.scheduled lacks the timestamp resolution: %v", attrsOf(s))
		}
		if s.Name() == "ide.usable" {
			if d := s.EndTime().Sub(s.StartTime()); d != 5*time.Second || !s.StartTime().Equal(ready) {
				t.Errorf("ide.usable lasts %v", d)
			}
		}
	}
}

// TestWarmLaunchTrace checks that a claim records candidates and the chosen pod, and that no cold-start spans appear.
func TestWarmLaunchTrace(t *testing.T) {
	requireEnv(t)
	rec := recordSpans(t)

	mustCreate(t, newTemplate("tpl-wtrace"))
	pool := &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-wt", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-wtrace", MinIdle: 1, TargetIdle: 1, MaxInstances: 10}}
	mustCreate(t, pool)
	sel := map[string]string{v1.LabelPool: "pool-wt"}
	eventually(t, "idle pod", func() bool { return len(podsWith(sel)) == 1 })
	idle := podsWith(sel)[0]
	setReady(t, idle.Name)
	eventually(t, "idle ready", func() bool { _ = get(pool); return pool.Status.IdleReady == 1 })

	createSession(t, "s-wtrace", "tpl-wtrace", func(s *v1.IDESession) {
		s.Annotations = map[string]string{v1.AnnotationTraceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}
	})
	eventually(t, "ready", func() bool { return sessionStatus("s-wtrace").Phase == v1.SessionReady })

	var claimed sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch s.Name() {
		case "claim":
			claimed = s
		case "instance.create", "pod.scheduled", "image.pulled":
			t.Errorf("span %s in a warm launch", s.Name())
		}
	}
	if claimed == nil {
		t.Fatal("no claim span")
	}
	attrs := attrsOf(claimed)
	if attrs["eduide.strategy"] != "warm-ready" || attrs["eduide.pool"] != "pool-wt" || attrs["eduide.claim.result"] != "claimed" || attrs["eduide.claim.candidates"] != "1" {
		t.Errorf("claim attributes %v", attrs)
	}
	if ev := claimed.Events(); len(ev) != 1 || ev[0].Name != "claim.won" {
		t.Errorf("events %v", ev)
	}
}

// TestStartTimeSimulation runs the order of a launch many times with a creation timestamp truncated to the second:
// the start span never begins before the session was created (the end of session.create) and, when the operator
// was not late, begins at the moment it first saw the session.
func TestStartTimeSimulation(t *testing.T) {
	base := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	for i := range 2000 {
		create := base.Add(time.Duration(i*7919%1000) * time.Millisecond)    // the API server commits the session
		createEnd := create.Add(time.Duration(i%5) * time.Millisecond)       // session.create ends after the response
		observed := createEnd.Add(time.Duration(i%300+1) * time.Millisecond) // the operator sees the session later
		stamp := create.Truncate(time.Second)                                // what metav1.Time keeps
		got := startTime(stamp, observed)
		if observed.Sub(stamp) <= time.Second && (got.Before(createEnd) || !got.Equal(observed)) {
			t.Fatalf("start %v before session.create end %v (observed %v, stamp %v)", got, createEnd, observed, stamp)
		}
		if got.Before(stamp) {
			t.Fatalf("start %v before the creation timestamp %v", got, stamp)
		}
	}
	// A late operator falls back to the coarse creation timestamp.
	stamp := base
	if got := startTime(stamp, stamp.Add(5*time.Second)); !got.Equal(stamp) {
		t.Errorf("late observation: %v", got)
	}
}

func TestClampKeepsOrder(t *testing.T) {
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	lo, hi := t0.Add(500*time.Millisecond), t0.Add(3500*time.Millisecond)
	prev := time.Time{}
	for ms := -2000; ms < 6000; ms += 250 {
		got := clamp(t0.Add(time.Duration(ms)*time.Millisecond), lo, hi)
		if got.Before(lo) || got.After(hi) || got.Before(prev) {
			t.Fatalf("clamp(%d ms) = %v", ms, got)
		}
		prev = got
	}
}

// TestMilestonePhasesWithoutTrace checks that the late phases reach the metrics when the session has no traceparent:
// installations without tracing still need ide_usable for the usage export.
func TestMilestonePhasesWithoutTrace(t *testing.T) {
	requireEnv(t)
	recordSpans(t) // also takes the clock back to the wall time
	prevChecks := milestoneChecks.Load()
	milestoneChecks.Store(&[]time.Duration{time.Second, 2 * time.Second, 4 * time.Second})
	t.Cleanup(func() { milestoneChecks.Store(prevChecks) })

	mustCreate(t, newTemplate("tpl-notrace"))
	createSession(t, "s-notrace", "tpl-notrace")
	var st v1.IDESessionStatus
	eventually(t, "instance", func() bool { st = sessionStatus("s-notrace"); return st.Instance != "" })
	setReady(t, "ide-"+st.Instance)
	eventually(t, "ready", func() bool { st = sessionStatus("s-notrace"); return st.Phase == v1.SessionReady })

	usable := telemetry.SessionPhase.WithLabelValues("ide_usable", strategyName(st.StartStrategy), "tpl-notrace")
	before := histCount(usable)
	ready := clk.Now()
	agent.set(func(a *fakeAgent) {
		a.milestones = []agentapi.Milestone{ms("claim-received", ready), ms("workspace-ready", ready.Add(5*time.Second))}
	})
	t.Cleanup(func() { agent.set(func(a *fakeAgent) { a.milestones = nil }) })
	eventually(t, "ide_usable observed", func() bool { return histCount(usable) == before+1 })
	time.Sleep(500 * time.Millisecond) // a further poll must not count it again
	if n := histCount(usable); n != before+1 {
		t.Errorf("ide_usable observed %d times, want once", n-before)
	}
}
