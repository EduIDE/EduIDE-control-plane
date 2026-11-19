package controller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/agentclient"
	"github.com/EduIDE/EduIDE-control-plane/internal/claim"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

const (
	condCapacity    = v1.ConditionCapacityPressure
	reasonQuota     = "QuotaExceeded"
	condIdleWarned  = "IdleWarningSent"
	condMaxWarned   = "MaxWarningSent"
	activityStep    = 5 * time.Minute
	agentWaitLimit  = 2 * time.Minute
	agentCallBudget = 5 * time.Second
	// pollBudget is shorter than the claim budget, so unreachable agents hold workers only briefly.
	pollBudget = 2 * time.Second
	// startLimit fails a session whose pod does not become ready, for example on an image pull error.
	startLimit = 10 * time.Minute
	// stuckLimit fails a session earlier when a container waits for a reason that does not heal (image pull errors).
	stuckLimit = 60 * time.Second
	// failedRetention keeps a failed session so the landing page can read the error.
	failedRetention = 10 * time.Minute
)

// lowPriority puts timeout polls and cleanups behind claims in the work queue.
var lowPriority = new(handler.LowPriority)

type sessionReconciler struct {
	*deps
	// readyWritten holds sessions whose Ready status this operator wrote but the cache does not show yet.
	// A reconcile on the stale copy would claim the agent a second time.
	readyWritten sync.Map
	// failedWritten holds the failure code per session this operator wrote, so a reconcile on a stale cache
	// does not log, count and emit the same failure again.
	failedWritten sync.Map
	// starts and exported hold the tracing state per session (trace.go).
	starts, exported sync.Map
}

func (r *sessionReconciler) setup(mgr ctrl.Manager) error {
	// The service creates the data Secret right after the session, so its arrival wakes the session.
	bySecret := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		for _, ref := range o.GetOwnerReferences() {
			if ref.Kind == "IDESession" {
				return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: ref.Name}}}
			}
		}
		return nil
	})
	byWorkspace := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		var l v1.IDESessionList
		if r.List(ctx, &l, client.InNamespace(o.GetNamespace()), client.MatchingFields{indexWorkspaceRef: o.GetName()}) != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(l.Items))
		for _, s := range l.Items {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&s)})
		}
		return out
	})
	return ctrl.NewControllerManagedBy(mgr).Named("session").For(&v1.IDESession{}).
		Owns(&corev1.Pod{}).
		Watches(&corev1.Secret{}, bySecret).
		Watches(&v1.IDEWorkspace{}, byWorkspace).
		WithOptions(opts(16)).Complete(r)
}

// workspaceWaitLimit is how long a session waits for a workspace that is not in the cache yet.
const workspaceWaitLimit = 30 * time.Second

