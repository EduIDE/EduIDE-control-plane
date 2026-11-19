package controller

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/agentapi"
	"github.com/EduIDE/EduIDE-control-plane/internal/claim"
	"github.com/EduIDE/EduIDE-control-plane/internal/telemetry"
)

// Tracing of the start path (ADR 0013). One span "start" runs from the creation of the session until Ready.
// It is kept in memory between reconciles, so a reconcile that changes nothing leaves no span. The phases
// below it (claim, instance.create, pod.scheduled, image.pulled, containers.started, pod.ready, agent.claim,
// status.ready) are children. After Ready the operator adds spans from the milestones the agent reports.

const (
	tracerName = "eduide-operator"
	// milestoneWindow is how long after Ready milestones still become spans. It keeps an operator restart
	// from exporting spans for sessions that have been running for hours.
	milestoneWindow = 10 * time.Minute
)

// milestoneChecks are the moments after Ready at which the operator polls the agent for milestones, on top
// of the regular status poll.
var milestoneChecks atomic.Pointer[[]time.Duration]

func init() { milestoneChecks.Store(&[]time.Duration{30 * time.Second, 120 * time.Second}) }

// startState is what the operator remembers per session for tracing. Losing it on a restart only loses spans.
type startState struct {
	span  trace.Span
	begin time.Time
}

// startTime is when the start span begins. The creation timestamp of the session has a resolution of one
// second, so a span that began there could begin before the launch that created the session. The moment the
// operator first sees the session is precise and only slightly late; if it is more than a second after the
// creation timestamp the operator was behind, and the (coarse) creation timestamp is the better answer.
func startTime(created, observed time.Time) time.Time {
	if observed.Before(created) || observed.Sub(created) > time.Second {
		return created
	}
	return observed
}

func tracer() trace.Tracer { return otel.Tracer(tracerName) }

func remoteParent(ctx context.Context, s *v1.IDESession) context.Context {
	if tp := s.Annotations[v1.AnnotationTraceparent]; tp != "" {
		return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": tp})
	}
	return ctx
}

// traceContext returns ctx joined to the trace of the launch. While the session starts, ctx holds the
// long-lived "start" span, which is created by the first reconcile that sees the session.
func (r *sessionReconciler) traceContext(ctx context.Context, s *v1.IDESession) context.Context {
	ctx = remoteParent(ctx, s)
	key := sessionKey(s)
	if st, ok := r.starts.Load(key); ok {
		return trace.ContextWithSpan(ctx, st.(*startState).span)
	}
	switch s.Status.Phase {
	case "", v1.SessionPending, v1.SessionStarting:
	default:
		return ctx
	}
	if s.DeletionTimestamp != nil {
		return ctx
	}
	begin := startTime(s.CreationTimestamp.Time, time.Now())
	ctx, span := tracer().Start(ctx, "start", trace.WithTimestamp(begin), trace.WithAttributes(r.baseAttrs(s)...))
	r.starts.Store(key, &startState{span: span, begin: begin})
	return ctx
}

func sessionKey(s *v1.IDESession) types.NamespacedName {
	return types.NamespacedName{Namespace: s.Namespace, Name: s.Name}
}

// endStart ends the start span of s with the result and, for failures, the stable error code.
func (r *sessionReconciler) endStart(s *v1.IDESession, result, code string) {
	st, ok := r.starts.LoadAndDelete(sessionKey(s))
	if !ok {
		return
	}
	span := st.(*startState).span
	span.SetAttributes(attribute.String("eduide.result", result), attribute.String("eduide.strategy", strategyName(s.Status.StartStrategy)))
	if code != "" {
		failSpan(span, code)
	}
	span.End(trace.WithTimestamp(r.clock.Now()))
}

// forget drops the tracing state of a session that is gone.
func (r *sessionReconciler) forget(key types.NamespacedName) {
	if st, ok := r.starts.LoadAndDelete(key); ok {
		sp := st.(*startState).span
		sp.SetAttributes(attribute.String("eduide.result", "deleted"))
		sp.End(trace.WithTimestamp(r.clock.Now()))
	}
	r.exported.Delete(key)
}

// failSpan marks a span as failed with the stable error code. Messages stay out: they may name launch data keys.
func failSpan(span trace.Span, code string) {
	span.SetStatus(codes.Error, code)
	span.SetAttributes(attribute.String("eduide.error_code", code))
}

func strategyName(s v1.StartStrategy) string {
	if s == "" {
		return "none"
	}
	return string(s)
}

