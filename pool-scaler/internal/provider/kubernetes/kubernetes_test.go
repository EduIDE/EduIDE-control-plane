package kubernetes

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/EduIDE/EduIDE-control-plane/pool-scaler/internal/budget"
	"github.com/EduIDE/EduIDE-control-plane/pool-scaler/internal/model"
)

func pool(name, template string, annotations map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "eduide.github.io/v1alpha1", "kind": "IDEPool",
		"metadata": map[string]any{"name": name, "namespace": "ns", "annotations": annotations},
		"spec":     map[string]any{"templateRef": template, "minIdle": int64(2), "maxInstances": int64(50), "targetIdle": int64(4)},
		"status": map[string]any{
			"idleReady": int64(3), "idleStarting": int64(1), "claimed": int64(5),
			"conditions": []any{map[string]any{"type": "CapacityPressure", "status": "True"}},
		},
	}}
}

func template(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "eduide.github.io/v1alpha1", "kind": "IDETemplate",
		"metadata": map[string]any{"name": name, "namespace": "ns"},
		"spec":     map[string]any{"resources": map[string]any{"requests": map[string]any{"cpu": "500m", "memory": "500M"}}},
	}}
}

func provider(objs ...runtime.Object) *Provider {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{PoolGVR: "IDEPoolList", TemplateGVR: "IDETemplateList"}, objs...)
	return &Provider{Dynamic: dyn, Core: fake.NewClientset(), Namespace: "ns", PodOverhead: budget.Resources{CPU: 0.1, Memory: 128 << 20}}
}

func TestPoolsReadStatusAndTemplate(t *testing.T) {
	p := provider(pool("java-17", "java", map[string]any{"x": "y"}), template("java"))
	pools, err := p.Pools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	j := pools["java-17"]
	if j.Template != "java" || j.IdleReady != 3 || j.IdleStarting != 1 || j.Claimed != 5 || !j.CapacityPressure {
		t.Fatalf("%+v", j)
	}
	if j.TargetIdle == nil || *j.TargetIdle != 4 || j.MinIdle != 2 || j.MaxInstances != 50 || j.Annotations["x"] != "y" {
		t.Fatalf("%+v", j)
	}
	if d := j.PodCPU - 0.6; d > 1e-9 || d < -1e-9 {
		t.Fatalf("pod cpu %v, want 0.5 + 0.1 overhead", j.PodCPU)
	}
	if j.PodMemory != 500e6+128<<20 {
		t.Fatalf("pod memory %v", j.PodMemory)
	}
}

func TestPatchTargetIdleUsesScaleSubresource(t *testing.T) {
	p := provider(pool("java-17", "java", nil), template("java"))
	dyn := p.Dynamic.(*dynfake.FakeDynamicClient)
	if err := p.PatchTargetIdle(context.Background(), "java-17", 7); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "patch" && a.GetSubresource() == "scale" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no patch on the scale subresource: %v", dyn.Actions())
	}
}

func TestQuotaAndNodeRest(t *testing.T) {
	q := corev1.ResourceQuota{Status: corev1.ResourceQuotaStatus{
		Hard: corev1.ResourceList{corev1.ResourceRequestsCPU: resource.MustParse("200"), corev1.ResourceRequestsStorage: resource.MustParse("500Gi")},
		Used: corev1.ResourceList{corev1.ResourceRequestsCPU: resource.MustParse("6200m")},
	}}
	r := QuotaRest([]corev1.ResourceQuota{q})
	if r.CPU != 193.8 || r.Memory != budget.Unlimited.Memory {
		t.Fatalf("%+v", r)
	}
	if QuotaRest(nil) != nil {
		t.Fatal("no quota means no limit")
	}

	node := func(name string, unschedulable bool) corev1.Node {
		return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: corev1.NodeSpec{Unschedulable: unschedulable},
			Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16"), corev1.ResourceMemory: resource.MustParse("32Gi")}}}
	}
	running := func(nodeName string) corev1.Pod {
		return corev1.Pod{Spec: corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")}}}}}}
	}
	n := NodeRest([]corev1.Node{node("a", false), node("b", true)}, []corev1.Pod{running("a"), running("b"), running("elsewhere")})
	if n.CPU != 14 || n.Memory != 28*(1<<30) {
		t.Fatalf("%+v", n)
	}
}

func TestEventIsWrittenOnThePool(t *testing.T) {
	p := provider()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	p.Now = func() time.Time { return now }
	pool := &model.App{Name: "java-17", UID: "uid-1"}
	if err := p.Event(context.Background(), pool, "ScaledUp", "strategy reactive: targetIdle 2 to 4: x"); err != nil {
		t.Fatal(err)
	}
	if err := p.Event(context.Background(), pool, "Held", strings.Repeat("x", 2000)); err != nil {
		t.Fatal(err)
	}
	evs, err := p.Core.EventsV1().Events("ns").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(evs.Items) != 2 {
		t.Fatalf("%v %v", err, evs)
	}
	e := evs.Items[0]
	if e.Reason != "ScaledUp" {
		e = evs.Items[1]
	}
	if e.ReportingController != EventController || e.Reason != "ScaledUp" || e.Regarding.Kind != "IDEPool" || e.Regarding.Name != "java-17" || string(e.Regarding.UID) != "uid-1" ||
		e.Regarding.APIVersion != "eduide.github.io/v1alpha1" || e.Type != "Normal" || !e.EventTime.Time.Equal(now) || e.Action == "" {
		t.Fatalf("%+v", e)
	}
	for _, x := range evs.Items {
		if len(x.Note) > 1024 {
			t.Fatalf("note of %d bytes", len(x.Note))
		}
	}
}