func (r *sessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	s := &v1.IDESession{}
	if err := r.Get(ctx, req.NamespacedName, s); err != nil {
		r.readyWritten.Delete(req.NamespacedName)
		r.forget(req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if _, ok := r.readyWritten.Load(req.NamespacedName); ok {
		if s.Status.Phase == "" || s.Status.Phase == v1.SessionPending || s.Status.Phase == v1.SessionStarting {
			// The status event follows in a moment and reconciles the session again.
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		r.readyWritten.Delete(req.NamespacedName)
	}
	ctx = r.traceContext(ctx, s)
	slog.DebugContext(ctx, "reconcile session", "session", s.Name, "phase", string(s.Status.Phase), "instance", s.Status.Instance, "deleting", s.DeletionTimestamp != nil)
	if s.DeletionTimestamp != nil {
		return r.finalize(ctx, s)
	}
	if !controllerutil.ContainsFinalizer(s, v1.SessionFinalizer) {
		orig := s.DeepCopy()
		controllerutil.AddFinalizer(s, v1.SessionFinalizer)
		if err := r.Patch(ctx, s, client.MergeFrom(orig), client.FieldOwner(fieldManager)); err != nil {
			return ctrl.Result{}, err
		}
	}
	if s.Status.Phase == v1.SessionFailed {
		return r.expire(ctx, s)
	}
	tmpl := &v1.IDETemplate{}
	tmplErr := r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.TemplateRef}, tmpl)
	if tmplErr != nil && !apierrors.IsNotFound(tmplErr) {
		return ctrl.Result{}, tmplErr
	}
	pod, err := r.findPod(ctx, s)
	if err != nil {
		return ctrl.Result{}, err
	}
	if pod != nil && pod.DeletionTimestamp != nil && s.Status.Instance == "" {
		pod = nil // a pod released after an agent conflict, still in the cache
	}
	if tmplErr != nil {
		if pod == nil && s.Status.Instance == "" {
			return r.fail(ctx, s, v1.ErrorTemplateNotFound, fmt.Sprintf("template %s not found", s.Spec.TemplateRef))
		}
		// A session with an instance keeps running when its template is deleted, for example by a chart upgrade.
		// It ends by the default timeouts.
		tmpl = &v1.IDETemplate{ObjectMeta: metav1.ObjectMeta{Name: s.Spec.TemplateRef, Namespace: s.Namespace}}
	}
	if pod == nil {
		if s.Status.Instance != "" {
			return r.fail(ctx, s, v1.ErrorStartFailed, "the instance is gone")
		}
		var res ctrl.Result
		if pod, res, err = r.start(ctx, s, tmpl); pod == nil {
			return res, err
		}
	}
	return r.progress(ctx, s, tmpl, pod)
}

// findPod looks in the cache first. A miss is checked against the API server, so a pod claimed a moment ago is never claimed twice.
func (r *sessionReconciler) findPod(ctx context.Context, s *v1.IDESession) (*corev1.Pod, error) {
	var l corev1.PodList
	sel := client.MatchingLabels{v1.LabelSession: s.Name}
	if err := r.List(ctx, &l, client.InNamespace(s.Namespace), sel); err != nil {
		return nil, err
	}
	if p := ownPod(s, l.Items); p != nil {
		return p, nil
	}
	if err := r.reader.List(ctx, &l, client.InNamespace(s.Namespace), sel); err != nil {
		return nil, err
	}
	return ownPod(s, l.Items), nil
}

// ownPod picks the instance of the session. The session label alone is not enough: the service reuses a
// session name for the same launch key, so the pod of an earlier session of that name can still be
// terminating. Only a pod this session controls counts, the recorded instance only once there is one, and a
// live pod before a terminating one.
func ownPod(s *v1.IDESession, pods []corev1.Pod) *corev1.Pod {
	var best *corev1.Pod
	for i := range pods {
		p := &pods[i]
		if !metav1.IsControlledBy(p, s) || (s.Status.Instance != "" && p.Labels[v1.LabelInstance] != s.Status.Instance) {
			continue
		}
		if best == nil || (best.DeletionTimestamp != nil && p.DeletionTimestamp == nil) {
			best = p
		}
	}
	return best
}

// start claims an idle pod or creates a cold one. It returns nil when it waits or fails.
func (r *sessionReconciler) start(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate) (*corev1.Pod, ctrl.Result, error) {
	np := r.nodePool(tmpl)
	var claimName string
	if ref := s.Spec.WorkspaceRef; ref != "" {
		ws := &v1.IDEWorkspace{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: ref}, ws); err != nil {
			if apierrors.IsNotFound(err) {
				// The service creates the workspace right before the session; the cache may not have it yet.
				if r.clock.Now().Sub(s.CreationTimestamp.Time) < workspaceWaitLimit {
					return nil, ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
				}
				res, err := r.fail(ctx, s, v1.ErrorWorkspaceNotFound, fmt.Sprintf("workspace %s not found", ref))
				return nil, res, err
			}
			return nil, ctrl.Result{}, err
		}
		if other, err := r.workspaceHolder(ctx, s); err != nil {
			return nil, ctrl.Result{}, err
		} else if other != "" {
			res, err := r.fail(ctx, s, v1.ErrorWorkspaceInUse, "the workspace is used by another running session")
			return nil, res, err
		}
		if ws.VolumeLost() {
			res, err := r.fail(ctx, s, v1.ErrorWorkspaceLost, fmt.Sprintf("the volume of workspace %s is gone", ref))
			return nil, res, err
		}
		if ws.Status.Phase != v1.WorkspaceReady || ws.Status.ClaimName == "" {
			// The workspace watch wakes the session when the volume is there.
			orig := s.DeepCopy()
			s.Status.Phase = v1.SessionPending
			return nil, ctrl.Result{}, r.patchStatus(ctx, orig, s)
		}
		claimName = ws.Status.ClaimName
	} else {
		kctx, kspan := tracer().Start(ctx, "claim")
		pod, retry, err := r.tryClaim(kctx, s, tmpl, np)
		if err != nil {
			failSpan(kspan, v1.ErrorStartFailed)
		}
		r.tag(kspan, s, tmpl, pod)
		if pod == nil && !retry && err == nil {
			kspan.SetAttributes(attribute.String("eduide.strategy", string(v1.StartCold))) // nothing to claim: the instance is created
		}
		kspan.End()
		if pod != nil || retry || err != nil {
			r.tag(trace.SpanFromContext(ctx), s, tmpl, pod)
			// After a conflict the cache is behind; look again instead of starting cold.
			return pod, ctrl.Result{Requeue: retry}, err //nolint:staticcheck // rate limited retry, no fixed delay
		}
	}

	cctx, cspan := tracer().Start(ctx, "instance.create")
	pod, err := r.createInstance(cctx, tmpl, s, r.sessionLabels(s, nil), claimName)
	if err != nil {
		failSpan(cspan, v1.ErrorStartFailed)
	}
	r.tag(cspan, s, tmpl, pod)
	cspan.End()
	if err == nil {
		r.note(ctx, s, corev1.EventTypeNormal, "Created", "created a new instance (cold start)")
	}
	if apierrors.IsForbidden(err) && strings.Contains(err.Error(), "exceeded quota") {
		// Quota is capacity pressure like an unschedulable pod (G8).
		res, err := r.capacity(ctx, s, tmpl, true)
		return nil, res, err
	}
	return pod, ctrl.Result{}, err
}