// baseAttrs are on every span: template, persistence and the session name. Never the owner; the user is only the pseudonymous id.
func (r *sessionReconciler) baseAttrs(s *v1.IDESession) []attribute.KeyValue {
	a := []attribute.KeyValue{
		attribute.String("eduide.session", s.Name),
		attribute.String("eduide.template", s.Spec.TemplateRef),
		attribute.String("eduide.persistence", persistenceOf(s)),
	}
	if len(r.salt) > 0 {
		a = append(a, attribute.String("eduide.user_id", telemetry.UserID(r.salt, s.Spec.Owner)))
	}
	return a
}

// tag adds what is known about the placement: strategy, pool, node pool and instance. tmpl and pod may be nil.
func (r *sessionReconciler) tag(span trace.Span, s *v1.IDESession, tmpl *v1.IDETemplate, pod *corev1.Pod) {
	a := r.baseAttrs(s)
	strategy := s.Status.StartStrategy
	if strategy == "" && pod != nil {
		strategy = strategyOf(pod)
	}
	a = append(a, attribute.String("eduide.strategy", strategyName(strategy)))
	if tmpl != nil {
		a = append(a, attribute.String("eduide.node_pool", gaugeName(r.nodePool(tmpl))))
	}
	if pod != nil {
		pool := pod.Labels[v1.LabelPool]
		if pool == "" {
			pool = "none"
		}
		a = append(a, attribute.String("eduide.pool", pool), attribute.String("eduide.instance", pod.Labels[v1.LabelInstance]))
	}
	span.SetAttributes(a...)
}

// strategyOf tells how a pod reached the session: created for it, or taken from a pool ready or still starting.
func strategyOf(pod *corev1.Pod) v1.StartStrategy {
	switch {
	case pod.Labels[v1.LabelPool] == "":
		return v1.StartCold
	case claim.Ready(pod):
		return v1.StartWarmReady
	default:
		return v1.StartWarmStarting
	}
}

// phaseSpan is a span with explicit times, derived from timestamps instead of measured by running code.
type phaseSpan struct {
	name, metric string
	start, end   time.Time
}

// podPhases splits the life of a pod into the phases a person can act on, from the real timestamps in the
// pod conditions and container statuses. Kubernetes stores them with a resolution of one second.
func podPhases(p *corev1.Pod) []phaseSpan {
	cond := func(t corev1.PodConditionType) time.Time {
		for _, c := range p.Status.Conditions {
			if c.Type == t && c.Status == corev1.ConditionTrue {
				return c.LastTransitionTime.Time
			}
		}
		return time.Time{}
	}
	var first, last time.Time
	for _, cs := range p.Status.ContainerStatuses {
		var at time.Time
		switch {
		case cs.State.Running != nil:
			at = cs.State.Running.StartedAt.Time
		case cs.State.Terminated != nil:
			at = cs.State.Terminated.StartedAt.Time
		}
		if at.IsZero() {
			continue
		}
		if first.IsZero() || at.Before(first) {
			first = at
		}
		if at.After(last) {
			last = at
		}
	}
	sched, ready := cond(corev1.PodScheduled), cond(corev1.PodReady)
	return []phaseSpan{
		{"pod.scheduled", "pod_scheduled", p.CreationTimestamp.Time, sched},
		{"image.pulled", "image_pulled", sched, first}, // sandbox, image pull and container create: the pull dominates
		{"containers.started", "containers_started", first, last},
		{"pod.ready", "pod_readiness", last, ready},
	}
}

// emit starts and ends one span per phase with explicit times. Phases before notBefore (the creation of
// the session, for a pod that was idle in a pool) are cut off, and incomplete or negative phases are skipped.
func (r *sessionReconciler) emit(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate, pod *corev1.Pod, phases []phaseSpan, lo, hi time.Time) {
	for _, ph := range phases {
		if ph.start.IsZero() || ph.end.IsZero() || ph.end.Before(ph.start) {
			continue
		}
		coarse := !hi.IsZero()
		if coarse && ph.end.Before(s.CreationTimestamp.Time) {
			continue // happened before the session existed: an idle pod from a pool
		}
		if coarse {
			ph.start, ph.end = clamp(ph.start, lo, hi), clamp(ph.end, lo, hi)
		}
		_, span := tracer().Start(ctx, ph.name, trace.WithTimestamp(ph.start))
		if coarse {
			span.SetAttributes(attribute.String("eduide.timestamp_resolution", "1s"))
		}
		r.tag(span, s, tmpl, pod)
		span.End(trace.WithTimestamp(ph.end))
		telemetry.SessionPhase.WithLabelValues(ph.metric, strategyName(s.Status.StartStrategy)).Observe(ph.end.Sub(ph.start).Seconds())
	}
}

