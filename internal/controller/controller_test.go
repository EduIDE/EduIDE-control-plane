package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/claim"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

func TestTemplatePrepullAndValidation(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-prepull")
	mustCreate(t, tm)
	ds := &appsv1.DaemonSet{}
	ds.Name, ds.Namespace = "prepull-tpl-prepull", testNS
	eventually(t, "prepull daemonset", func() bool { return get(ds) == nil })
	init := ds.Spec.Template.Spec.InitContainers
	if len(init) != 2 || init[0].Image != tm.Spec.Image || strings.Join(init[0].Command, " ") != "sh -c true" ||
		init[1].Image != "example.org/agent:1" || strings.Join(init[1].Args, " ") != "noop" {
		t.Errorf("init container: %+v", init)
	}
	if ds.Spec.Template.Spec.Containers[0].Image != "registry.k8s.io/pause:3.10" {
		t.Errorf("pause image: %s", ds.Spec.Template.Spec.Containers[0].Image)
	}
	eventually(t, "template ready", func() bool {
		_ = get(tm)
		return meta.IsStatusConditionTrue(tm.Status.Conditions, v1.ConditionReady)
	})

	bad := newTemplate("tpl-bad")
	bad.Spec.InjectableKeys = []v1.InjectableKey{{Name: "K", Source: v1.SourceRequest, Format: v1.FormatPattern, Pattern: "("}}
	mustCreate(t, bad)
	eventually(t, "template invalid", func() bool {
		_ = get(bad)
		return meta.IsStatusConditionFalse(bad.Status.Conditions, v1.ConditionReady)
	})
}

func TestColdStartCreatesInstance(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-cold"))
	createSession(t, "s-cold", "tpl-cold", func(s *v1.IDESession) {
		s.Annotations = map[string]string{v1.AnnotationTraceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}
	})

	var st v1.IDESessionStatus
	eventually(t, "instance", func() bool { st = sessionStatus("s-cold"); return st.Instance != "" })
	if st.StartStrategy != v1.StartCold || len(st.Instance) != 20 {
		t.Fatalf("status: %+v", st)
	}
	s := &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-cold", Namespace: testNS}}
	_ = get(s)
	if len(s.Finalizers) != 1 || s.Finalizers[0] != v1.SessionFinalizer {
		t.Errorf("finalizers: %v", s.Finalizers)
	}

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "ide-" + st.Instance, Namespace: testNS}}
	if err := get(pod); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		v1.LabelManagedBy: v1.LabelManagedByValue, v1.LabelComponent: v1.ComponentSession, v1.LabelTemplate: "tpl-cold",
		v1.LabelInstance: st.Instance, v1.LabelState: v1.StateClaimed, v1.LabelSession: "s-cold", v1.LabelOwnerHash: v1.OwnerHash("alice"),
	}
	for k, v := range want {
		if pod.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, pod.Labels[k], v)
		}
	}
	if c := metav1.GetControllerOf(pod); c == nil || c.Kind != "IDESession" || c.Name != "s-cold" {
		t.Errorf("controller: %+v", c)
	}
	if len(pod.Spec.Containers) != 2 || pod.Spec.Containers[0].Name != "ide" || pod.Spec.Containers[1].Name != "agent" {
		t.Fatalf("containers: %+v", pod.Spec.Containers)
	}
	prefix, idCookie := "", ""
	for _, e := range pod.Spec.Containers[1].Env {
		switch e.Name {
		case "AGENT_PATH_PREFIX":
			prefix = e.Value
		case "AGENT_ID_TOKEN_COOKIE":
			idCookie = e.Value
		}
	}
	if idCookie != testIDTokenCookie {
		t.Errorf("AGENT_ID_TOKEN_COOKIE = %q, want the configured cookie name %q", idCookie, testIDTokenCookie)
	}
	if prefix != "/"+pod.Labels[v1.LabelInstance]+"/" {
		t.Errorf("AGENT_PATH_PREFIX = %q, want /<instance>/", prefix)
	}
	if *pod.Spec.AutomountServiceAccountToken || *pod.Spec.EnableServiceLinks {
		t.Error("service account token or service links enabled")
	}
	for _, e := range pod.Spec.Containers[0].Env {
		if strings.Contains(e.Value, "git.example.org") {
			t.Error("launch data in the IDE container")
		}
	}

	owned := func(o metav1.Object) bool {
		for _, r := range o.GetOwnerReferences() {
			if r.Kind == "Pod" && r.Name == pod.Name && r.UID == pod.UID {
				return true
			}
		}
		return false
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: testNS}}
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: pod.Name + "-token", Namespace: testNS}}
	route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: testNS}}
	for _, o := range []client.Object{svc, sec, route} {
		eventually(t, "child "+o.GetName(), func() bool { return get(o) == nil })
		if !owned(o) {
			t.Errorf("%T not owned by the pod", o)
		}
		if o.GetLabels()[v1.LabelInstance] != st.Instance || o.GetLabels()[v1.LabelManagedBy] != v1.LabelManagedByValue {
			t.Errorf("%T labels: %v", o, o.GetLabels())
		}
	}
	token := string(sec.Data[v1.InstanceTokenKey])
	if len(token) != 64 {
		t.Errorf("token length %d", len(token))
	}
	if svc.Spec.Ports[0].Port != 8080 || svc.Spec.Ports[0].Name != "http" || svc.Spec.Selector[v1.LabelInstance] != st.Instance {
		t.Errorf("service: %+v", svc.Spec)
	}
	if route.Labels[v1.LabelRoute] != v1.RouteSession {
		t.Errorf("route label: %v", route.Labels)
	}
	rs := route.Spec
	if string(rs.ParentRefs[0].Name) != "gw" || string(*rs.ParentRefs[0].Namespace) != "gateway-system" || string(*rs.ParentRefs[0].SectionName) != "https" || rs.ParentRefs[0].Port == nil || *rs.ParentRefs[0].Port != 443 ||
		rs.Rules[0].Timeouts == nil || rs.Rules[0].Timeouts.Request == nil || *rs.Rules[0].Timeouts.Request != "0s" {
		t.Errorf("parent: %+v", rs.ParentRefs[0])
	}
	if len(rs.Hostnames) != 2 || rs.Hostnames[0] != "ide.example.org" || rs.Hostnames[1] != "webview.ide.example.org" {
		t.Errorf("hostnames: %v", rs.Hostnames)
	}
	rule := rs.Rules[0]
	if *rule.Matches[0].Path.Value != "/"+st.Instance+"/" || *rule.Matches[0].Path.Type != gatewayv1.PathMatchPathPrefix {
		t.Errorf("match: %+v", rule.Matches[0].Path)
	}
	if h := rule.Filters[0].RequestHeaderModifier.Set[0]; string(h.Name) != v1.InstanceTokenHeader || h.Value != token {
		t.Errorf("header filter: %+v", h)
	}
	if p := rule.Filters[1].URLRewrite.Path; p.Type != gatewayv1.PrefixMatchHTTPPathModifier || *p.ReplacePrefixMatch != "/" {
		t.Errorf("rewrite: %+v", p)
	}
	if len(rule.Filters) != 3 || rule.Filters[2].Type != gatewayv1.HTTPRouteFilterResponseHeaderModifier ||
		rule.Filters[2].ResponseHeaderModifier.Set[0].Name != "Strict-Transport-Security" || len(rule.Filters[2].ResponseHeaderModifier.Set) != 1 {
		t.Errorf("response filter: %+v", rule.Filters)
	}
	ide := pod.Spec.Containers[0].SecurityContext
	if ide == nil || ide.AllowPrivilegeEscalation == nil || *ide.AllowPrivilegeEscalation || ide.Capabilities == nil || len(ide.Capabilities.Drop) != 1 || ide.Capabilities.Drop[0] != "ALL" {
		t.Errorf("ide securityContext: %+v", ide)
	}
	if b := rule.BackendRefs[0]; string(b.Name) != pod.Name || *b.Port != 8080 {
		t.Errorf("backend: %+v", b)
	}

	// The pod becomes ready: one claim push with the data, then Ready.
	setReady(t, pod.Name)
	eventually(t, "session ready", func() bool { st = sessionStatus("s-cold"); return st.Phase == v1.SessionReady })
	if st.URL != "https://ide.example.org/"+st.Instance+"/" || st.Timestamps.AgentReady == nil || st.Timestamps.Ready == nil {
		t.Errorf("ready status: %+v", st)
	}
	c := agent.claimFor("s-cold")
	if c == nil || c.Data["GIT_URI"] != "https://git.example.org/r.git" || c.Traceparent == nil {
		t.Fatalf("agent claim: %+v", c)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.tokens[0] != "Bearer "+token {
		t.Error("agent called without the instance token")
	}
}

