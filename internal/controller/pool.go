package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/claim"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

// maxCreatesPerPass bounds the burst of one pool reconcile; the pool is requeued for the rest.
const maxCreatesPerPass = 50

type poolReconciler struct{ *deps }

func (r *poolReconciler) setup(mgr ctrl.Manager) error {
	// Pods are mapped by label, not by owner: a claimed pod is owned by its session but still counts for the pool.
	byPool := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		if p := o.GetLabels()[v1.LabelPool]; p != "" {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: p}}}
		}
		return nil
	})
	byTemplate := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		var pools v1.IDEPoolList
		if r.List(ctx, &pools, client.InNamespace(o.GetNamespace())) != nil {
			return nil
		}
		var out []reconcile.Request
		for _, p := range pools.Items {
			if p.Spec.TemplateRef == o.GetName() {
				out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&p)})
			}
		}
		return out
	})
	return ctrl.NewControllerManagedBy(mgr).Named("pool").For(&v1.IDEPool{}).
		Watches(&corev1.Pod{}, byPool).
		Watches(&v1.IDETemplate{}, byTemplate).
		WithOptions(opts(4)).Complete(r)
}

func (r *poolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pool := &v1.IDEPool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	orig := pool.DeepCopy()
	tmpl := &v1.IDETemplate{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: pool.Namespace, Name: pool.Spec.TemplateRef}, tmpl); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{Type: v1.ConditionReady, Status: metav1.ConditionFalse,
			Reason: "TemplateNotFound", Message: fmt.Sprintf("template %s not found", pool.Spec.TemplateRef), ObservedGeneration: pool.Generation})
		return ctrl.Result{}, r.writeStatus(ctx, orig, pool)
	}

	var all corev1.PodList
	if err := r.List(ctx, &all, client.InNamespace(pool.Namespace), client.MatchingLabels{v1.LabelPool: pool.Name}); err != nil {
		return ctrl.Result{}, err
	}
	var claimed int32
	var idle []corev1.Pod
	for i := range all.Items {
		p := &all.Items[i]
		switch {
		case p.Labels[v1.LabelState] == v1.StateClaimed && p.DeletionTimestamp == nil:
			claimed++
		case p.Labels[v1.LabelState] == v1.StateIdle && !claim.Usable(p):
			// Failed or deleting idle pods are replaced; a failed one is removed here.
			if p.DeletionTimestamp == nil {
				if err := r.Delete(ctx, p); client.IgnoreNotFound(err) != nil {
					return ctrl.Result{}, err
				}
			}
		case p.Labels[v1.LabelState] == v1.StateIdle:
			idle = append(idle, *p)
		}
	}

	np := r.nodePool(tmpl)
	underPressure := r.pressure.Active(np)
	target := claim.EffectiveIdle(pool.Spec.MinIdle, pool.Spec.TargetIdle, pool.Spec.MaxInstances, claimed, underPressure)
	res := ctrl.Result{}
	fresh := map[string]bool{}

	switch have := int32(len(idle)); { //nolint:gosec // bounded by maxInstances
	case have < target:
		n := min(int(target-have), maxCreatesPerPass)
		for range n {
			pod, err := r.createInstance(ctx, tmpl, pool, map[string]string{v1.LabelPool: pool.Name, v1.LabelState: v1.StateIdle}, "")
			if err != nil {
				return ctrl.Result{}, err
			}
			idle = append(idle, *pod)
			fresh[pod.Name] = true
		}
		if int(target-have) > n {
			res.RequeueAfter = time.Second
		}
	case have > target:
		gone := map[string]bool{}
		for _, p := range claim.Surplus(idle, int(have-target)) {
			if err := r.Delete(ctx, &p); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			gone[p.Name] = true
		}
		kept := idle[:0]
		for _, p := range idle {
			if !gone[p.Name] {
				kept = append(kept, p)
			}
		}
		idle = kept
	}

	var ready, starting int32
	for _, p := range idle {
		if claim.Ready(&p) {
			ready++
			continue
		}
		starting++
		if fresh[p.Name] {
			continue
		}
		if err := r.ensureChildren(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
	}
	pool.Status.IdleReady, pool.Status.IdleStarting, pool.Status.Claimed = ready, starting, claimed
	pool.Status.Replicas = ready + starting
	pool.Status.Selector = fmt.Sprintf("%s=%s,%s=%s", v1.LabelPool, pool.Name, v1.LabelState, v1.StateIdle)
	pool.Status.ObservedGeneration = pool.Generation
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Reconciled", ObservedGeneration: pool.Generation})
	ps := metav1.ConditionFalse
	reason := "NoPressure"
	if underPressure {
		ps, reason = metav1.ConditionTrue, "NodePoolFull"
		res.RequeueAfter = 5 * time.Second
	}
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{Type: v1.ConditionCapacityPressure, Status: ps, Reason: reason, ObservedGeneration: pool.Generation})

	telemetry.PoolInstances.WithLabelValues(pool.Name, "idle_ready").Set(float64(ready))
	telemetry.PoolInstances.WithLabelValues(pool.Name, "idle_starting").Set(float64(starting))
	telemetry.PoolInstances.WithLabelValues(pool.Name, "claimed").Set(float64(claimed))
	telemetry.PoolTargetIdle.WithLabelValues(pool.Name).Set(float64(target))
	return res, r.writeStatus(ctx, orig, pool)
}

// writeStatus writes the whole status, only on change: it has required counters a merge patch would omit.
func (r *poolReconciler) writeStatus(ctx context.Context, orig, pool *v1.IDEPool) error {
	if equality.Semantic.DeepEqual(orig.Status, pool.Status) {
		return nil
	}
	// A conflict means the pool changed; that event reconciles it again.
	return client.IgnoreNotFound(ignoreConflict(r.Status().Update(ctx, pool, client.FieldOwner(fieldManager))))
}

func ignoreConflict(err error) error {
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}