func (r *sessionReconciler) sessionLabels(s *v1.IDESession, extra map[string]string) map[string]string {
	l := map[string]string{v1.LabelState: v1.StateClaimed, v1.LabelSession: s.Name, v1.LabelOwnerHash: v1.OwnerHash(s.Spec.Owner)}
	for k, v := range extra {
		l[k] = v
	}
	return l
}

// tryClaim takes an idle pool pod with one merge patch guarded by the resourceVersion (ADR 0009).
// retry is true when a candidate was in conflict: the cache is behind and may still hold an idle pod.
func (r *sessionReconciler) tryClaim(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate, nodePool string) (*corev1.Pod, bool, error) {
	var pods corev1.PodList
	err := r.List(ctx, &pods, client.InNamespace(s.Namespace), client.HasLabels{v1.LabelPool},
		client.MatchingLabels{v1.LabelTemplate: tmpl.Name, v1.LabelState: v1.StateIdle, v1.LabelComponent: v1.ComponentSession})
	if err != nil {
		return nil, false, err
	}
	conflict := false
	// Under pressure the claim does not wait for pods that are still starting.
	cands := claim.Candidates(pods.Items, !r.pressure.Active(nodePool))
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.Int("eduide.claim.idle_pods", len(pods.Items)), attribute.Int("eduide.claim.candidates", len(cands)),
		attribute.Bool("eduide.claim.under_pressure", r.pressure.Active(nodePool)))
	slog.DebugContext(ctx, "claim candidates", "session", s.Name, "template", tmpl.Name, "idle_pods", len(pods.Items), "candidates", len(cands), "under_pressure", r.pressure.Active(nodePool))
	for _, c := range cands {
		pod, before := c.DeepCopy(), c.DeepCopy()
		for k, v := range r.sessionLabels(s, nil) {
			pod.Labels[k] = v
		}
		pod.OwnerReferences = nil
		if err := controllerutil.SetControllerReference(s, pod, r.scheme); err != nil {
			return nil, false, err
		}
		err := r.Patch(ctx, pod, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(fieldManager))
		switch {
		case apierrors.IsConflict(err):
			slog.DebugContext(ctx, "claim conflict, trying the next candidate", "session", s.Name, "pod", c.Name)
			conflict = true
			span.AddEvent("claim.conflict", trace.WithAttributes(attribute.String("eduide.claim.pod", c.Name)))
			telemetry.ClaimTotal.WithLabelValues(tmpl.Name, "conflict").Inc()
		case apierrors.IsNotFound(err):
			span.AddEvent("claim.gone", trace.WithAttributes(attribute.String("eduide.claim.pod", c.Name)))
			telemetry.ClaimTotal.WithLabelValues(tmpl.Name, "conflict").Inc()
		case err != nil:
			return nil, false, err
		default:
			slog.DebugContext(ctx, "claim won", "session", s.Name, "pod", c.Name)
			span.AddEvent("claim.won", trace.WithAttributes(attribute.String("eduide.claim.pod", c.Name)))
			span.SetAttributes(attribute.String("eduide.claim.result", "claimed"))
			telemetry.ClaimTotal.WithLabelValues(tmpl.Name, "claimed").Inc()
			r.note(ctx, s, corev1.EventTypeNormal, "Claimed", "claimed an idle instance")
			return pod, false, nil
		}
	}
	if conflict {
		span.SetAttributes(attribute.String("eduide.claim.result", "conflict"))
		return nil, true, nil
	}
	span.SetAttributes(attribute.String("eduide.claim.result", "empty"))
	// Templates without a pool never have an idle pod: that is no signal.
	var pools v1.IDEPoolList
	if err := r.List(ctx, &pools, client.InNamespace(s.Namespace)); err != nil {
		return nil, false, err
	}
	if slices.ContainsFunc(pools.Items, func(p v1.IDEPool) bool { return p.Spec.TemplateRef == tmpl.Name }) {
		telemetry.ClaimTotal.WithLabelValues(tmpl.Name, "empty").Inc()
	}
	return nil, false, nil
}

