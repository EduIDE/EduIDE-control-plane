# 0009. Claim protocol with optimistic locking

- Status: Accepted
- Date: 2026-11-18
- Decisions: G4, G8, G14
- Contracts: [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go) (`LabelState`, `StateIdle`, `StateClaimed`), [api/v1alpha1/idesession_types.go](../../api/v1alpha1/idesession_types.go), [api/agent/v1.yaml](../../api/agent/v1.yaml), [docs/contracts/metrics.md](../contracts/metrics.md) (`eduide_claim_total`)

## Context and Problem Statement

Many launches arrive at once at an exam start (target burst: 200 per minute).
Several sessions must never take the same idle pod, and a claim must not need a lock service or a gateway change.

## Decision Drivers

- No double assignment, also with several reconcile workers.
- Idempotent: a repeated reconcile does not claim a second pod.
- Warm start below 2 s p95.

## Considered Options

1. A lock (Lease or ConfigMap) around the pool.
2. A queue in the operator that serializes claims.
3. Label patch with a `resourceVersion` precondition, retry with the next candidate.

## Decision Outcome

Chosen option: "label patch with `resourceVersion` precondition".

1. The session controller receives the IDESession through the informer and picks the oldest idle pod in phase Ready of the matching pool from the cache (`eduide.github.io/state=idle`, pod ready).
2. It patches the pod: `eduide.github.io/state=claimed`, the session and owner-hash labels, with the `resourceVersion` of the cached pod as precondition.
3. A conflict means somebody else won. The controller takes the next candidate. There are no locks and no double assignment.
4. After a successful patch the operator sends exactly one `POST /v1/claim` to the agent with the data from the session Secret ([0006](0006-agent-sidecar.md), [0012](0012-user-data-secrets-allowlist.md)).
5. When the agent answers, the operator sets `timestamps.agentReady`, then the phase `Ready` with `status.url`. authz sees `Ready` through its informer and lets the owner in ([0005](0005-session-auth-at-gateway.md)).
6. With no idle candidate, the session starts cold, or waits `warm-starting` for an idle pod that is still starting. The result is in `status.startStrategy` (`warm-ready`, `warm-starting`, `cold`).

Rules:

- The claim is idempotent. If a pod already carries the session label, the controller continues from there. The agent accepts the same session twice and answers 409 for another session.
- The claim changes labels only. The Service, HTTPRoute and instance token exist since the pod was created and do not change ([0004](0004-gateway-api-only.md)).
- Under capacity pressure the claim does not wait for idle pods ([0008](0008-idepool-scale-bare-pods.md)).
- Release removes access first: the session leaves `Ready` (authz denies at once), then the operator discards the pod. Open WebSockets end with it.
- Launch idempotency (G14) is enforced by the service with the label `eduide.github.io/launch-key` ([0010](0010-rest-api-spec-first.md)).
- Claim results are counted in `eduide_claim_total` as `claimed`, `conflict` and `empty`. Phase durations go to `eduide_session_phase_seconds`.

### Consequences

- Good: Lock-free, horizontally safe, and one API write per attempt.
- Good: Pure selection logic in `internal/claim` is easy to test table-driven.
- Bad: Under a burst, workers collide on the same oldest pod and retry. Choosing the oldest first is deliberate; a randomized start index is the follow-up if conflicts show in `eduide_claim_total`.
- Follow-ups: envtest covers conflicts and idempotence ([0015](0015-test-strategy.md)).

## Pros and Cons of the Options

### Lock

- Good, because simple to reason about.
- Bad, because another component and a hot spot.

### Serializing queue

- Good, because no conflicts.
- Bad, because it caps throughput and breaks with several operator replicas on failover.

### Optimistic patch

- Good, because the API server is the arbiter.
- Bad, because retries under contention.