func TestTemplateNotFound(t *testing.T) {
	requireEnv(t)
	createSession(t, "s-notpl", "does-not-exist")
	eventually(t, "failed", func() bool {
		st := sessionStatus("s-notpl")
		return st.Phase == v1.SessionFailed && st.ErrorCode == v1.ErrorTemplateNotFound
	})
}

func TestPoolFillsAndShrinks(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-pool"))
	pool := &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-a", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-pool", MinIdle: 1, TargetIdle: 3, MaxInstances: 10}}
	mustCreate(t, pool)
	sel := client.MatchingLabels{v1.LabelPool: "pool-a"}
	eventually(t, "3 idle pods", func() bool { return len(podsWith(sel)) == 3 })
	for _, p := range podsWith(sel) {
		if p.Labels[v1.LabelState] != v1.StateIdle || p.Labels[v1.LabelTemplate] != "tpl-pool" {
			t.Errorf("labels: %v", p.Labels)
		}
		if c := metav1.GetControllerOf(&p); c == nil || c.Kind != "IDEPool" {
			t.Errorf("controller: %+v", c)
		}
	}
	eventually(t, "status starting 3", func() bool { _ = get(pool); return pool.Status.IdleStarting == 3 })
	if pool.Status.Replicas != 3 || pool.Status.Selector == "" {
		t.Errorf("status: %+v", pool.Status)
	}

	setReady(t, podsWith(sel)[0].Name)
	setReady(t, podsWith(sel)[1].Name)
	eventually(t, "status ready 2", func() bool { _ = get(pool); return pool.Status.IdleReady == 2 && pool.Status.IdleStarting == 1 })

	// Shrink: the starting pod goes first.
	for {
		_ = get(pool)
		pool.Spec.TargetIdle = 2
		if k8s.Update(context.Background(), pool) == nil {
			break
		}
	}
	eventually(t, "shrunk to 2 ready", func() bool {
		_ = get(pool)
		return pool.Status.IdleReady == 2 && pool.Status.IdleStarting == 0 && len(podsWith(sel)) == 2
	})
}

func TestWarmClaimAndConflict(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-warm"))
	pool := &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-w", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-warm", MinIdle: 1, TargetIdle: 1, MaxInstances: 10}}
	mustCreate(t, pool)
	sel := client.MatchingLabels{v1.LabelPool: "pool-w"}
	eventually(t, "idle pod", func() bool { return len(podsWith(sel)) == 1 })
	idle := podsWith(sel)[0]
	setReady(t, idle.Name)
	eventually(t, "idle ready", func() bool { _ = get(pool); return pool.Status.IdleReady == 1 })

	// Two sessions race for one ready pod: exactly one wins it.
	createSession(t, "s-w1", "tpl-warm")
	createSession(t, "s-w2", "tpl-warm")
	var s1, s2 v1.IDESessionStatus
	eventually(t, "both started", func() bool {
		s1, s2 = sessionStatus("s-w1"), sessionStatus("s-w2")
		return s1.Instance != "" && s2.Instance != ""
	})
	if s1.Instance == s2.Instance {
		t.Fatalf("both sessions got instance %s", s1.Instance)
	}
	warmReady := 0
	for _, s := range []v1.IDESessionStatus{s1, s2} {
		if s.StartStrategy == v1.StartWarmReady {
			warmReady++
		} else if s.StartStrategy != v1.StartCold && s.StartStrategy != v1.StartWarmStarting {
			t.Errorf("strategy %q", s.StartStrategy)
		}
	}
	if warmReady != 1 {
		t.Fatalf("want exactly one warm-ready, got %+v and %+v", s1, s2)
	}
	winner := s1
	if s2.StartStrategy == v1.StartWarmReady {
		winner = s2
	}
	if winner.PodName != idle.Name {
		t.Errorf("winner took %s, want %s", winner.PodName, idle.Name)
	}
	claimed := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: idle.Name, Namespace: testNS}}
	_ = get(claimed)
	if claimed.Labels[v1.LabelState] != v1.StateClaimed || claimed.Labels[v1.LabelPool] != "pool-w" || claimed.Labels[v1.LabelOwnerHash] != v1.OwnerHash("alice") {
		t.Errorf("claimed labels: %v", claimed.Labels)
	}
	if c := metav1.GetControllerOf(claimed); c == nil || c.Kind != "IDESession" || len(claimed.OwnerReferences) != 1 {
		t.Errorf("owners after claim: %+v", claimed.OwnerReferences)
	}
	eventually(t, "winner ready", func() bool {
		name := "s-w1"
		if winner.Instance == s2.Instance {
			name = "s-w2"
		}
		st := sessionStatus(name)
		return st.Phase == v1.SessionReady && st.Timestamps.Claimed != nil
	})
	// The agent gets the launch data once, also when later reconciles read a cache that lags behind.
	time.Sleep(500 * time.Millisecond)
	winnerName := "s-w1"
	if winner.Instance == s2.Instance {
		winnerName = "s-w2"
	}
	if n := agent.claimCount(winnerName); n != 1 {
		t.Errorf("agent claimed %d times, want once", n)
	}
	// The pool refills its idle pod and counts the claimed one.
	// The loser either starts cold or claims the refill pod while it starts.
	fromPool := int32(0)
	for _, s := range []v1.IDESessionStatus{s1, s2} {
		if s.StartStrategy != v1.StartCold {
			fromPool++
		}
	}
	eventually(t, "pool refilled", func() bool {
		_ = get(pool)
		return pool.Status.Claimed == fromPool && pool.Status.IdleStarting+pool.Status.IdleReady >= 1
	})
}

func TestCapacityPressureEvictsAndFails(t *testing.T) {
	requireEnv(t)
	mk := func(name, nodePool string) {
		tm := newTemplate(name)
		tm.Spec.Placement.NodeSelector = map[string]string{nodePoolKey: nodePool}
		mustCreate(t, tm)
		mustCreate(t, &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-" + name, Namespace: testNS},
			Spec: v1.IDEPoolSpec{TemplateRef: name, MinIdle: 2, TargetIdle: 2, MaxInstances: 10}})
	}
	mk("tpl-full", "np-full")
	mk("tpl-other", "np-other")
	full, other := client.MatchingLabels{v1.LabelNodePool: "np-full", v1.LabelState: v1.StateIdle}, client.MatchingLabels{v1.LabelNodePool: "np-other", v1.LabelState: v1.StateIdle}
	eventually(t, "idle pods", func() bool { return len(podsWith(full)) == 2 && len(podsWith(other)) == 2 })

	// A session of a third template in the full node pool: its pod cannot be placed.
	tm := newTemplate("tpl-cap")
	tm.Spec.Placement.NodeSelector = map[string]string{nodePoolKey: "np-full"}
	mustCreate(t, tm)
	createSession(t, "s-cap", "tpl-cap")
	var st v1.IDESessionStatus
	eventually(t, "cold pod", func() bool { st = sessionStatus("s-cap"); return st.PodName != "" })
	evictions := func() float64 { return testutil.ToFloat64(telemetry.CapacityEvictions.WithLabelValues("np-full")) }
	before := evictions()
	setUnschedulable(t, st.PodName)

	eventually(t, "idle pods of the full node pool gone", func() bool { return len(podsWith(full)) == 0 })
	if d := evictions() - before; d != 1 {
		t.Errorf("capacity evictions rose by %v, want 1", d)
	}
	eventually(t, "capacity-exhausted", func() bool {
		st = sessionStatus("s-cap")
		return st.Phase == v1.SessionFailed && st.ErrorCode == v1.ErrorCapacityExhausted
	})
	if !strings.Contains(st.Message, "np-full") {
		t.Errorf("message %q does not name the node pool", st.Message)
	}
	if n := len(podsWith(other)); n != 2 {
		t.Errorf("other node pool has %d idle pods, want 2", n)
	}
	pool := &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-tpl-full", Namespace: testNS}}
	eventually(t, "pool shows pressure", func() bool {
		_ = get(pool)
		return meta.IsStatusConditionTrue(pool.Status.Conditions, v1.ConditionCapacityPressure) || pool.Status.IdleStarting+pool.Status.IdleReady == 0
	})
	if len(podsWith(client.MatchingLabels{v1.LabelSession: "s-cap"})) != 0 {
		t.Error("the failed session keeps its pod")
	}
}