// progress moves a session with a pod through Starting to Ready.
func (r *sessionReconciler) progress(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate, pod *corev1.Pod) (ctrl.Result, error) {
	now := r.clock.Now()
	orig := s.DeepCopy()
	st := &s.Status
	st.Instance, st.PodName, st.ObservedGeneration = pod.Labels[v1.LabelInstance], pod.Name, s.Generation
	if st.StartStrategy == "" {
		st.StartStrategy = strategyOf(pod)
	}
	r.tag(trace.SpanFromContext(ctx), s, tmpl, pod)
	if st.Timestamps.Claimed == nil {
		t := metav1.NewTime(now)
		if st.StartStrategy == v1.StartCold {
			t = pod.CreationTimestamp
		}
		st.Timestamps.Claimed = &t
	}
	if st.Phase == "" || st.Phase == v1.SessionPending {
		st.Phase = v1.SessionStarting
	}

	switch {
	case pod.DeletionTimestamp != nil:
		return r.fail(ctx, s, v1.ErrorStartFailed, "the instance was terminated")
	case st.Phase == v1.SessionReady && !claim.Usable(pod):
		// An evicted pod or one of a node that shut down keeps its object; without this the session stays Ready
		// with a dead URL until the maximum lifetime.
		return r.fail(ctx, s, v1.ErrorStartFailed, "the instance stopped")
	case st.Phase == v1.SessionReady:
		return r.running(ctx, s, tmpl, pod)
	case claim.Unschedulable(pod):
		return r.capacity(ctx, s, tmpl, false)
	}
	if c := meta.FindStatusCondition(st.Conditions, condCapacity); c != nil && c.Status == metav1.ConditionTrue {
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: condCapacity, Status: metav1.ConditionFalse, Reason: "Placed", LastTransitionTime: metav1.NewTime(now)})
	}
	// Also on a ready pod: a create that failed half way leaves the instance without its route.
	if gone, err := r.repairChildren(ctx, pod); err != nil {
		return ctrl.Result{}, err
	} else if gone {
		return r.fail(ctx, s, v1.ErrorStartFailed, "the access token Secret of the instance is missing")
	}
	if !claim.Ready(pod) {
		switch {
		case !claim.Usable(pod):
			return r.fail(ctx, s, v1.ErrorStartFailed, "the instance stopped")
		case now.Sub(st.Timestamps.Claimed.Time) > startLimit:
			return r.fail(ctx, s, v1.ErrorStartFailed, "the instance did not become ready in time")
		}
		wait := time.Minute
		if c, reason := claim.Hopeless(pod); reason != "" {
			left := stuckLimit - now.Sub(st.Timestamps.Claimed.Time)
			if left <= 0 {
				return r.fail(ctx, s, v1.ErrorStartFailed, fmt.Sprintf("the instance cannot start: container %s waits with %s", c, reason))
			}
			wait = left + time.Second
		}
		return ctrl.Result{RequeueAfter: wait}, r.patchStatus(ctx, orig, s)
	}

	if st.Timestamps.PodReady == nil {
		t := *st.Timestamps.Claimed
		if ready := readyTime(pod); ready.After(t.Time) {
			t = ready
		}
		st.Timestamps.PodReady = &t
	}
	stuck := now.Sub(st.Timestamps.PodReady.Time) > agentWaitLimit
	wait := func(why string) (ctrl.Result, error) {
		if stuck {
			return r.fail(ctx, s, v1.ErrorStartFailed, why)
		}
		return ctrl.Result{RequeueAfter: time.Second}, r.patchStatus(ctx, orig, s)
	}

	data, ok, err := r.launchData(ctx, s)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		return wait(fmt.Sprintf("launch data Secret %s missing or not labelled %s=%s", s.Spec.DataSecretRef, v1.LabelManagedBy, v1.LabelManagedByValue))
	}
	base, token, err := r.agentTarget(ctx, pod)
	if err != nil {
		return wait("the agent is not reachable")
	}
	err = r.pushClaim(ctx, s, tmpl, pod, base, token, data)
	switch {
	case errors.Is(err, agentclient.ErrConflict) && st.StartStrategy != v1.StartCold:
		// The pod was claimed out of band after we patched it: drop it and take the next candidate or start cold.
		slog.WarnContext(ctx, "agent refused the claim, releasing the pod", "session", s.Name, "pod", pod.Name)
		st.Instance, st.PodName, st.StartStrategy, st.Timestamps.Claimed = "", "", "", nil
		if err := r.patchStatus(ctx, orig, s); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 200 * time.Millisecond}, nil
	case errors.Is(err, agentclient.ErrConflict):
		return r.fail(ctx, s, v1.ErrorStartFailed, "the instance belongs to another session")
	case err != nil:
		slog.WarnContext(ctx, "agent claim failed", "session", s.Name, "error", err.Error())
		return wait("the agent did not accept the launch data")
	}

	t := metav1.NewTime(now)
	st.Timestamps.AgentReady, st.Timestamps.Ready = &t, &t
	st.Phase, st.URL, st.ErrorCode, st.Message = v1.SessionReady, r.cfg.InstanceURL(st.Instance), "", ""
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: s.Generation, LastTransitionTime: t})
	sctx, sspan := tracer().Start(ctx, "status.ready")
	err = r.patchStatus(sctx, orig, s)
	if err != nil {
		failSpan(sspan, v1.ErrorStartFailed)
	}
	r.tag(sspan, s, tmpl, pod)
	sspan.End()
	if err != nil {
		return ctrl.Result{}, err
	}
	lo, hi := r.phaseBounds(s)
	r.emit(ctx, s, tmpl, pod, podPhases(pod), lo, hi)
	r.endStart(s, "ready", "")
	r.readyWritten.Store(client.ObjectKeyFromObject(s), struct{}{})
	strategy := string(st.StartStrategy)
	r.note(ctx, s, corev1.EventTypeNormal, "Ready", fmt.Sprintf("ready after %.1fs (%s)", now.Sub(s.CreationTimestamp.Time).Seconds(), strategy),
		"result", "ready", "strategy", strategy, "duration_s", now.Sub(s.CreationTimestamp.Time).Seconds())
	telemetry.SessionPhase.WithLabelValues("claim", strategy).Observe(st.Timestamps.Claimed.Sub(s.CreationTimestamp.Time).Seconds())
	telemetry.SessionPhase.WithLabelValues("pod_ready", strategy).Observe(st.Timestamps.PodReady.Sub(st.Timestamps.Claimed.Time).Seconds())
	telemetry.SessionPhase.WithLabelValues("agent_ready", strategy).Observe(now.Sub(st.Timestamps.PodReady.Time).Seconds())
	telemetry.SessionStartup.WithLabelValues(tmpl.Name, strategy, "ready", persistenceOf(s)).Observe(now.Sub(s.CreationTimestamp.Time).Seconds())
	if s.Spec.WorkspaceRef != "" {
		if err := r.touchWorkspace(ctx, s); err != nil {
			slog.WarnContext(ctx, "cannot update workspace lastUsed", "workspace", s.Spec.WorkspaceRef, "error", err.Error())
		}
	}
	return ctrl.Result{RequeueAfter: min(r.jitter(), (*milestoneChecks.Load())[0]), Priority: lowPriority}, nil
}