// clamp keeps t within [lo, hi]. It is monotonic, so phases keep their order.
func clamp(t, lo, hi time.Time) time.Time {
	if t.Before(lo) {
		return lo
	}
	if t.After(hi) {
		return hi
	}
	return t
}

// phaseBounds are the limits for spans from one-second pod timestamps: from the beginning of the start span
// (or the creation timestamp when the operator restarted and lost it) to now.
func (r *sessionReconciler) phaseBounds(s *v1.IDESession) (time.Time, time.Time) {
	lo := s.CreationTimestamp.Time
	if st, ok := r.starts.Load(sessionKey(s)); ok {
		lo = st.(*startState).begin
	}
	return lo, r.clock.Now()
}

// milestoneRule makes a span from two milestones: it starts at the latest of from and ends at to.
// All moments are precise times of the agent: the Ready timestamp of the status has a resolution of one second and
// would start spans before the events that cause them.
var milestoneRules = []struct {
	name, metric string
	from         []string
	to           string
}{
	{"browser.connect", "browser_connect", []string{"claim-received"}, "first-request"},
	{"ws.connect", "ws_connect", []string{"first-request"}, "first-websocket"},
	// The extension polls only once it is activated, so the delivery waits for the plugin host: the span shows the
	// wait after both the claim and the plugin host are there. The plugin host start itself is ide.load.
	{"data.delivered", "data_delivered", []string{"claim-received", "plugin-host-started"}, "data-delivered"},
	{"ide.load", "ide_load", []string{"first-request"}, "plugin-host-started"},
	{"workspace.ready", "workspace_ready", []string{"first-request", "plugin-host-started"}, "workspace-ready"},
	{"clone", "clone", []string{"workspace-ready"}, "clone-done"},
	{"first.input", "first_input", []string{"workspace-ready"}, "first-input"},
	{"ide.usable", "ide_usable", []string{"claim-received"}, "workspace-ready"},
}

// milestoneSpans turns the milestones of an agent into spans. A span whose milestones are missing or out of
// order is left out; for an instance that was idle in a pool the IDE started before the browser came.
func milestoneSpans(ms []agentapi.Milestone) []phaseSpan {
	at := map[string]time.Time{}
	for _, m := range ms {
		at[m.Name] = m.At
	}
	var out []phaseSpan
	for _, rule := range milestoneRules {
		end, ok := at[rule.to]
		if !ok {
			continue
		}
		var start time.Time
		for _, f := range rule.from {
			t, ok := at[f]
			if !ok {
				start = time.Time{}
				break
			}
			if t.After(start) {
				start = t
			}
		}
		if start.IsZero() || end.Before(start) {
			continue
		}
		out = append(out, phaseSpan{rule.name, rule.metric, start, end})
	}
	return out
}

// exportMilestones adds the spans of new milestones under the trace of the launch. The state is in memory:
// after an operator restart a milestone may become a span a second time, which Jaeger shows as a duplicate.
func (r *sessionReconciler) exportMilestones(ctx context.Context, s *v1.IDESession, tmpl *v1.IDETemplate, pod *corev1.Pod, ms []agentapi.Milestone) {
	st := &s.Status
	if len(ms) == 0 || st.Timestamps.Ready == nil || s.Annotations[v1.AnnotationTraceparent] == "" ||
		r.clock.Now().Sub(st.Timestamps.Ready.Time) > milestoneWindow {
		return
	}
	v, _ := r.exported.LoadOrStore(sessionKey(s), &map[string]bool{})
	done := v.(*map[string]bool)
	pctx := remoteParent(ctx, s)
	for _, ph := range milestoneSpans(ms) {
		if (*done)[ph.name] {
			continue
		}
		(*done)[ph.name] = true
		r.emit(pctx, s, tmpl, pod, []phaseSpan{ph}, time.Time{}, time.Time{})
	}
}

// nextMilestoneCheck is the time until the next check after Ready that is still ahead, or 0 when all are past.
func nextMilestoneCheck(now, ready time.Time) time.Duration {
	age := now.Sub(ready)
	for _, c := range *milestoneChecks.Load() {
		if c > age {
			return c - age + time.Second
		}
	}
	return 0
}