func TestWorkspaceSessionAndExpiry(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-ws")
	size := resource.MustParse("5Gi")
	tm.Spec.Persistence.Size = &size
	mustCreate(t, tm)
	ws := &v1.IDEWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "w-test", Namespace: testNS},
		Spec: v1.IDEWorkspaceSpec{Owner: "alice", TemplateRef: "tpl-ws",
			Persistence: v1.WorkspacePersistence{Mode: v1.WorkspaceVolume}, Retention: &metav1.Duration{Duration: time.Hour}}}
	mustCreate(t, ws)
	eventually(t, "workspace ready", func() bool { _ = get(ws); return ws.Status.Phase == v1.WorkspaceReady })
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "w-test", Namespace: testNS}}
	if err := get(pvc); err != nil {
		t.Fatal(err)
	}
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; q.Cmp(size) != 0 {
		t.Errorf("size %s", q.String())
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast" {
		t.Errorf("storage class: %v", pvc.Spec.StorageClassName)
	}
	if c := metav1.GetControllerOf(pvc); c == nil || c.Kind != "IDEWorkspace" {
		t.Errorf("pvc owner: %+v", c)
	}
	if ws.Status.ClaimName != "w-test" || ws.Status.LastUsed == nil || ws.Status.ExpiresAt == nil ||
		!ws.Status.ExpiresAt.Equal(&metav1.Time{Time: ws.Status.LastUsed.Add(time.Hour)}) {
		t.Errorf("status: %+v", ws.Status)
	}

	// A running session pins the workspace and mounts its volume.
	createSession(t, "s-ws", "tpl-ws", func(s *v1.IDESession) { s.Spec.WorkspaceRef = "w-test" })
	var st v1.IDESessionStatus
	eventually(t, "session pod", func() bool { st = sessionStatus("s-ws"); return st.PodName != "" })
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: st.PodName, Namespace: testNS}}
	_ = get(pod)
	if st.StartStrategy != v1.StartCold || len(pod.Spec.Volumes) == 0 || pod.Spec.Volumes[0].PersistentVolumeClaim == nil || pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "w-test" {
		t.Errorf("session with workspace: %+v %+v", st, pod.Spec.Volumes[0])
	}
	clk.Step(2 * time.Hour)
	defer clk.Step(-2 * time.Hour)
	time.Sleep(500 * time.Millisecond)
	_ = get(ws)
	if ws.Status.Phase != v1.WorkspaceReady {
		t.Fatalf("workspace with a session is %s", ws.Status.Phase)
	}

	// Ending the session sets lastUsed; the expiry runs from there.
	if err := k8s.Delete(context.Background(), &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-ws", Namespace: testNS}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "lastUsed set by the session end", func() bool {
		_ = get(ws)
		return ws.Status.LastUsed.After(time.Now().Add(time.Hour))
	})
	clk.Step(61 * time.Minute)
	defer clk.Step(-61 * time.Minute)
	poke(ws)
	eventually(t, "workspace expired", func() bool {
		_ = get(ws)
		return ws.Status.Phase == v1.WorkspaceExpired
	})
	if ws.Status.ExpiredAt == nil {
		t.Error("expiredAt is not set")
	}
	if !logs.has("workspace expired", "workspace", "w-test") {
		t.Error("the expiry is not logged")
	}

	// The retain annotation cancels the expiry.
	setRetain := func(v string) {
		for {
			_ = get(ws)
			if ws.Annotations == nil {
				ws.Annotations = map[string]string{}
			}
			ws.Annotations[v1.AnnotationRetain] = v
			if k8s.Update(context.Background(), ws) == nil {
				return
			}
		}
	}
	setRetain("true")
	eventually(t, "workspace ready again", func() bool { _ = get(ws); return ws.Status.Phase == v1.WorkspaceReady && ws.Status.ExpiredAt == nil })
	setRetain("false")
	eventually(t, "workspace expired again", func() bool { _ = get(ws); return ws.Status.Phase == v1.WorkspaceExpired })

	// After the grace period the workspace is deleted.
	clk.Step(testGrace + time.Second)
	defer clk.Step(-testGrace - time.Second)
	poke(ws)
	eventually(t, "workspace deleted", func() bool { return notFound(ws) })
	if !logs.has("workspace deleted after its grace period", "workspace", "w-test") {
		t.Error("the deletion is not logged")
	}
}

func TestWorkspaceImport(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-import"))
	mustCreate(t, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "legacy-claim", Namespace: testNS}, Spec: corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}})
	ws := &v1.IDEWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "w-import", Namespace: testNS},
		Spec: v1.IDEWorkspaceSpec{Owner: "bob", TemplateRef: "tpl-import",
			Persistence: v1.WorkspacePersistence{Mode: v1.WorkspaceVolume, ExistingClaimName: "legacy-claim"}}}
	before := time.Now().Add(-time.Second)
	mustCreate(t, ws)
	eventually(t, "import ready", func() bool { _ = get(ws); return ws.Status.Phase == v1.WorkspaceReady })
	if ws.Status.ClaimName != "legacy-claim" || ws.Status.LastUsed.Time.Before(before) {
		t.Errorf("status: %+v", ws.Status)
	}
	// The workspace adopts the imported claim, so the retention deletes it like a created one.
	legacy := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "legacy-claim", Namespace: testNS}}
	eventually(t, "claim adopted", func() bool {
		_ = get(legacy)
		c := metav1.GetControllerOf(legacy)
		return c != nil && c.Kind == "IDEWorkspace" && c.Name == "w-import"
	})
	if err := get(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "w-import", Namespace: testNS}}); err == nil {
		t.Error("an imported workspace must not create a new claim")
	}
}

func TestIdleWarningAndTimeout(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-idle")
	tm.Spec.Timeouts = v1.Timeouts{Idle: &metav1.Duration{Duration: 3 * time.Second}, Warning: &metav1.Duration{Duration: time.Second}}
	mustCreate(t, tm)
	createSession(t, "s-idle", "tpl-idle")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-idle"); return st.PodName != "" })
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus("s-idle").Phase == v1.SessionReady })
	before := agent.noticeCount()
	eventually(t, "warning sent", func() bool { return agent.noticeCount() > before })
	eventually(t, "session ended", func() bool {
		return notFound(&v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-idle", Namespace: testNS}})
	})
	time.Sleep(300 * time.Millisecond)
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if n := len(agent.notices) - before; n != 1 {
		t.Errorf("warning sent %d times, want once", n)
	}
}

// poke changes an annotation so the controller reconciles now: requeue timers do not follow the test clock.
func poke(o client.Object) {
	for {
		if get(o) != nil {
			return
		}
		a := o.GetAnnotations()
		if a == nil {
			a = map[string]string{}
		}
		a["test/poke"] = time.Now().String()
		o.SetAnnotations(a)
		if k8s.Update(context.Background(), o) == nil {
			return
		}
	}
}

func TestFailedSessionIsDeletedAfterRetention(t *testing.T) {
	requireEnv(t)
	createSession(t, "s-gone", "no-such-template")
	s := &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-gone", Namespace: testNS}}
	eventually(t, "failed", func() bool { return sessionStatus("s-gone").Phase == v1.SessionFailed })
	poke(s)
	time.Sleep(300 * time.Millisecond)
	if notFound(s) {
		t.Fatal("the failed session is gone before the landing page could read the error")
	}
	clk.Step(failedRetention + time.Second)
	defer clk.Step(-failedRetention - time.Second)
	poke(s)
	eventually(t, "failed session deleted", func() bool { return notFound(s) })
}

func TestStoppedPodFailsSession(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-stop"))
	createSession(t, "s-stop", "tpl-stop")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-stop"); return st.PodName != "" })
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: st.PodName, Namespace: testNS}}
	if err := get(p); err != nil {
		t.Fatal(err)
	}
	p.Status.Phase = corev1.PodFailed
	if err := k8s.Status().Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	eventually(t, "start-failed", func() bool {
		st = sessionStatus("s-stop")
		return st.Phase == v1.SessionFailed && st.ErrorCode == v1.ErrorStartFailed
	})
}

