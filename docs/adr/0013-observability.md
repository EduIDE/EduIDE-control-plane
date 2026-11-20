# 0013. Observability with OpenTelemetry, Prometheus and Jaeger v2

- Status: Accepted
- Date: 2026-11-18
- Decisions: G1, G9, G17
- Contracts: [docs/contracts/metrics.md](../contracts/metrics.md), [internal/telemetry](../../internal/telemetry), [internal/config/config.go](../../internal/config/config.go) (`Telemetry`), [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go) (`AnnotationTraceparent`)

## Context and Problem Statement

The old stack measures only the time from the launch request to the returned URL.
The time the user waits after that (route, IDE load, data bridge) is unmeasured, and the operator metrics are not scraped on stud.
The pool scaler depends on metric names that drifted from the agreement.
We need one measurable start path with a small footprint.

## Decision Drivers

- Time budgets per phase (warm below 2 s, cold below 30 s p95) as a gate.
- Few extra components per cluster and per deployment.
- No personal data in telemetry.

## Considered Options

1. Keep Micrometer-style metrics and Sentry.
2. OpenTelemetry in code, Prometheus endpoint, traces to Jaeger v2, logs as JSON.
3. A collector per deployment in front of the trace store.

## Decision Outcome

Chosen option: "OpenTelemetry in code, Prometheus endpoint, Jaeger v2".

- Metrics: every binary exposes `/metrics` (operator `:9464`, service and authz `:9090`). Rancher Monitoring scrapes through ServiceMonitor or PodMonitor. Names and labels are fixed in [docs/contracts/metrics.md](../contracts/metrics.md) and checked by a test; renaming needs one release of deprecation and an ADR. Histograms exist per phase and per start strategy.
- Traces: the OTel SDK exports OTLP over gRPC directly to one Jaeger v2 per cluster, with no collector per deployment. Badger storage on a small PVC, retention 7 days. Jaeger is not public: port-forward or admin auth (G17). The OTLP endpoint is configured per deployment (`telemetry.otlpEndpoint`), so an own Jaeger needs no code change.
- Trace context: the landing page sends `traceparent`. The service stores it as the annotation `eduide.github.io/traceparent` on the session. The operator continues the trace with one long span `start` from session creation until Ready, held in memory between reconciles. Reconciles that change nothing leave no span. The start phases are children: `claim` (candidates, chosen pod and conflicts as span events), `instance.create`, `pod.scheduled`, `image.pulled`, `containers.started`, `pod.ready` (the last four from pod conditions and container statuses with their real timestamps), `agent.claim`, `status.ready`. The service adds children of `launch`: `validate`, `workspace.get_or_create`, `session.create`, `secret.create`. Every span has the template, strategy, persistence and session name, the operator spans also pool, node pool and instance id. An error is the span status plus the stable error code (`eduide.error_code`).
- Trace into the session: the agent has no network egress, so it exports no spans. It keeps milestone timestamps in memory (`claim-received`, `first-request`, `first-websocket`, `data-delivered`, and the ones the extension reports on the local listener with `POST /v1/milestones`: `plugin-host-started`, `workspace-ready`, `clone-done`, `first-input`; first report per name wins, at most 8) and returns them in `GET /v1/status`. The operator turns new milestones into spans with explicit start and end times under the `launch` span (`browser.connect`, `ws.connect`, `data.delivered`, `ide.load`, `workspace.ready`, `first.input`, `ide.usable`), on its regular status poll and 30 s and 120 s after Ready. Late spans use the precise milestone times of the agent (the status timestamps have a resolution of one second). `start` begins when the operator first sees the session, and spans derived from one-second pod timestamps are clamped into `start` and marked `eduide.timestamp_resolution=1s`. Late spans are fine for Jaeger. The metric `eduide_session_phase_seconds` has the same phases. Reading guide: [operations/tracing.md](../operations/tracing.md).
- Sampling: start-path traces 100 percent (low volume), reconcile traces parent-based at a low rate.
- Privacy (G17): spans, logs and metrics hold no e-mail addresses or names, only a pseudonymous user id (hash with a salt per deployment, `telemetry.userSaltFile`) and the session id. Tokens never appear ([0012](0012-user-data-secrets-allowlist.md)).
- Logs: `log/slog` as JSON on stdout with `trace_id`. The controller-runtime logger is bridged to it in operator, service and authz. The operator logs one line per session event (claim, create, ready with duration and strategy, failed with code, ended by idle or max, eviction, workspace expiry) and emits Kubernetes Events on the IDESession. Lines hold the pseudonymous user id, never launch data.
- Service names in traces: `eduide-operator`, `eduide-service`, `eduide-authz`. authz adds an `authz.check` span only when the traceparent of the request is sampled, with the decision and reason as attributes. The agent has no spans (see above).
- No Sentry in the control plane (G9). The browser Sentry of the landing page stays optional, and Sentry could later receive OTLP.
- Dashboards and alerts are code in the chart: start latency per strategy, claim results, pool state, error rates, authz decisions.
- Load targets (G1) as provisional numbers: 1,000 concurrent sessions per deployment, 200 launches per minute in a burst, control plane below 150 MiB per deployment. Phase 0 measures the production baseline and the scale tests check the targets ([0015](0015-test-strategy.md)).
- The skill [debug-session-launch](../../.claude/skills/debug-session-launch/SKILL.md) shows how to read a launch trace.

### Consequences

- Good: One trace explains a slow start from click to ready. Metric names are a tested contract.
- Good: Little to run: one Jaeger per cluster, no collectors.
- Bad: Without a collector, an unreachable Jaeger drops spans (the SDK drops, it does not block).
- Bad: The `start` span lives in operator memory: an operator restart during a start loses it, and milestone spans may repeat for sessions that became Ready shortly before. Milestones are a clock of the pod, not of the browser; the clone by Scorpio is not measured (no hook).
- Bad: Dropping Sentry loses its grouping of errors in the control plane. Logs with `trace_id` replace it.
- Follow-up: The scaler uses the new names, and the Prometheus setup scrapes all three binaries.

## Pros and Cons of the Options

### Old metrics and Sentry

- Good, because exists.
- Bad, because blind after the URL and duplicate prefixes in metric names.

### OTel with Jaeger v2

- Good, because one standard for traces and metrics, small footprint.
- Bad, because a new component per cluster.

### Collector per deployment

- Good, because buffers and fans out.
- Bad, because memory per deployment for no current need.

## Amendment 2026-11-20: launch is the root span

The landing page exports no spans, so its `traceparent` as the parent left about three of four launch traces without their root: Jaeger marked them incomplete and listed them by trace id.
The service now starts `launch` as a new root span and keeps the incoming `traceparent` as a span link.
The annotation `eduide.github.io/traceparent` carries the context of `launch`, so operator, agent milestones and authz still join the trace.
