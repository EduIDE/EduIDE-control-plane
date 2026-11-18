package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

func TestTemplatePrepullAndValidation(t *testing.T) {
	requireEnv(t)
	tm := newTemplate("tpl-prepull")
	mustCreate(t, tm)
	ds := &appsv1.DaemonSet{}
	ds.Name, ds.Namespace = "prepull-tpl-prepull", testNS
	eventually(t, "prepull daemonset", func() bool { return get(ds) == nil })
	init := ds.Spec.Template.Spec.InitContainers
	if len(init) != 1 || init[0].Image != tm.Spec.Image || strings.Join(init[0].Command, " ") != "sh -c true" {
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
	if string(rs.ParentRefs[0].Name) != "gw" || string(*rs.ParentRefs[0].Namespace) != "gateway-system" || string(*rs.ParentRefs[0].SectionName) != "https" {
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
	// The pool refills its idle pod and counts the claimed one.
	eventually(t, "pool refilled", func() bool {
		_ = get(pool)
		return pool.Status.Claimed == 1 && pool.Status.IdleStarting+pool.Status.IdleReady >= 1
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
	setUnschedulable(t, st.PodName)

	eventually(t, "idle pods of the full node pool gone", func() bool { return len(podsWith(full)) == 0 })
	eventually(t, "capacity-exhausted", func() bool {
		st = sessionStatus("s-cap")
		return st.Phase == v1.SessionFailed && st.ErrorCode == v1.ErrorCapacityExhausted
	})
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
	if st.StartStrategy != v1.StartCold || pod.Spec.Volumes[0].PersistentVolumeClaim == nil || pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "w-test" {
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
func poke(ws *v1.IDEWorkspace) {
	for {
		if get(ws) != nil {
			return
		}
		if ws.Annotations == nil {
			ws.Annotations = map[string]string{}
		}
		ws.Annotations["test/poke"] = time.Now().String()
		if k8s.Update(context.Background(), ws) == nil {
			return
		}
	}
}