func TestIdleWarningRearmsAfterActivity(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-rearm")
	tm.Spec.Timeouts = v1.Timeouts{Idle: &metav1.Duration{Duration: 3 * time.Second}, Warning: &metav1.Duration{Duration: time.Second}}
	mustCreate(t, tm)
	createSession(t, "s-rearm", "tpl-rearm")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-rearm"); return st.PodName != "" })
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus("s-rearm").Phase == v1.SessionReady })
	before := agent.noticeCount()
	eventually(t, "first warning", func() bool { return agent.noticeCount() > before })
	defer agent.set(func(a *fakeAgent) { a.activity = nil })
	eventually(t, "warning re-armed", func() bool {
		at := clk.Now()
		agent.set(func(a *fakeAgent) { a.activity = &at })
		return meta.IsStatusConditionFalse(sessionStatus("s-rearm").Conditions, condIdleWarned)
	})
	eventually(t, "second warning", func() bool { return agent.noticeCount() >= before+2 })
	eventually(t, "session ended", func() bool {
		return notFound(&v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-rearm", Namespace: testNS}})
	})
}

func TestUnreachableAgentDoesNotEndIdleSession(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-down")
	tm.Spec.Timeouts = v1.Timeouts{Idle: &metav1.Duration{Duration: 2 * time.Second}, Warning: &metav1.Duration{Duration: time.Second}}
	mustCreate(t, tm)
	createSession(t, "s-down", "tpl-down")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-down"); return st.PodName != "" })
	agent.set(func(a *fakeAgent) { a.down = true })
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus("s-down").Phase == v1.SessionReady })
	time.Sleep(3 * time.Second)
	s := &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-down", Namespace: testNS}}
	if notFound(s) {
		t.Fatal("the session ended on missing activity data")
	}
	agent.set(func(a *fakeAgent) { a.down = false })
	eventually(t, "session ended once the agent answers", func() bool { return notFound(s) })
}

func TestPoolExpectations(t *testing.T) {
	r := &poolReconciler{}
	key := types.NamespacedName{Namespace: testNS, Name: "p"}
	now := time.Now()
	r.expect(key, "a", now)
	r.expect(key, "b", now)
	if n := r.unseen(key, nil, now); n != 2 {
		t.Fatalf("before the cache shows them: %d", n)
	}
	if n := r.unseen(key, []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}, now); n != 1 {
		t.Fatalf("after the cache shows a: %d", n)
	}
	if n := r.unseen(key, nil, now.Add(expectTTL+time.Second)); n != 0 {
		t.Fatalf("after the TTL: %d", n)
	}
}

// The service creates the workspace right before the session, so the operator can see the session first.
func TestSessionWaitsForLateWorkspace(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-late"))
	createSession(t, "s-late", "tpl-late", func(s *v1.IDESession) { s.Spec.WorkspaceRef = "w-late" })
	time.Sleep(time.Second)
	if st := sessionStatus("s-late"); st.Phase == v1.SessionFailed {
		t.Fatalf("session failed before the workspace arrived: %+v", st)
	}
	mustCreate(t, &v1.IDEWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "w-late", Namespace: testNS},
		Spec: v1.IDEWorkspaceSpec{Owner: "alice", TemplateRef: "tpl-late", Persistence: v1.WorkspacePersistence{Mode: v1.WorkspaceVolume}}})
	eventually(t, "session pod with the late workspace", func() bool { return sessionStatus("s-late").PodName != "" })
}

func sessionEvents(name string) map[string]string {
	var l eventsv1.EventList
	_ = k8s.List(context.Background(), &l, client.InNamespace(testNS))
	out := map[string]string{}
	for _, e := range l.Items {
		if e.Regarding.Kind == "IDESession" && e.Regarding.Name == name {
			out[e.Reason] = e.Type + ": " + e.Note
		}
	}
	return out
}

func TestEventsAndLogsOfASession(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-obs"))
	createSession(t, "s-obs", "tpl-obs")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-obs"); return st.PodName != "" })
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus("s-obs").Phase == v1.SessionReady })
	eventually(t, "events", func() bool {
		ev := sessionEvents("s-obs")
		return strings.HasPrefix(ev["Created"], "Normal") && strings.HasPrefix(ev["Ready"], "Normal")
	})
	for _, msg := range []string{"session created", "session ready"} {
		recs := logs.find(msg, "s-obs")
		if len(recs) != 1 {
			t.Fatalf("%q logged %d times", msg, len(recs))
		}
		if u, _ := recs[0]["user"].(string); len(u) != 16 || u == "alice" {
			t.Errorf("%q: user %v is not a pseudonym", msg, recs[0]["user"])
		}
		if recs[0]["template"] != "tpl-obs" {
			t.Errorf("%q: template %v", msg, recs[0]["template"])
		}
	}
	if r := logs.find("session ready", "s-obs")[0]; r["strategy"] != "cold" || r["duration_s"] == nil {
		t.Errorf("ready line: %v", r)
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	for _, m := range logs.recs {
		if strings.Contains(fmt.Sprint(m), "git.example.org") {
			t.Errorf("launch data in a log line: %v", m)
		}
	}
}

func TestFailureEmitsWarningEventAndLog(t *testing.T) {
	requireEnv(t)
	createSession(t, "s-fail-obs", "no-such-template-obs")
	eventually(t, "warning event", func() bool {
		return strings.Contains(sessionEvents("s-fail-obs")["Failed"], "Warning: template-not-found")
	})
	recs := logs.find("session failed", "s-fail-obs")
	if len(recs) != 1 || recs[0]["code"] != v1.ErrorTemplateNotFound || recs[0]["level"] != "WARN" {
		t.Errorf("failure line: %v", recs)
	}
}

func TestIdleEndIsLogged(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-endlog")
	tm.Spec.Timeouts = v1.Timeouts{Idle: &metav1.Duration{Duration: 2 * time.Second}, Warning: &metav1.Duration{Duration: time.Second}}
	mustCreate(t, tm)
	createSession(t, "s-endlog", "tpl-endlog")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-endlog"); return st.PodName != "" })
	setReady(t, st.PodName)
	eventually(t, "ended", func() bool {
		return notFound(&v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-endlog", Namespace: testNS}})
	})
	if recs := logs.find("session ended", "s-endlog"); len(recs) != 1 || recs[0]["reason"] != "idle" {
		t.Errorf("end line: %v", recs)
	}
}

func TestRunningSessionSurvivesTemplateDeletion(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-orphan")
	mustCreate(t, tm)
	createSession(t, "s-orphan", "tpl-orphan")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-orphan"); return st.PodName != "" })
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus("s-orphan").Phase == v1.SessionReady })
	if err := k8s.Delete(context.Background(), tm); err != nil {
		t.Fatal(err)
	}
	s := &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-orphan", Namespace: testNS}}
	for range 3 {
		poke(s)
		time.Sleep(400 * time.Millisecond)
	}
	if st = sessionStatus("s-orphan"); st.Phase != v1.SessionReady {
		t.Fatalf("session ended with its template: %+v", st)
	}
	if len(podsWith(client.MatchingLabels{v1.LabelSession: "s-orphan"})) != 1 {
		t.Error("the pod is gone")
	}
}

func TestImagePullErrorFailsFast(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-pull"))
	createSession(t, "s-pull", "tpl-pull")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-pull"); return st.PodName != "" })
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: st.PodName, Namespace: testNS}}
	if err := get(p); err != nil {
		t.Fatal(err)
	}
	setWaiting := func(msg string) {
		if err := get(p); err != nil {
			t.Fatal(err)
		}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "ide", State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: msg}}}}
		if err := k8s.Status().Update(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	setWaiting(`Back-off pulling image "r.example.org/secret-registry-detail/ide:1": ErrImagePull: manifest unknown`)
	time.Sleep(500 * time.Millisecond)
	if st = sessionStatus("s-pull"); st.Phase == v1.SessionFailed {
		t.Fatalf("failed before the grace period: %+v", st)
	}
	clk.Step(stuckLimit + time.Second)
	defer clk.Step(-stuckLimit - time.Second)
	// A pull that can heal, such as a timeout of a large image, is not given up after the grace period.
	setWaiting(`Back-off pulling image "r.example.org/ide:1": ErrImagePull: dial tcp: i/o timeout`)
	time.Sleep(500 * time.Millisecond)
	if st = sessionStatus("s-pull"); st.Phase == v1.SessionFailed {
		t.Fatalf("a transient pull error failed the session: %+v", st)
	}
	setWaiting(`Back-off pulling image "r.example.org/secret-registry-detail/ide:1": ErrImagePull: manifest unknown`)
	eventually(t, "start-failed", func() bool { st = sessionStatus("s-pull"); return st.Phase == v1.SessionFailed })
	if st.ErrorCode != v1.ErrorStartFailed || !strings.Contains(st.Message, "ImagePullBackOff") || strings.Contains(st.Message, "secret-registry-detail") {
		t.Errorf("status: %+v", st)
	}
}

