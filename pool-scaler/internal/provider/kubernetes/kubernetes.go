// Package kubernetes reads the pool state, pod sizes, quota and node capacity,
// and writes spec.targetIdle through the pool's /scale subresource. It is the scaler's only writer.
package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/EduIDE/EduIDE-control-plane/pool-scaler/internal/budget"
	"github.com/EduIDE/EduIDE-control-plane/pool-scaler/internal/model"
)

// Served versions of the control plane resources (api/v1alpha1 in the monorepo).
var (
	PoolGVR     = schema.GroupVersionResource{Group: "eduide.github.io", Version: "v1alpha1", Resource: "idepools"}
	TemplateGVR = schema.GroupVersionResource{Group: "eduide.github.io", Version: "v1alpha1", Resource: "idetemplates"}
)

// conditionCapacityPressure is api/v1alpha1.ConditionCapacityPressure; this
// module does not import the root module.
const conditionCapacityPressure = "CapacityPressure"

// Provider talks to the Kubernetes API.
type Provider struct {
	Dynamic      dynamic.Interface
	Core         kubernetes.Interface
	Namespace    string
	NodeSelector map[string]string
	PodOverhead  budget.Resources
}

// Pools returns every IDEPool with its counts from status and the pod size
// from its IDETemplate.
func (p *Provider) Pools(ctx context.Context) (map[string]*model.App, error) {
	list, err := p.Dynamic.Resource(PoolGVR).Namespace(p.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list idepools: %w", err)
	}
	tpls, err := p.Dynamic.Resource(TemplateGVR).Namespace(p.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list idetemplates: %w", err)
	}
	requests := map[string]*unstructured.Unstructured{}
	for i := range tpls.Items {
		requests[tpls.Items[i].GetName()] = &tpls.Items[i]
	}
	pools := map[string]*model.App{}
	for i := range list.Items {
		a := p.poolFromUnstructured(&list.Items[i], requests)
		pools[a.Name] = a
	}
	return pools, nil
}

func (p *Provider) poolFromUnstructured(u *unstructured.Unstructured, templates map[string]*unstructured.Unstructured) *model.App {
	a := &model.App{Name: u.GetName(), Annotations: u.GetAnnotations(), ClaimRate: -1}
	a.Template, _, _ = unstructured.NestedString(u.Object, "spec", "templateRef")
	a.MinIdle = int(nestedInt(u.Object, "spec", "minIdle"))
	a.MaxInstances = int(nestedInt(u.Object, "spec", "maxInstances"))
	if v, ok, _ := unstructured.NestedInt64(u.Object, "spec", "targetIdle"); ok {
		t := int(v)
		a.TargetIdle = &t
	}
	a.IdleReady = int(nestedInt(u.Object, "status", "idleReady"))
	a.IdleStarting = int(nestedInt(u.Object, "status", "idleStarting"))
	a.Claimed = int(nestedInt(u.Object, "status", "claimed"))
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		if m, ok := c.(map[string]any); ok && m["type"] == conditionCapacityPressure {
			a.CapacityPressure = m["status"] == "True"
		}
	}
	a.PodCPU, a.PodMemory = p.PodOverhead.CPU, p.PodOverhead.Memory
	if t := templates[a.Template]; t != nil {
		req, _, _ := unstructured.NestedMap(t.Object, "spec", "resources", "requests")
		a.PodCPU += quantity(req["cpu"])
		a.PodMemory += quantity(req["memory"])
	}
	return a
}

func nestedInt(obj map[string]any, fields ...string) int64 {
	v, _, _ := unstructured.NestedInt64(obj, fields...)
	return v
}

func quantity(v any) float64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.AsApproximateFloat64()
}

// Budget returns the quota rest and the unrequested allocatable on the
// session nodes. nil means that source sets no limit.
func (p *Provider) Budget(ctx context.Context) (quota, nodes *budget.Resources, err error) {
	quotas, err := p.Core.CoreV1().ResourceQuotas(p.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("list resourcequotas: %w", err)
	}
	quota = QuotaRest(quotas.Items)

	nodeList, err := p.Core.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(p.NodeSelector).String()})
	if err != nil {
		return quota, nil, fmt.Errorf("list nodes: %w", err)
	}
	pods, err := p.Core.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "status.phase!=Succeeded,status.phase!=Failed"})
	if err != nil {
		return quota, nil, fmt.Errorf("list pods: %w", err)
	}
	nodes = NodeRest(nodeList.Items, pods.Items)
	return quota, nodes, nil
}

// QuotaRest is hard minus used for request quotas, the minimum over all
// quotas in the namespace.
func QuotaRest(quotas []corev1.ResourceQuota) *budget.Resources {
	if len(quotas) == 0 {
		return nil
	}
	rest := budget.Unlimited
	for _, q := range quotas {
		for _, pair := range []struct {
			names []corev1.ResourceName
			dst   *float64
		}{
			{[]corev1.ResourceName{corev1.ResourceRequestsCPU, corev1.ResourceCPU}, &rest.CPU},
			{[]corev1.ResourceName{corev1.ResourceRequestsMemory, corev1.ResourceMemory}, &rest.Memory},
		} {
			for _, n := range pair.names {
				hard, ok := q.Status.Hard[n]
				if !ok {
					hard, ok = q.Spec.Hard[n]
				}
				if !ok {
					continue
				}
				used := q.Status.Used[n]
				*pair.dst = math.Min(*pair.dst, hard.AsApproximateFloat64()-used.AsApproximateFloat64())
			}
		}
	}
	return &rest
}

// NodeRest is allocatable minus requested over the schedulable nodes given.
func NodeRest(nodes []corev1.Node, pods []corev1.Pod) *budget.Resources {
	if len(nodes) == 0 {
		return nil
	}
	in := map[string]bool{}
	var rest budget.Resources
	for _, n := range nodes {
		if n.Spec.Unschedulable {
			continue
		}
		in[n.Name] = true
		rest.CPU += n.Status.Allocatable.Cpu().AsApproximateFloat64()
		rest.Memory += n.Status.Allocatable.Memory().AsApproximateFloat64()
	}
	for _, pod := range pods {
		if !in[pod.Spec.NodeName] {
			continue
		}
		for _, c := range pod.Spec.Containers {
			rest.CPU -= c.Resources.Requests.Cpu().AsApproximateFloat64()
			rest.Memory -= c.Resources.Requests.Memory().AsApproximateFloat64()
		}
	}
	return &rest
}

// PatchTargetIdle sets targetIdle through the /scale subresource, so the
// scaler needs no update right on the pool itself.
func (p *Provider) PatchTargetIdle(ctx context.Context, pool string, v int) error {
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"replicas": v}})
	_, err := p.Dynamic.Resource(PoolGVR).Namespace(p.Namespace).Patch(ctx, pool, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "pool-scaler"}, "scale")
	return err
}