func readyTime(p *corev1.Pod) metav1.Time {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.LastTransitionTime
		}
	}
	return metav1.Time{}
}

func (r *sessionReconciler) jitter() time.Duration {
	return time.Duration(float64(r.poll) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // jitter, not security
}

// launchData reads the data Secret. ok is false while it does not exist yet. The values are never logged.
func (r *sessionReconciler) launchData(ctx context.Context, s *v1.IDESession) (map[string]string, bool, error) {
	data := map[string]string{}
	if s.Spec.DataSecretRef == "" {
		return data, true, nil
	}
	sec := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.DataSecretRef}, sec); err != nil {
		return nil, false, client.IgnoreNotFound(err)
	}
	for k, v := range sec.Data {
		data[k] = string(v)
	}
	return data, true, nil
}

// agentTarget is the control address and the bearer token of a pod's agent.
func (r *sessionReconciler) agentTarget(ctx context.Context, pod *corev1.Pod) (string, string, error) {
	base, err := r.agentURL(pod)
	if err != nil {
		return "", "", err
	}
	sec := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: tokenName(pod.Labels[v1.LabelInstance])}, sec); err != nil {
		return "", "", err
	}
	return base, string(sec.Data[v1.InstanceTokenKey]), nil
}

// running enforces idle and maximum lifetime from the agent's activity (ADR 0016).
func (r *sessionReconciler) running(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate, pod *corev1.Pod) (ctrl.Result, error) {
	now := r.clock.Now()
	orig := s.DeepCopy()
	st := &s.Status
	base := st.Timestamps.Claimed.Time
	if st.Timestamps.AgentReady != nil {
		base = st.Timestamps.AgentReady.Time
	}
	url, token, terr := r.agentTarget(ctx, pod)
	last := time.Time{}
	if st.LastActivity != nil {
		last = st.LastActivity.Time
	}
	polled := false
	if terr == nil {
		cctx, cancel := context.WithTimeout(ctx, pollBudget)
		as, err := r.agent.Status(cctx, url, token)
		cancel()
		polled = err == nil
		if polled && !as.Claimed {
			// The agent container restarted and lost the launch data: push it again.
			r.reclaim(ctx, s, tmpl, pod, url, token)
		}
		if polled && as.Milestones != nil {
			r.exportMilestones(ctx, s, tmpl, pod, *as.Milestones)
		}
		if err == nil && as.LastActivity != nil {
			if as.LastActivity.Sub(last) > activityStep || st.LastActivity == nil {
				t := metav1.NewTime(*as.LastActivity)
				st.LastActivity = &t
			}
			last = *as.LastActivity
		}
	}
	if last.After(base) {
		base = last
	}
	if !polled {
		// Missing data alone never ends a session for idleness (ADR 0016); the maximum lifetime still applies.
		base = now
	}
	to := resolveTimeouts(tmpl)
	idleWarned := meta.IsStatusConditionTrue(st.Conditions, condIdleWarned)
	if idleWarned && now.Before(base.Add(to.warning)) {
		// The user came back, so the next idle period gets its own warning.
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: condIdleWarned, Status: metav1.ConditionFalse, Reason: "Active", LastTransitionTime: metav1.NewTime(now)})
		idleWarned = false
	}
	maxWarned := meta.IsStatusConditionTrue(st.Conditions, condMaxWarned)
	d := decide(now, s.CreationTimestamp.Time, base, to, idleWarned, maxWarned)

	switch d.act {
	case actEnd:
		why := "idle"
		if !now.Before(s.CreationTimestamp.Add(to.max)) {
			why = "max"
		}
		r.note(ctx, s, corev1.EventTypeNormal, "Ended", "ended by the "+why+" timeout", "reason", why)
		return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, s))
	case actWarnIdle, actWarnMax:
		kind, cond, msg := agentapi.IdleWarning, condIdleWarned, "This session ends soon because it is idle."
		if d.act == actWarnMax {
			kind, cond, msg = agentapi.MaxWarning, condMaxWarned, "This session reaches its maximum lifetime soon."
		}
		if terr == nil {
			cctx, cancel := context.WithTimeout(ctx, pollBudget)
			err := r.agent.Notify(cctx, url, token, agentapi.Notice{Kind: kind, Message: msg, At: &d.at})
			cancel()
			if err == nil {
				meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: cond, Status: metav1.ConditionTrue, Reason: "Sent", LastTransitionTime: metav1.NewTime(now)})
			}
		}
	}
	if err := r.patchStatus(ctx, orig, s); err != nil {
		return ctrl.Result{}, err
	}
	wait := min(r.jitter(), d.next.Sub(now))
	if st.Timestamps.Ready != nil {
		if next := nextMilestoneCheck(now, st.Timestamps.Ready.Time); next > 0 {
			wait = min(wait, next)
		}
	}
	if d.act != actNone {
		wait = min(wait, 5*time.Second)
	}
	return ctrl.Result{RequeueAfter: max(wait, time.Second), Priority: lowPriority}, nil
}