func TestSecondSessionOnABusyWorkspaceFailsAtOnce(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-busy"))
	mustCreate(t, &v1.IDEWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "w-busy", Namespace: testNS},
		Spec: v1.IDEWorkspaceSpec{Owner: "alice", TemplateRef: "tpl-busy", Persistence: v1.WorkspacePersistence{Mode: v1.WorkspaceVolume}}})
	createSession(t, "s-busy1", "tpl-busy", func(s *v1.IDESession) { s.Spec.WorkspaceRef = "w-busy" })
	eventually(t, "first session has a pod", func() bool { return sessionStatus("s-busy1").PodName != "" })
	createSession(t, "s-busy2", "tpl-busy", func(s *v1.IDESession) { s.Spec.WorkspaceRef = "w-busy" })
	var st v1.IDESessionStatus
	eventually(t, "workspace-in-use", func() bool { st = sessionStatus("s-busy2"); return st.Phase == v1.SessionFailed })
	if st.ErrorCode != v1.ErrorWorkspaceInUse || st.PodName != "" {
		t.Errorf("status: %+v", st)
	}
	if p := sessionStatus("s-busy1").Phase; p == v1.SessionFailed {
		t.Error("the running session was disturbed")
	}
	// Further reconciles of the failed session must not count or emit the failure again.
	time.Sleep(time.Second)
	if n := testutil.ToFloat64(telemetry.SessionFailures.WithLabelValues("tpl-busy", v1.ErrorWorkspaceInUse)); n != 1 {
		t.Errorf("failures counted %v times, want once", n)
	}
}

func TestAgentConflictReleasesThePod(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-409"))
	pool := &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-409", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-409", MinIdle: 1, TargetIdle: 1, MaxInstances: 10}}
	mustCreate(t, pool)
	sel := client.MatchingLabels{v1.LabelPool: "pool-409"}
	eventually(t, "idle pod", func() bool { return len(podsWith(sel)) == 1 })
	idle := podsWith(sel)[0]
	setReady(t, idle.Name)
	eventually(t, "idle ready", func() bool { _ = get(pool); return pool.Status.IdleReady == 1 })

	agent.set(func(a *fakeAgent) { a.conflicts = 1 })
	createSession(t, "s-409", "tpl-409")
	eventually(t, "claimed pod released", func() bool {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: idle.Name, Namespace: testNS}}
		return notFound(p) || p.DeletionTimestamp != nil
	})
	var st v1.IDESessionStatus
	eventually(t, "session continues on another pod", func() bool {
		st = sessionStatus("s-409")
		return st.PodName != "" && st.PodName != idle.Name
	})
	if st.Phase == v1.SessionFailed {
		t.Fatalf("session failed: %+v", st)
	}
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus("s-409").Phase == v1.SessionReady })
}

func TestMissingDataSecretMessage(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-nosecret"))
	mustCreate(t, newSession("s-nosecret", "tpl-nosecret", "alice"))
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-nosecret"); return st.PodName != "" })
	setReady(t, st.PodName)
	time.Sleep(500 * time.Millisecond)
	clk.Step(agentWaitLimit + time.Minute)
	defer clk.Step(-agentWaitLimit - time.Minute)
	poke(&v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-nosecret", Namespace: testNS}})
	eventually(t, "failed", func() bool { st = sessionStatus("s-nosecret"); return st.Phase == v1.SessionFailed })
	want := "launch data Secret s-nosecret-data missing or not labelled app.kubernetes.io/managed-by=eduide"
	if st.Message != want {
		t.Errorf("message %q, want %q", st.Message, want)
	}
}

func TestAgentRestartIsReclaimed(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-reclaim"))
	createSession(t, "s-reclaim", "tpl-reclaim")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-reclaim"); return st.PodName != "" })
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus("s-reclaim").Phase == v1.SessionReady })
	if n := agent.claimCount("s-reclaim"); n != 1 {
		t.Fatalf("claims before the restart: %d", n)
	}
	agent.set(func(a *fakeAgent) { a.unclaimed = true })
	defer agent.set(func(a *fakeAgent) { a.unclaimed = false })
	eventually(t, "second claim", func() bool { return agent.claimCount("s-reclaim") >= 2 })
	if c := agent.claimFor("s-reclaim"); c == nil || c.Data["GIT_URI"] != "https://git.example.org/r.git" {
		t.Errorf("claim: %+v", c)
	}
	eventually(t, "reclaim event", func() bool { return strings.Contains(sessionEvents("s-reclaim")["Reclaimed"], "Normal") })
}

func TestAllowlistIsEnforcedBeforeTheClaim(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-allow")
	tm.Spec.InjectableKeys = []v1.InjectableKey{{Name: "GIT_URI", Format: v1.FormatPattern, Pattern: ".*", MaxLength: 12}}
	mustCreate(t, tm)
	mustCreate(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s-allow-data", Namespace: testNS, Labels: map[string]string{v1.LabelManagedBy: v1.LabelManagedByValue}},
		StringData: map[string]string{"GIT_URI": "https://git.example.org/r.git", "EVIL": "top-secret-value"},
	})
	mustCreate(t, newSession("s-allow", "tpl-allow", "alice"))
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-allow"); return st.PodName != "" })
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus("s-allow").Phase == v1.SessionReady })
	c := agent.claimFor("s-allow")
	if c == nil || len(c.Data) != 1 || c.Data["GIT_URI"] != "https://git." {
		t.Fatalf("claim data: %+v", c)
	}
	recs := logs.find("launch data does not match the template allowlist", "s-allow")
	if len(recs) == 0 || fmt.Sprint(recs[0]["dropped_keys"]) != "[EVIL]" || strings.Contains(fmt.Sprint(recs[0]), "top-secret-value") {
		t.Errorf("log: %v", recs)
	}
}

func poolPods(pool string) []corev1.Pod { return podsWith(client.MatchingLabels{v1.LabelPool: pool}) }

func TestTemplateChangeRollsIdlePods(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-roll")
	mustCreate(t, tm)
	mustCreate(t, &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-roll", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-roll", MinIdle: 2, TargetIdle: 2, MaxInstances: 10}})
	eventually(t, "2 idle pods", func() bool { return len(poolPods("pool-roll")) == 2 })
	for _, p := range poolPods("pool-roll") {
		setReady(t, p.Name)
	}
	old := map[string]bool{}
	for _, p := range poolPods("pool-roll") {
		old[p.Name] = true
		if p.Annotations[v1.AnnotationSpecHash] == "" {
			t.Fatal("pod has no spec hash")
		}
	}
	for {
		_ = get(tm)
		tm.Spec.Image = "example.org/ide:2"
		if k8s.Update(context.Background(), tm) == nil {
			break
		}
	}
	// A replacement starts above the target; no old pod goes before a replacement is ready.
	var surge string
	eventually(t, "a replacement exists", func() bool {
		for _, p := range poolPods("pool-roll") {
			if !old[p.Name] {
				surge = p.Name
				return true
			}
		}
		return false
	})
	time.Sleep(time.Second)
	readyIdle := func(ps []corev1.Pod) (n int) {
		for _, p := range ps {
			if claim.Ready(&p) {
				n++
			}
		}
		return n
	}
	if ps := poolPods("pool-roll"); readyIdle(ps) != 2 || len(ps) != 3 {
		t.Fatalf("before the replacement is ready: %d ready of %d", readyIdle(ps), len(ps))
	}
	setReady(t, surge)
	eventually(t, "all pods replaced", func() bool {
		ps := poolPods("pool-roll")
		if n := readyIdle(ps); n < 2 {
			t.Errorf("only %d ready idle pods during the roll", n)
		}
		done := len(ps) == 2
		for _, p := range ps {
			if old[p.Name] {
				done = false
				continue
			}
			if p.Spec.Containers[0].Image != "example.org/ide:2" {
				t.Errorf("image %s", p.Spec.Containers[0].Image)
			}
			if !claim.Ready(&p) {
				setReady(t, p.Name)
			}
		}
		return done
	})
}

