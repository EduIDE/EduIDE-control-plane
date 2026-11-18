# 0008. `IDEPool` as its own CRD with `/scale`, bare pods

- Status: Accepted
- Date: 2026-11-18
- Decisions: G8, U7
- Contracts: [api/v1alpha1/idepool_types.go](../../api/v1alpha1/idepool_types.go), [docs/contracts/metrics.md](../contracts/metrics.md) (`eduide_pool_*`, `eduide_capacity_*`), [internal/config/config.go](../../internal/config/config.go) (`Placement`, `Capacity`)

## Context and Problem Statement

Prewarming needs a desired number of idle instances per template.
Helm owns the template, and the pool scaler wants to change the idle count at runtime.
Today the scaler counts pods itself, and a Deployment plus ReplicaSet exist per instance.
Capacity pressure (a session pod that cannot be scheduled or hits quota) must not leave idle pods hogging the room.

## Decision Drivers

- No field ownership conflicts between Helm and the scaler.
- Fewer API objects per instance.
- Works with HPA and KEDA through `/scale`.
- Predictable behavior under capacity pressure, replacing the old error 555.

## Considered Options

1. Pool fields inside `IDETemplate`.
2. Separate `IDEPool` with scale subresource, pool pods managed through ReplicaSets or Deployments.
3. Separate `IDEPool` with scale subresource, pool controller manages bare pods.

## Decision Outcome

Chosen option: "separate `IDEPool` with `/scale` and bare pods".

- `IDEPool` has `templateRef`, `minIdle` (floor, set by Helm), `targetIdle` (set by the scaler) and `maxInstances`.
  The operator keeps `clamp(max(minIdle, targetIdle), 0, maxInstances - claimed)` idle instances, and 0 under capacity pressure.
- `/scale` maps to `spec.targetIdle` and `status.replicas`. Status also has `idleReady`, `idleStarting`, `claimed` and conditions, so the scaler reads numbers and counts no pods.
- Pool instances are bare pods. The pool controller replaces the ReplicaSet. This saves two API objects per instance.
- Every pool pod carries the annotation `eduide.github.io/spec-hash` (template spec, agent image, placement). The pool deletes idle pods with another hash: starting ones at once, ready ones only while the pool has more ready idle pods than its target. While old pods are left, the pool keeps one pod above its target, so each old ready pod goes once a replacement is ready and the ready count never drops during a roll. Claimed pods are never touched: every delete of the pool carries the resourceVersion as precondition, so a pod claimed a moment ago survives a stale cache.
- Idle pods with a restarted container, or with a container that waits for more than 60 s with an error that waiting does not change, are deleted and recreated: `InvalidImageName`, `CreateContainerConfigError`, `CrashLoopBackOff`, and pull errors whose message says the image is not found or access is denied. Other pull errors (timeouts, rate limits) keep the pod: the kubelet retries them. A claim never takes such a pod. A claimed pod without a live session is deleted after 30 s.
- The pool controller repairs the Service and HTTPRoute of idle pods. A pod without its token Secret is deleted once it has started or its route carries the old token, because the token cannot be restored. A pod that has not started yet, after a create that failed half way, gets a new token.
- Only the oldest `IDEPool` of a template is active. Another pool of the same template gets `Ready=False` with reason `DuplicateTemplate` and keeps 0 idle pods.
- Capacity pressure (G8): when a session pod is `Unschedulable` or hits quota, the operator deletes the oldest idle pod of any pool in the same node pool and holds those pools at 0 idle while the pressure lasts.
  After 45 s (`capacity.exhaustedAfter`) without room the session fails with `capacity-exhausted`.
  PriorityClass is not used: the priority of a claimed pool pod cannot change, and quota pressure triggers no preemption.
- Node pools (U7): a placement profile per deployment (node selector, tolerations, affinity), overridable per template. Pressure and eviction are per node pool, identified by the configurable node label key and recorded in the label `eduide.github.io/node-pool`. Image prepull runs only on nodes of the matching pool.
- Pressure shows in `eduide_capacity_pressure`, `eduide_capacity_evictions_total` and `eduide_pool_target_idle`. The pool scaler only plans.
- Scaler annotations `eduide.github.io/scaler-mode` and `eduide.github.io/scaler-override` live on the `IDEPool`.
- The claim itself is in [0009](0009-claim-protocol.md).

### Consequences

- Good: Helm and the scaler own different objects. HPA and KEDA work.
- Good: Two API objects less per instance.
- Bad: The operator reimplements what a ReplicaSet does (create, replace failed, delete surplus). Controller tests in envtest cover it ([0015](0015-test-strategy.md)).
- Bad: The operator deletes free pods under pressure, and only the same node pool, so cross-pool pressure is not solved.
- Follow-up: The pool scaler is adapted to `IDEPool` and the new metric names.

## Pros and Cons of the Options

### Pool fields in the template

- Good, because one object.
- Bad, because Helm and scaler fight over fields.

### ReplicaSets or Deployments

- Good, because Kubernetes handles replacement.
- Bad, because of two extra API objects per instance and slower claim bookkeeping.

### Bare pods

- Good, because the fewest objects and full control of claim selection.
- Bad, because the operator owns replacement logic.
