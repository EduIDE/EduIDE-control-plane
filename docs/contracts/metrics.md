# Metrics contract

Names and labels of all Prometheus metrics the control plane exposes.
The pool scaler, dashboards and alerts depend on them.
`internal/telemetry/metrics_test.go` checks that this table and the code agree.
Rules: add metrics and labels freely; renaming or removing one needs a deprecation over one release and an ADR.

| Metric | Type | Labels | Binary | Meaning |
|---|---|---|---|---|
| `eduide_session_startup_seconds` | histogram | `template`, `strategy`, `result` | operator | IDESession creation until `Ready` (`result=ready`) or `Failed` (`result=failed`); `strategy` is `warm-ready`, `warm-starting` or `cold` |
| `eduide_session_phase_seconds` | histogram | `phase`, `strategy` | operator | One phase of the start path: `claim`, `pod_ready`, `agent_ready` |
| `eduide_claim_total` | counter | `template`, `result` | operator | Claim attempts: `claimed`, `conflict` (lost the optimistic lock), `empty` (no idle instance) |
| `eduide_pool_instances` | gauge | `pool`, `state` | operator | Instances of a pool: `idle_ready`, `idle_starting`, `claimed` |
| `eduide_pool_target_idle` | gauge | `pool` | operator | Effective idle target after floor, cap and capacity pressure |
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