func TestClaimedPodIsNotRolled(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-keep")
	mustCreate(t, tm)
	mustCreate(t, &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-keep", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-keep", MinIdle: 1, TargetIdle: 1, MaxInstances: 10}})
	eventually(t, "idle", func() bool { return len(poolPods("pool-keep")) == 1 })
	setReady(t, poolPods("pool-keep")[0].Name)
	createSession(t, "s-keep", "tpl-keep")
	var st v1.IDESessionStatus
	eventually(t, "claimed", func() bool { st = sessionStatus("s-keep"); return st.PodName != "" })
	for {
		_ = get(tm)
		tm.Spec.Image = "example.org/ide:3"
		if k8s.Update(context.Background(), tm) == nil {
			break
		}
	}
	time.Sleep(2 * time.Second)
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: st.PodName, Namespace: testNS}}
	if err := get(p); err != nil || p.DeletionTimestamp != nil {
		t.Fatalf("claimed pod touched: %v", err)
	}
}

func TestOrphanedClaimIsDeleted(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-orphan2"))
	mustCreate(t, &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-orphan", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-orphan2", MinIdle: 1, TargetIdle: 1, MaxInstances: 10}})
	eventually(t, "idle", func() bool { return len(poolPods("pool-orphan")) == 1 })
	p := poolPods("pool-orphan")[0]
	for {
		_ = get(&p)
		p.Labels[v1.LabelState], p.Labels[v1.LabelSession] = v1.StateClaimed, "ghost"
		if k8s.Update(context.Background(), &p) == nil {
			break
		}
	}
	time.Sleep(time.Second)
	if p.DeletionTimestamp != nil || notFound(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: testNS}}) {
		t.Fatal("deleted before the grace period")
	}
	clk.Step(orphanGrace + time.Second)
	defer clk.Step(-orphanGrace - time.Second)
	poke(&v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-orphan", Namespace: testNS}})
	eventually(t, "orphan deleted", func() bool {
		q := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: testNS}}
		return notFound(q) || get(q) == nil && q.DeletionTimestamp != nil
	})
}

func TestBrokenIdlePodsAreReplaced(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-broken"))
	mustCreate(t, &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-broken", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-broken", MinIdle: 2, TargetIdle: 2, MaxInstances: 10}})
	eventually(t, "2 idle", func() bool { return len(poolPods("pool-broken")) == 2 })
	pods := poolPods("pool-broken")
	setStatus := func(name string, cs corev1.ContainerStatus) {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}}
		if err := get(p); err != nil {
			t.Fatal(err)
		}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{cs}
		if err := k8s.Status().Update(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	// A restarted container: replaced at once.
	setStatus(pods[0].Name, corev1.ContainerStatus{Name: "agent", RestartCount: 1})
	eventually(t, "restarted pod replaced", func() bool {
		ps := poolPods("pool-broken")
		for _, p := range ps {
			if p.Name == pods[0].Name {
				return false
			}
		}
		return len(ps) == 2
	})
	// A pull error: replaced after the grace period, and a claim skips it meanwhile. A pull that can heal stays.
	pull := func(msg string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: "ide", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: msg}}}
	}
	setStatus(pods[1].Name, pull("429 Too Many Requests"))
	clk.Step(stuckLimit + time.Second)
	defer clk.Step(-stuckLimit - time.Second)
	poke(&v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-broken", Namespace: testNS}})
	time.Sleep(500 * time.Millisecond)
	if notFound(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: pods[1].Name, Namespace: testNS}}) {
		t.Fatal("a pod with a pull error that can heal was replaced")
	}
	setStatus(pods[1].Name, pull("failed to resolve reference: not found"))
	poke(&v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-broken", Namespace: testNS}})
	eventually(t, "pull error pod replaced", func() bool {
		for _, p := range poolPods("pool-broken") {
			if p.Name == pods[1].Name {
				return false
			}
		}
		return true
	})
}

func TestIdleInstanceIsRepaired(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-repair"))
	mustCreate(t, &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-repair", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-repair", MinIdle: 1, TargetIdle: 1, MaxInstances: 10}})
	eventually(t, "idle", func() bool { return len(poolPods("pool-repair")) == 1 })
	p := poolPods("pool-repair")[0]
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: testNS}}
	route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: testNS}}
	eventually(t, "children", func() bool { return get(svc) == nil && get(route) == nil })
	if err := k8s.Delete(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Delete(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	poke(&v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-repair", Namespace: testNS}})
	eventually(t, "service and route recreated", func() bool { return get(svc) == nil && get(route) == nil })

	// Without its token Secret the pod is discarded and replaced.
	if err := k8s.Delete(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: p.Name + "-token", Namespace: testNS}}); err != nil {
		t.Fatal(err)
	}
	poke(&v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-repair", Namespace: testNS}})
	eventually(t, "pod without token replaced", func() bool {
		ps := poolPods("pool-repair")
		return len(ps) == 1 && ps[0].Name != p.Name
	})
}

func TestSessionFailsWhenTheTokenSecretIsGone(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-notoken"))
	createSession(t, "s-notoken", "tpl-notoken")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-notoken"); return st.PodName != "" })
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: st.PodName + "-token", Namespace: testNS}}
	eventually(t, "token", func() bool { return get(sec) == nil })
	if err := k8s.Delete(context.Background(), sec); err != nil {
		t.Fatal(err)
	}
	poke(&v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-notoken", Namespace: testNS}})
	eventually(t, "failed", func() bool { st = sessionStatus("s-notoken"); return st.Phase == v1.SessionFailed })
	if st.ErrorCode != v1.ErrorStartFailed || !strings.Contains(st.Message, "token Secret") {
		t.Errorf("status: %+v", st)
	}
}

// A create that failed after the pod leaves it without children; a pod that has not started gets new ones.
func TestHalfCreatedInstanceIsCompleted(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-half"))
	createSession(t, "s-half", "tpl-half")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-half"); return st.PodName != "" })
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: st.PodName + "-token", Namespace: testNS}}
	route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: st.PodName, Namespace: testNS}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: st.PodName, Namespace: testNS}}
	eventually(t, "children", func() bool { return get(sec) == nil && get(route) == nil && get(svc) == nil })
	for _, o := range []client.Object{route, svc, sec} {
		if err := k8s.Delete(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "children gone", func() bool { return notFound(sec) && notFound(route) })
	poke(&v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-half", Namespace: testNS}})
	eventually(t, "children recreated", func() bool { return get(sec) == nil && get(route) == nil && get(svc) == nil })
	if st = sessionStatus("s-half"); st.Phase == v1.SessionFailed {
		t.Fatalf("session failed: %+v", st)
	}
}

func TestOnlyTheOldestPoolOfATemplateIsActive(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-dup"))
	mk := func(name string) *v1.IDEPool {
		p := &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
			Spec: v1.IDEPoolSpec{TemplateRef: "tpl-dup", MinIdle: 1, TargetIdle: 1, MaxInstances: 10}}
		mustCreate(t, p)
		return p
	}
	first := mk("pool-dup-a")
	eventually(t, "first pool fills", func() bool { return len(poolPods("pool-dup-a")) == 1 })
	time.Sleep(1100 * time.Millisecond) // creation timestamps have second resolution
	second := mk("pool-dup-b")
	eventually(t, "duplicate condition", func() bool {
		_ = get(second)
		c := meta.FindStatusCondition(second.Status.Conditions, v1.ConditionReady)
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "DuplicateTemplate"
	})
	if n := len(poolPods("pool-dup-b")); n != 0 {
		t.Errorf("duplicate pool keeps %d pods", n)
	}
	// When the older pool goes, the other one takes over.
	if err := k8s.Delete(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second pool takes over", func() bool {
		_ = get(second)
		return meta.IsStatusConditionTrue(second.Status.Conditions, v1.ConditionReady) && len(poolPods("pool-dup-b")) == 1
	})
}