// capacity handles a session pod that cannot be placed (G8): evict the oldest idle pod of the node pool and fail after exhaustedAfter.
// quota is true when the pod create was refused by a resource quota. That is told once per session as an Event and a log line.
func (r *sessionReconciler) capacity(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate, quota bool) (ctrl.Result, error) {
	now := r.clock.Now()
	np := r.nodePool(tmpl)
	r.pressure.Mark(np)
	orig := s.DeepCopy()
	since := now
	told := false
	if c := meta.FindStatusCondition(s.Status.Conditions, condCapacity); c != nil && c.Status == metav1.ConditionTrue {
		since, told = c.LastTransitionTime.Time, c.Reason == reasonQuota
	}
	reason, message := "NoCapacity", "the pod cannot be placed in its node pool"
	if quota {
		reason, message = reasonQuota, "the pod was refused by the resource quota of the namespace"
	}
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: condCapacity, Status: metav1.ConditionTrue, Reason: reason,
		Message: message, LastTransitionTime: metav1.NewTime(now)})
	if s.Status.Phase == "" {
		s.Status.Phase = v1.SessionPending
	}
	if err := r.patchStatus(ctx, orig, s); err != nil {
		return ctrl.Result{}, err
	}

	var idle corev1.PodList
	if err := r.List(ctx, &idle, client.InNamespace(s.Namespace), client.MatchingLabels{v1.LabelComponent: v1.ComponentSession, v1.LabelState: v1.StateIdle}); err != nil {
		return ctrl.Result{}, err
	}
	if quota && !told {
		r.events.Eventf(s, nil, corev1.EventTypeWarning, reasonQuota, reasonQuota, "%s", message)
		slog.InfoContext(ctx, "the pod create was refused by the resource quota", "session", s.Name, "node_pool", gaugeName(np))
	}
	if v := claim.OldestIdle(idle.Items, np); v != nil && r.pressure.mayEvict(np, v.Name) {
		// The precondition keeps a pod that was claimed a moment ago when the cache is stale.
		err := r.Delete(ctx, v, client.Preconditions{ResourceVersion: &v.ResourceVersion})
		if err == nil {
			r.pressure.evictedPod(np, v.Name)
			telemetry.CapacityEvictions.WithLabelValues(gaugeName(np)).Inc()
			slog.InfoContext(ctx, "evicted the oldest idle instance for capacity", "session", s.Name, "node_pool", gaugeName(np))
		} else if err = client.IgnoreNotFound(ignoreConflict(err)); err != nil {
			return ctrl.Result{}, err
		}
	}

	left := r.cfg.Capacity.ExhaustedAfter.Duration - now.Sub(since)
	if left <= 0 {
		return r.fail(ctx, s, v1.ErrorCapacityExhausted, fmt.Sprintf("there is no capacity to start the session in node pool %s", gaugeName(np)))
	}
	return ctrl.Result{RequeueAfter: min(left, 2*time.Second)}, nil
}

