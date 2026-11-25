# Metrics contract

Names and labels of all Prometheus metrics the control plane exposes.
The pool scaler, dashboards and alerts depend on them.
`internal/telemetry/metrics_test.go` checks that this table and the code agree.
Rules: add metrics and labels freely; renaming or removing one needs a deprecation over one release and an ADR.

| Metric | Type | Labels | Binary | Meaning |
|---|---|---|---|---|
| `eduide_session_startup_seconds` | histogram | `template`, `strategy`, `result`, `persistence`, `start_mode` | operator | Session-preparation time as the operator measures it: starts when the IDESession object is created (the launch API answers before that moment is over and the session URL reaches the browser over SSE, so the client sees a slightly different time), stops when the operator writes phase `Ready` (`result=ready`) or `Failed` (`result=failed`); `start_mode` is `eager` (`warm-ready` or `warm-starting`: a pool instance was claimed), `lazy` (`cold`, the template has no IDEPool), `lazy_fallback` (`cold` although the template has an IDEPool, so no unclaimed pool slot was found) or `none` (failed before a strategy was chosen); buckets 0.25 to 120 s. Earlier: IDESession creation until `Ready` (`result=ready`) or `Failed` (`result=failed`); `strategy` is `warm-ready`, `warm-starting` or `cold`; `persistence` is `none` or `volume` (label added with U16) |
| `eduide_session_failures_total` | counter | `template`, `code` | operator | Sessions that ended in `Failed`, by stable error code (`capacity-exhausted`, `start-failed`, `template-not-found`, `workspace-not-found`, `workspace-lost`); the admin panel counts problems per code from it (U16) |
| `eduide_session_phase_seconds` | histogram | `phase`, `strategy`, `template` | operator | One phase of the start path: `claim`, `pod_ready`, `agent_ready` (session creation to claimed, claimed to pod ready, pod ready to agent claim). Added with the trace phases ([tracing](../operations/tracing.md)): from pod timestamps `pod_scheduled`, `image_pulled`, `containers_started`, `pod_readiness` (only the part after session creation, so none for a warm-ready claim); after Ready, from agent milestones (observed once the operator sees them, up to 10 minutes after Ready) `browser_connect`, `ws_connect`, `data_delivered`, `ide_load`, `workspace_ready`, `clone`, `first_input`, `ide_usable`, with or without tracing (before 0.2.5 only for sessions with a `traceparent`). The label values are additive |
| `eduide_claim_total` | counter | `template`, `result` | operator | Claim attempts: `claimed`, `conflict` (lost the optimistic lock), `empty` (no idle instance) |
| `eduide_pool_instances` | gauge | `pool`, `state` | operator | Instances of a pool: `idle_ready`, `idle_starting`, `claimed` |
| `eduide_pool_target_idle` | gauge | `pool` | operator | Effective idle target after floor, cap and capacity pressure |
| `eduide_pool_min_idle` | gauge | `pool`, `template` | operator | `spec.minIdle` of the pool; `template` lets an export join pool to template |
| `eduide_pool_max_instances` | gauge | `pool`, `template` | operator | `spec.maxInstances` of the pool |
| `eduide_session_lifetime_seconds` | histogram | `template`, `reason` | operator | IDESession creation until the operator removes its finalizer, observed once per session; `reason` is `error` (phase `Failed`), `timeout` (ended by the idle or maximum timeout) or `user` (deleted by the user or the service; also the value after an operator restart between the timeout delete and the finalizer); buckets 60 s to 86400 s |
| `eduide_template_resources` | gauge | `template`, `resource`, `kind` | operator | CPU (`cpu`, cores) and memory (`memory`, bytes) `request` or `limit` of the IDE container; only defined values have a series; removed with the template |
| `eduide_template_timeout_seconds` | gauge | `template`, `kind` | operator | Session timeouts after `ResolvedTimeouts()`: `kind` is `idle`, `warning` or `max`; always set, removed with the template |
| `eduide_capacity_pressure` | gauge | `node_pool` | operator | 1 while a session pod in this node pool cannot be placed (G8) |
| `eduide_capacity_evictions_total` | counter | `node_pool` | operator | Idle pods deleted to make room for a session |
| `eduide_sessions` | gauge | `template`, `phase` | operator | Sessions by phase |
| `eduide_workspaces` | gauge | `phase` | operator | Workspaces by phase |
| `eduide_agent_requests_total` | counter | `op`, `result` | operator | Requests to agents: `op` is `claim`, `status`, `notify`; `result` is `ok` or `error` |
| `eduide_api_requests_total` | counter | `operation`, `code` | service | REST requests by OpenAPI operationId and HTTP status |
| `eduide_api_request_seconds` | histogram | `operation` | service | REST request duration, SSE streams excluded |
| `eduide_launch_total` | counter | `template`, `result` | service | Launches: `created`, `existing` (idempotent repeat), `rejected` |
| `eduide_authz_decisions_total` | counter | `result`, `reason` | authz | ext_authz decisions: `allow`, `deny`, `login` (redirect to sign in again) |
| `eduide_authz_check_seconds` | histogram | | authz | Duration of one ext_authz check |

Ports: operator `:9464`, service `:9090`, authz `:9090` (all `/metrics`), next to `/healthz` and `/readyz`.
No metric carries a user name, e-mail or token (G18).