func TestEmptyClaimCountsOnlyForTemplatesWithAPool(t *testing.T) {
	requireEnv(t)
	count := func(tpl string) float64 {
		return testutil.ToFloat64(telemetry.ClaimTotal.WithLabelValues(tpl, "empty"))
	}
	mustCreate(t, newTemplate("tpl-nopool"))
	createSession(t, "s-nopool", "tpl-nopool")
	eventually(t, "cold pod", func() bool { return sessionStatus("s-nopool").PodName != "" })
	if n := count("tpl-nopool"); n != 0 {
		t.Errorf("template without a pool counted %v empty claims", n)
	}
	mustCreate(t, newTemplate("tpl-emptypool"))
	mustCreate(t, &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-emptypool", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-emptypool", MinIdle: 0, TargetIdle: 0, MaxInstances: 5}})
	eventually(t, "pool exists in the cache", func() bool {
		return get(&v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-emptypool", Namespace: testNS}}) == nil
	})
	time.Sleep(300 * time.Millisecond)
	createSession(t, "s-emptypool", "tpl-emptypool")
	eventually(t, "empty claim counted", func() bool { return count("tpl-emptypool") >= 1 })
}

func TestPressureMayEvict(t *testing.T) {
	c := &offsetClock{}
	p := newPressure(c)
	if !p.mayEvict("np", "a") {
		t.Fatal("first eviction must be allowed")
	}
	p.evictedPod("np", "a")
	if p.mayEvict("np", "a") || p.mayEvict("np", "b") {
		t.Error("a second eviction inside the window must wait")
	}
	if !p.mayEvict("other", "x") {
		t.Error("another node pool is not affected")
	}
	c.Step(evictWindow)
	if p.mayEvict("np", "a") || !p.mayEvict("np", "b") {
		t.Error("after the window only a different pod may go")
	}
}

func TestWorkspaceVolumeLost(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-lost"))
	ws := &v1.IDEWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "w-lost", Namespace: testNS},
		Spec: v1.IDEWorkspaceSpec{Owner: "carol", TemplateRef: "tpl-lost", Persistence: v1.WorkspacePersistence{Mode: v1.WorkspaceVolume}}}
	mustCreate(t, ws)
	eventually(t, "workspace ready", func() bool { _ = get(ws); return ws.Status.Phase == v1.WorkspaceReady })
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "w-lost", Namespace: testNS}}
	if err := k8s.Delete(context.Background(), pvc); err != nil {
		t.Fatal(err)
	}
	// envtest has no PVC protection controller, so drop its finalizer.
	_ = get(pvc)
	pvc.Finalizers = nil
	_ = k8s.Update(context.Background(), pvc)
	eventually(t, "volume lost", func() bool { _ = get(ws); return ws.VolumeLost() })
	if ws.Status.Phase != v1.WorkspaceReady {
		t.Errorf("phase changed to %s", ws.Status.Phase)
	}
	time.Sleep(500 * time.Millisecond)
	if err := get(pvc); err == nil {
		t.Fatal("the operator recreated an empty claim")
	}
	if !logs.has("workspace volume lost", "workspace", "w-lost") {
		t.Error("the loss is not logged")
	}
	createSession(t, "s-lost", "tpl-lost", func(s *v1.IDESession) { s.Spec.WorkspaceRef = "w-lost" })
	eventually(t, "session fails", func() bool { return sessionStatus("s-lost").ErrorCode == v1.ErrorWorkspaceLost })
}

// The service reuses the session name for the same launch key. A relaunch right after a stop meets the pod of
// the stopped session, which still terminates and carries the same session label: it is not the new instance.
func TestRelaunchWhileOldPodTerminates(t *testing.T) {
	requireEnv(t)
	ctx := context.Background()
	mustCreate(t, newTemplate("tpl-relaunch"))
	const name = "s-relaunch"
	createSession(t, name, "tpl-relaunch")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus(name); return st.PodName != "" })
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus(name).Phase == v1.SessionReady })

	// envtest has no kubelet: a finalizer holds the old pod in Terminating, as the grace period does.
	old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: st.PodName, Namespace: testNS}}
	if err := get(old); err != nil {
		t.Fatal(err)
	}
	old.Finalizers = append(old.Finalizers, "test.example.org/terminating")
	if err := k8s.Update(ctx, old); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if get(old) == nil {
			old.Finalizers = nil
			_ = k8s.Update(ctx, old)
		}
	})
	s := &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}}
	if err := k8s.Delete(ctx, s); err != nil {
		t.Fatal(err)
	}
	eventually(t, "session gone", func() bool { return notFound(s) })
	_ = k8s.Delete(ctx, old) // envtest has no garbage collector either
	_ = k8s.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-data", Namespace: testNS}})

	createSession(t, name, "tpl-relaunch")
	eventually(t, "new pod", func() bool {
		st = sessionStatus(name)
		return st.PodName != "" && st.PodName != old.Name
	})
	setReady(t, st.PodName)
	eventually(t, "ready again", func() bool {
		st = sessionStatus(name)
		if st.Phase == v1.SessionFailed {
			t.Fatalf("the relaunch failed: %s %q", st.ErrorCode, st.Message)
		}
		return st.Phase == v1.SessionReady
	})
	// The old pod stays around for a while; the session must keep its own instance.
	time.Sleep(time.Second)
	if st = sessionStatus(name); st.Phase != v1.SessionReady || st.PodName == old.Name {
		t.Fatalf("phase %s on pod %s (old pod %s): %q", st.Phase, st.PodName, old.Name, st.Message)
	}
}

// An evicted pod keeps its object in phase Failed. The session must not stay Ready with a dead URL.
func TestReadySessionFailsWhenPodFails(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-evict"))
	createSession(t, "s-evict", "tpl-evict")
	var st v1.IDESessionStatus
	eventually(t, "pod", func() bool { st = sessionStatus("s-evict"); return st.PodName != "" })
	setReady(t, st.PodName)
	eventually(t, "ready", func() bool { return sessionStatus("s-evict").Phase == v1.SessionReady })

	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: st.PodName, Namespace: testNS}}
	if err := get(p); err != nil {
		t.Fatal(err)
	}
	p.Status.Phase, p.Status.Reason = corev1.PodFailed, "Evicted"
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.Now()}}
	if err := k8s.Status().Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	eventually(t, "start-failed", func() bool {
		st = sessionStatus("s-evict")
		return st.Phase == v1.SessionFailed && st.ErrorCode == v1.ErrorStartFailed
	})
}

// A pod that fits no node for a reason other than room (here: the node affinity of its volume) fails its own
// session, but it must not put the node pool under pressure: that would delete the idle pods of every pool there.
func TestUnschedulableWithoutCapacityReasonKeepsThePools(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-nopressure")
	tm.Spec.Placement.NodeSelector = map[string]string{nodePoolKey: "np-nopressure"}
	mustCreate(t, tm)
	mustCreate(t, &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-tpl-nopressure", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-nopressure", MinIdle: 2, TargetIdle: 2, MaxInstances: 10}})
	idle := client.MatchingLabels{v1.LabelNodePool: "np-nopressure", v1.LabelState: v1.StateIdle}
	eventually(t, "idle pods", func() bool { return len(podsWith(idle)) == 2 })
	names := func() map[string]bool {
		m := map[string]bool{}
		for _, p := range podsWith(idle) {
			m[p.Name] = true
		}
		return m
	}
	before := names()

	other := newTemplate("tpl-nofit")
	other.Spec.Placement.NodeSelector = map[string]string{nodePoolKey: "np-nopressure"}
	mustCreate(t, other)
	createSession(t, "s-nofit", "tpl-nofit")
	var st v1.IDESessionStatus
	eventually(t, "cold pod", func() bool { st = sessionStatus("s-nofit"); return st.PodName != "" })
	evictions := testutil.ToFloat64(telemetry.CapacityEvictions.WithLabelValues("np-nopressure"))
	setUnschedulableBecause(t, st.PodName, "0/3 nodes are available: 3 node(s) had volume node affinity conflict.")

	eventually(t, "start-failed", func() bool {
		st = sessionStatus("s-nofit")
		return st.Phase == v1.SessionFailed
	})
	if st.ErrorCode != v1.ErrorStartFailed {
		t.Errorf("error code %s, want %s: the cluster is not full", st.ErrorCode, v1.ErrorStartFailed)
	}
	if d := testutil.ToFloat64(telemetry.CapacityEvictions.WithLabelValues("np-nopressure")) - evictions; d != 0 {
		t.Errorf("%v idle pods were evicted", d)
	}
	after := names()
	for n := range before {
		if !after[n] {
			t.Errorf("idle pod %s of the pool is gone", n)
		}
	}
}