// persistenceOf is the value of the persistence label of the startup metric.
func persistenceOf(s *v1.IDESession) string {
	if s.Spec.WorkspaceRef != "" {
		return string(v1.PersistenceVolume)
	}
	return string(v1.PersistenceNone)
}

// fail ends the start path for good and discards the instance.
func (r *sessionReconciler) fail(ctx context.Context, s *v1.IDESession, code, msg string) (ctrl.Result, error) {
	key := client.ObjectKeyFromObject(s)
	if c, ok := r.failedWritten.Load(key); ok && c == code || s.Status.Phase == v1.SessionFailed && s.Status.ErrorCode == code {
		return r.expire(ctx, s)
	}
	orig := s.DeepCopy()
	s.Status.Phase, s.Status.ErrorCode, s.Status.Message = v1.SessionFailed, code, msg
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: v1.ConditionReady, Status: metav1.ConditionFalse, Reason: camel(code), Message: msg,
		ObservedGeneration: s.Generation, LastTransitionTime: metav1.NewTime(r.clock.Now())})
	if err := r.patchStatus(ctx, orig, s); err != nil {
		return ctrl.Result{}, err
	}
	r.failedWritten.Store(key, code)
	strategy := strategyName(s.Status.StartStrategy)
	r.endStart(s, "failed", code)
	telemetry.SessionStartup.WithLabelValues(s.Spec.TemplateRef, strategy, "failed", persistenceOf(s)).Observe(r.clock.Now().Sub(s.CreationTimestamp.Time).Seconds())
	telemetry.SessionFailures.WithLabelValues(s.Spec.TemplateRef, code).Inc()
	r.note(ctx, s, corev1.EventTypeWarning, "Failed", code+": "+msg, "result", "failed", "code", code, "message", msg, "strategy", strategy)
	return r.expire(ctx, s)
}

// expire discards the pods of a failed session and deletes the session after failedRetention.
func (r *sessionReconciler) expire(ctx context.Context, s *v1.IDESession) (ctrl.Result, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(s.Namespace), client.MatchingLabels{v1.LabelSession: s.Name}); err != nil {
		return ctrl.Result{}, err
	}
	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp != nil {
			continue
		}
		if err := r.Delete(ctx, &pods.Items[i]); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
	}
	failedAt := s.CreationTimestamp.Time
	if c := meta.FindStatusCondition(s.Status.Conditions, v1.ConditionReady); c != nil && c.Status == metav1.ConditionFalse {
		failedAt = c.LastTransitionTime.Time
	}
	if left := failedAt.Add(failedRetention).Sub(r.clock.Now()); left > 0 {
		return ctrl.Result{RequeueAfter: left, Priority: lowPriority}, nil
	}
	return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, s))
}

// finalize records the workspace use. The pod goes through garbage collection.
func (r *sessionReconciler) finalize(ctx context.Context, s *v1.IDESession) (ctrl.Result, error) {
	r.forget(sessionKey(s))
	if !controllerutil.ContainsFinalizer(s, v1.SessionFinalizer) {
		return ctrl.Result{}, nil
	}
	if s.Spec.WorkspaceRef != "" {
		if err := r.touchWorkspace(ctx, s); err != nil {
			return ctrl.Result{}, err
		}
	}
	orig := s.DeepCopy()
	controllerutil.RemoveFinalizer(s, v1.SessionFinalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, s, client.MergeFrom(orig), client.FieldOwner(fieldManager)))
}

// touchWorkspace sets lastUsed to now (G19). A missing workspace is not an error.
func (r *sessionReconciler) touchWorkspace(ctx context.Context, s *v1.IDESession) error {
	ws := &v1.IDEWorkspace{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.WorkspaceRef}, ws); err != nil {
		return client.IgnoreNotFound(err)
	}
	orig := ws.DeepCopy()
	t := metav1.NewTime(r.clock.Now())
	ws.Status.LastUsed = &t
	return client.IgnoreNotFound(r.patchStatus(ctx, orig, ws))
}

