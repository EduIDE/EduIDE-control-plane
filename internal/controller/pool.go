package controller

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
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

// orphanGrace is how long a claimed pod without a live session stays before the pool deletes it.
const orphanGrace = 30 * time.Second

// expectTTL bounds how long a created pod counts as idle before the cache shows it.
const expectTTL = 30 * time.Second

type poolReconciler struct {
	*deps
	mu sync.Mutex
	// created holds pods this reconciler created per pool, until the cache shows them.
	// Without it a reconcile on a cache that lags behind its own creates fills the pool twice.
	created map[types.NamespacedName]map[string]time.Time
	// orphans holds when a claimed pod was first seen without a live session.
	orphans map[string]time.Time
}

// unseen forgets the created pods the cache shows and returns how many it does not show yet.
func (r *poolReconciler) unseen(pool types.NamespacedName, pods []corev1.Pod, now time.Time) int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.created[pool]
	for i := range pods {
		delete(m, pods[i].Name)
	}
	for name, t := range m {
		if now.Sub(t) > expectTTL {
			delete(m, name)
		}
	}
	return int32(len(m)) //nolint:gosec // bounded by maxCreatesPerPass
}

func (r *poolReconciler) expect(pool types.NamespacedName, pod string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.created == nil {
		r.created = map[types.NamespacedName]map[string]time.Time{}
	}
	if r.created[pool] == nil {
		r.created[pool] = map[string]time.Time{}
	}
	r.created[pool][pod] = now
}

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
	// A pool that goes away or changes can make a pool of the same template active or inactive.
	bySibling := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		var pools v1.IDEPoolList
		if r.List(ctx, &pools, client.InNamespace(o.GetNamespace())) != nil {
			return nil
		}
		ref := o.(*v1.IDEPool).Spec.TemplateRef
		var out []reconcile.Request
		for _, p := range pools.Items {
			if p.Spec.TemplateRef == ref && p.Name != o.GetName() {
				out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&p)})
			}
		}
		return out
	})
	return ctrl.NewControllerManagedBy(mgr).Named("pool").For(&v1.IDEPool{}).
		Watches(&v1.IDEPool{}, bySibling).
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
	res := ctrl.Result{}
	now := r.clock.Now()
	del := func(p *corev1.Pod) error {
		if p.DeletionTimestamp != nil {
			return nil
		}
		// The resourceVersion guard spares a pod a session claimed a moment ago: the claim changed it (ADR 0009).
		return client.IgnoreNotFound(ignoreConflict(r.Delete(ctx, p, client.Preconditions{ResourceVersion: &p.ResourceVersion})))
	}
	for i := range all.Items {
		p := &all.Items[i]
		switch {
		case p.Labels[v1.LabelState] == v1.StateClaimed && p.DeletionTimestamp == nil:
			claimed++
			left, err := r.orphanLeft(ctx, p, now)
			if err != nil {
				return ctrl.Result{}, err
			}
			if left == 0 {
				slog.InfoContext(ctx, "deleted a claimed instance without a session", "pool", pool.Name, "session", p.Labels[v1.LabelSession])
				if err := del(p); err != nil {
					return ctrl.Result{}, err
				}
			} else if left > 0 && (res.RequeueAfter == 0 || left < res.RequeueAfter) {
				res.RequeueAfter = left
			}
		case p.Labels[v1.LabelState] == v1.StateIdle && !claim.Usable(p):
			// Failed or deleting idle pods are replaced; a failed one is removed here.
			if err := del(p); err != nil {
				return ctrl.Result{}, err
			}
		case p.Labels[v1.LabelState] == v1.StateIdle:
			// A restarted container is gone for good; a hopeless wait gets a grace period. A pull that can heal keeps the pod.
			_, hopeless := claim.Hopeless(p)
			if why := claim.Broken(p); why == "restarted" || hopeless != "" && now.Sub(p.CreationTimestamp.Time) > stuckLimit {
				why = cmp.Or(hopeless, why)
				slog.WarnContext(ctx, "deleted a broken idle instance", "pool", pool.Name, "reason", why)
				if err := del(p); err != nil {
					return ctrl.Result{}, err
				}
				continue
			}
			if gone, err := r.repairChildren(ctx, p); err != nil {
				return ctrl.Result{}, err
			} else if gone {
				slog.WarnContext(ctx, "deleted an idle instance without its token Secret", "pool", pool.Name)
				if err := del(p); err != nil {
					return ctrl.Result{}, err
				}
				continue
			}
			idle = append(idle, *p)
		}
	}

	// Only the oldest pool of a template is active; a second one would double the idle pods.
	duplicate, err := r.duplicateOf(ctx, pool)
	if err != nil {
		return ctrl.Result{}, err
	}

	np := r.nodePool(tmpl)
	underPressure := r.pressure.Active(np)
	target := claim.EffectiveIdle(pool.Spec.MinIdle, pool.Spec.TargetIdle, pool.Spec.MaxInstances, claimed, underPressure)
	if duplicate != "" {
		target = 0
	}

	// Idle pods built from an older template or agent image are replaced. Old ones that are not ready go at once.
	// An old ready one goes only when the pool has more ready pods than its target, and the pool starts one pod
	// above its target while old ones are left, so the ready count never drops during the roll.
	hash := r.specHash(tmpl)
	var keep, staleReady []corev1.Pod
	ready := int32(0)
	for i := range idle {
		p := &idle[i]
		switch {
		case p.Annotations[v1.AnnotationSpecHash] != hash && !claim.Ready(p):
			if err := del(p); err != nil {
				return ctrl.Result{}, err
			}
			continue
		case p.Annotations[v1.AnnotationSpecHash] != hash:
			staleReady = append(staleReady, *p)
		default:
			keep = append(keep, *p)
		}
		if claim.Ready(p) {
			ready++
		}
	}
	sort.Slice(staleReady, func(i, j int) bool { return staleReady[i].CreationTimestamp.Before(&staleReady[j].CreationTimestamp) })
	for len(staleReady) > 0 && ready > target {
		if err := del(&staleReady[0]); err != nil {
			return ctrl.Result{}, err
		}
		slog.InfoContext(ctx, "replacing an idle instance built from an older template", "pool", pool.Name)
		staleReady, ready = staleReady[1:], ready-1
	}
	idle = append(keep, staleReady...)
	surge := int32(0)
	if len(staleReady) > 0 && target > 0 && !underPressure && claimed+target < pool.Spec.MaxInstances {
		surge = 1
	}

	pending := r.unseen(req.NamespacedName, all.Items, r.clock.Now())

	switch have, want := int32(len(idle))+pending, target+surge; { //nolint:gosec // bounded by maxInstances
	case have < want:
		n := min(int(want-have), maxCreatesPerPass)
		for range n {
			pod, err := r.createInstance(ctx, tmpl, pool, map[string]string{v1.LabelPool: pool.Name, v1.LabelState: v1.StateIdle}, "")
			if apierrors.IsForbidden(err) && strings.Contains(err.Error(), "exceeded quota") {
				// Not a reconciler error: the quota blocks the refill, so log it and retry after the pressure window.
				slog.InfoContext(ctx, "the pool pod create was refused by the resource quota", "pool", pool.Name)
				return ctrl.Result{RequeueAfter: pressureTTL}, nil
			}
			if err != nil {
				return ctrl.Result{}, err
			}
			r.expect(req.NamespacedName, pod.Name, r.clock.Now())
			idle = append(idle, *pod)
		}
		if int(want-have) > n {
			res.RequeueAfter = time.Second
		}
	case have > want:
		gone := map[string]bool{}
		for _, p := range claim.Surplus(idle, int(have-want)) {
			if err := del(&p); err != nil {
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

	ready, starting := int32(0), pending
	for _, p := range idle {
		if claim.Ready(&p) {
			ready++
			continue
		}
		starting++
	}
	pool.Status.IdleReady, pool.Status.IdleStarting, pool.Status.Claimed = ready, starting, claimed
	pool.Status.Replicas = ready + starting
	pool.Status.Selector = fmt.Sprintf("%s=%s,%s=%s", v1.LabelPool, pool.Name, v1.LabelState, v1.StateIdle)
	pool.Status.ObservedGeneration = pool.Generation
	ready0 := metav1.Condition{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Reconciled", ObservedGeneration: pool.Generation}
	if duplicate != "" {
		ready0.Status, ready0.Reason, ready0.Message = metav1.ConditionFalse, "DuplicateTemplate", "pool "+duplicate+" is older and serves template "+pool.Spec.TemplateRef
	}
	meta.SetStatusCondition(&pool.Status.Conditions, ready0)
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

// orphanLeft is how long a claimed pod may stay before it is deleted: -1 when its session is alive, 0 when the grace is over.
func (r *poolReconciler) orphanLeft(ctx context.Context, p *corev1.Pod, now time.Time) (time.Duration, error) {
	alive := false
	if name := p.Labels[v1.LabelSession]; name != "" {
		s := &v1.IDESession{}
		key := client.ObjectKey{Namespace: p.Namespace, Name: name}
		err := r.Get(ctx, key, s)
		if apierrors.IsNotFound(err) {
			// The cache can lag behind a session created a moment ago.
			err = r.reader.Get(ctx, key, s)
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return 0, err
		}
		alive = err == nil && s.DeletionTimestamp == nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if alive {
		delete(r.orphans, p.Name)
		return -1, nil
	}
	if r.orphans == nil {
		r.orphans = map[string]time.Time{}
	}
	// Most orphans are pods of ended sessions that their finalizer deletes; forget the ones nobody asks for again.
	for k, t := range r.orphans {
		if now.Sub(t) > 2*orphanGrace {
			delete(r.orphans, k)
		}
	}
	since, ok := r.orphans[p.Name]
	if !ok {
		since = now
		r.orphans[p.Name] = since
	}
	if left := since.Add(orphanGrace).Sub(now); left > 0 {
		return left + time.Second, nil
	}
	delete(r.orphans, p.Name)
	return 0, nil
}

// duplicateOf returns the name of an older pool of the same template, or "". Only the oldest pool is active.
func (r *poolReconciler) duplicateOf(ctx context.Context, pool *v1.IDEPool) (string, error) {
	var pools v1.IDEPoolList
	if err := r.List(ctx, &pools, client.InNamespace(pool.Namespace)); err != nil {
		return "", err
	}
	for i := range pools.Items {
		o := &pools.Items[i]
		if o.Name == pool.Name || o.Spec.TemplateRef != pool.Spec.TemplateRef || o.DeletionTimestamp != nil {
			continue
		}
		if o.CreationTimestamp.Before(&pool.CreationTimestamp) || o.CreationTimestamp.Equal(&pool.CreationTimestamp) && o.Name < pool.Name {
			return o.Name, nil
		}
	}
	return "", nil
}