// The cache can be behind a claim the operator created a moment ago: that is not a lost volume.
func TestEnsureVolumeAsksTheAPIServerBeforeTheVolumeIsLost(t *testing.T) {
	ws := &v1.IDEWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "w-stale", Namespace: "ns"}, Status: v1.IDEWorkspaceStatus{ClaimName: "w-stale"}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "w-stale", Namespace: "ns"}}
	empty := func() client.WithWatch { return fake.NewClientBuilder().WithScheme(NewScheme()).Build() }
	live := fake.NewClientBuilder().WithScheme(NewScheme()).WithObjects(pvc).Build()

	r := &workspaceReconciler{&deps{Client: empty(), reader: live}}
	if name, msg, err := r.ensureVolume(context.Background(), ws, newTemplate("x")); err != nil || name != "w-stale" {
		t.Fatalf("stale cache: name %q, message %q, err %v; want the existing claim", name, msg, err)
	}
	r = &workspaceReconciler{&deps{Client: empty(), reader: empty()}}
	if name, msg, err := r.ensureVolume(context.Background(), ws, newTemplate("x")); err != nil || name != "" || msg == "" {
		t.Fatalf("claim really gone: name %q, message %q, err %v; want no claim and a reason", name, msg, err)
	}
}

// An imported claim belongs to one workspace. A second workspace that names it must not get the volume.
func TestImportedClaimIsNotShared(t *testing.T) {
	owner := &v1.IDEWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "w-owner", Namespace: "ns", UID: "uid-owner"}}
	yes := true
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "ns", OwnerReferences: []metav1.OwnerReference{
		{APIVersion: v1.GroupVersion.String(), Kind: "IDEWorkspace", Name: owner.Name, UID: owner.UID, Controller: &yes}}}}
	c := fake.NewClientBuilder().WithScheme(NewScheme()).WithObjects(pvc).Build()
	r := &workspaceReconciler{&deps{Client: c, reader: c, scheme: NewScheme()}}
	imports := func(name string, uid types.UID) *v1.IDEWorkspace {
		return &v1.IDEWorkspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: uid},
			Spec: v1.IDEWorkspaceSpec{Persistence: v1.WorkspacePersistence{ExistingClaimName: "data"}}}
	}
	if name, msg, err := r.ensureVolume(context.Background(), imports("w-other", "uid-other"), newTemplate("x")); err != nil || name != "" || !strings.Contains(msg, "w-owner") {
		t.Fatalf("second workspace: name %q, message %q, err %v; want no claim and the owner in the message", name, msg, err)
	}
	if name, _, err := r.ensureVolume(context.Background(), imports("w-owner", "uid-owner"), newTemplate("x")); err != nil || name != "data" {
		t.Fatalf("the owner itself: name %q, err %v", name, err)
	}
}

// histCount is the number of observations of one histogram series.
func histCount(o prometheus.Observer) uint64 {
	var m dto.Metric
	_ = o.(prometheus.Metric).Write(&m)
	return m.GetHistogram().GetSampleCount()
}

func TestStartModeLabel(t *testing.T) {
	requireEnv(t)
	mustCreate(t, newTemplate("tpl-sm-lazy"))
	mustCreate(t, newTemplate("tpl-sm-fallback"))
	mustCreate(t, &v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-sm-fallback", Namespace: testNS},
		Spec: v1.IDEPoolSpec{TemplateRef: "tpl-sm-fallback", MinIdle: 0, TargetIdle: 0, MaxInstances: 5}})
	eventually(t, "pool in the cache", func() bool {
		return get(&v1.IDEPool{ObjectMeta: metav1.ObjectMeta{Name: "pool-sm-fallback", Namespace: testNS}}) == nil
	})
	time.Sleep(300 * time.Millisecond)
	for _, c := range []struct{ tpl, mode string }{{"tpl-sm-lazy", "lazy"}, {"tpl-sm-fallback", "lazy_fallback"}} {
		name := "s-" + strings.ReplaceAll(c.mode, "_", "-")
		createSession(t, name, c.tpl)
		var st v1.IDESessionStatus
		eventually(t, "pod", func() bool { st = sessionStatus(name); return st.PodName != "" })
		setReady(t, st.PodName)
		eventually(t, "ready", func() bool { return sessionStatus(name).Phase == v1.SessionReady })
		eventually(t, c.mode+" observed", func() bool {
			return histCount(telemetry.SessionStartup.WithLabelValues(c.tpl, "cold", "ready", "none", c.mode)) == 1
		})
	}
}

func TestSessionLifetimeReasons(t *testing.T) {
	requireEnv(t)
	count := func(tpl, reason string) uint64 {
		return histCount(telemetry.SessionLifetime.WithLabelValues(tpl, reason))
	}

	// error: the session fails and is deleted after the retention.
	createSession(t, "s-life-err", "tpl-life-missing")
	s := &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-life-err", Namespace: testNS}}
	eventually(t, "failed", func() bool { return sessionStatus("s-life-err").Phase == v1.SessionFailed })
	clk.Step(failedRetention + time.Second)
	poke(s)
	eventually(t, "error lifetime", func() bool { return count("tpl-life-missing", "error") == 1 })
	clk.Step(-failedRetention - time.Second)

	// user: deleted from outside.
	mustCreate(t, newTemplate("tpl-life"))
	createSession(t, "s-life-user", "tpl-life")
	eventually(t, "pod", func() bool { return sessionStatus("s-life-user").PodName != "" })
	u := &v1.IDESession{ObjectMeta: metav1.ObjectMeta{Name: "s-life-user", Namespace: testNS}}
	_ = get(u)
	if err := k8s.Delete(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	eventually(t, "user lifetime", func() bool { return count("tpl-life", "user") == 1 })
	time.Sleep(300 * time.Millisecond)
	if n := count("tpl-life", "user"); n != 1 {
		t.Errorf("lifetime observed %d times, want once", n)
	}
}

func TestTemplateResourcesGauge(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-res")
	tm.Spec.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
	}
	mustCreate(t, tm)
	g := func(res, kind string) float64 {
		return testutil.ToFloat64(telemetry.TemplateResources.WithLabelValues("tpl-res", res, kind))
	}
	eventually(t, "gauge set", func() bool { return g("cpu", "request") == 0.5 })
	if g("memory", "request") != 1<<30 || g("memory", "limit") != 2<<30 {
		t.Errorf("memory request %v limit %v", g("memory", "request"), g("memory", "limit"))
	}
	tg := func(kind string) float64 {
		return testutil.ToFloat64(telemetry.TemplateTimeout.WithLabelValues("tpl-res", kind))
	}
	if tg("idle") != v1.DefaultIdleTimeout.Seconds() || tg("warning") != v1.DefaultWarningTimeout.Seconds() || tg("max") != v1.DefaultMaxTimeout.Seconds() {
		t.Errorf("timeouts idle %v warning %v max %v", tg("idle"), tg("warning"), tg("max"))
	}
	if err := k8s.Delete(context.Background(), tm); err != nil {
		t.Fatal(err)
	}
	eventually(t, "series removed", func() bool {
		return !hasSeries("tpl-res") && !hasTimeoutSeries("tpl-res")
	})
}

func hasSeries(tpl string) bool {
	ch := make(chan prometheus.Metric, 64)
	go func() { telemetry.TemplateResources.Collect(ch); close(ch) }()
	found := false
	for m := range ch {
		var d dto.Metric
		_ = m.Write(&d)
		for _, l := range d.GetLabel() {
			if l.GetName() == "template" && l.GetValue() == tpl {
				found = true
			}
		}
	}
	return found
}

func hasTimeoutSeries(tpl string) bool {
	ch := make(chan prometheus.Metric, 16)
	go func() { telemetry.TemplateTimeout.Collect(ch); close(ch) }()
	found := false
	for m := range ch {
		var d dto.Metric
		_ = m.Write(&d)
		for _, l := range d.GetLabel() {
			found = found || l.GetName() == "template" && l.GetValue() == tpl
		}
	}
	return found
}