// camel turns an error code such as capacity-exhausted into a condition reason such as CapacityExhausted.
func camel(code string) string {
	var b strings.Builder
	for _, p := range strings.Split(code, "-") {
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// note logs one line about the session and emits a Kubernetes Event on it. The line carries the pseudonymous user id only.
func (r *sessionReconciler) note(ctx context.Context, s *v1.IDESession, eventType, reason, msg string, attrs ...any) {
	args := append([]any{"session", s.Name, "template", s.Spec.TemplateRef, "user", telemetry.UserID(r.salt, s.Spec.Owner)}, attrs...)
	if eventType == corev1.EventTypeWarning {
		slog.WarnContext(ctx, "session "+strings.ToLower(reason), args...)
	} else {
		slog.InfoContext(ctx, "session "+strings.ToLower(reason), args...)
	}
	// The template is the related object and the pseudonymous user ends the note, so the admin API can
	// show both after a failed session is gone.
	related := &v1.IDETemplate{TypeMeta: metav1.TypeMeta{APIVersion: v1.GroupVersion.String(), Kind: "IDETemplate"},
		ObjectMeta: metav1.ObjectMeta{Name: s.Spec.TemplateRef, Namespace: s.Namespace}}
	r.events.Eventf(s, related, eventType, reason, reason, "%s (user %s)", msg, telemetry.UserID(r.salt, s.Spec.Owner))
}

// workspaceHolder returns the name of another session that holds the workspace of s, or "".
// The older session wins, so two sessions created at once do not both fail. Sessions that are deleting or failed hold nothing.
func (r *sessionReconciler) workspaceHolder(ctx context.Context, s *v1.IDESession) (string, error) {
	var l v1.IDESessionList
	if err := r.List(ctx, &l, client.InNamespace(s.Namespace), client.MatchingFields{indexWorkspaceRef: s.Spec.WorkspaceRef}); err != nil {
		return "", err
	}
	for i := range l.Items {
		o := &l.Items[i]
		if o.Name == s.Name || o.DeletionTimestamp != nil || o.Status.Phase == v1.SessionFailed {
			continue
		}
		if o.CreationTimestamp.Before(&s.CreationTimestamp) || o.CreationTimestamp.Equal(&s.CreationTimestamp) && o.Name < s.Name {
			return o.Name, nil
		}
	}
	return "", nil
}

// pushClaim sends the launch data to the agent. Keys the template does not allow are dropped and long values are cut (ADR 0012).
func (r *sessionReconciler) pushClaim(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate, pod *corev1.Pod, base, token string, data map[string]string) error {
	cctx, cancel := context.WithTimeout(ctx, agentCallBudget)
	defer cancel()
	req := agentapi.ClaimRequest{Session: s.Name, Data: r.allowedData(ctx, s, tmpl, data)}
	if tp := s.Annotations[v1.AnnotationTraceparent]; tp != "" {
		req.Traceparent = &tp
	}
	actx, aspan := tracer().Start(cctx, "agent.claim")
	defer aspan.End()
	start := time.Now()
	err := r.agent.Claim(actx, base, token, req)
	if err != nil {
		failSpan(aspan, v1.ErrorStartFailed)
	}
	r.tag(aspan, s, tmpl, pod)
	slog.DebugContext(ctx, "agent claim call", "session", s.Name, "keys", len(req.Data), "duration_ms", time.Since(start).Milliseconds(), "ok", err == nil)
	return err
}

// reclaim pushes the claim again to an agent that reports claimed=false for a Ready session.
func (r *sessionReconciler) reclaim(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate, pod *corev1.Pod, base, token string) {
	data, ok, err := r.launchData(ctx, s)
	if err != nil || !ok {
		slog.WarnContext(ctx, "cannot re-claim: launch data unavailable", "session", s.Name)
		return
	}
	if err := r.pushClaim(ctx, s, tmpl, pod, base, token, data); err != nil {
		slog.WarnContext(ctx, "re-claim after agent restart failed", "session", s.Name, "error", err.Error())
		return
	}
	r.note(ctx, s, corev1.EventTypeNormal, "Reclaimed", "pushed the launch data again after an agent restart")
}

// allowedData keeps the keys of the template allowlist and cuts values to their maxLength. Only key names are logged.
// A template that is gone leaves no allowlist to apply, so the data passes as the service validated it.
func (r *sessionReconciler) allowedData(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate, data map[string]string) map[string]string {
	if tmpl.UID == "" {
		return data
	}
	limit := map[string]int{}
	for _, k := range tmpl.Spec.InjectableKeys {
		limit[k.Name] = cmp.Or(int(k.MaxLength), 2048)
	}
	out := make(map[string]string, len(data))
	var dropped, cut []string
	for k, v := range data {
		n, ok := limit[k]
		switch {
		case !ok:
			dropped = append(dropped, k)
		case len(v) > n:
			cut = append(cut, k)
			out[k] = strings.ToValidUTF8(v[:n], "") // no half rune at the cut
		default:
			out[k] = v
		}
	}
	if len(dropped)+len(cut) > 0 {
		slices.Sort(dropped)
		slices.Sort(cut)
		slog.WarnContext(ctx, "launch data does not match the template allowlist", "session", s.Name, "dropped_keys", dropped, "truncated_keys", cut)
	}
	return out
}
