# Runbook: pool is empty

Symptom: launches are slow (cold start instead of under 2 s), sessions show `startStrategy: cold` or `warm-starting`, users wait at exam start.

## Metrics

From [docs/contracts/metrics.md](../contracts/metrics.md):

| Metric | Look for |
|---|---|
| `eduide_pool_instances{pool,state}` | `idle_ready` at 0 while `claimed` is high |
| `eduide_pool_target_idle{pool}` | Lower than expected (capacity pressure sets it to 0) |
| `eduide_claim_total{result="empty"}` | Rising rate |
| `eduide_capacity_pressure{node_pool}` | 1 means a pod cannot be placed |
| `eduide_session_startup_seconds{strategy}` | `cold` and `warm-starting` dominate |

The alerts of the control plane chart fire on empty claims and on a pool at 0 idle. **Planned:** alert names are set by the chart.

## Checks

```bash
kubectl -n <ns> get idepool
kubectl -n <ns> get idepool <pool> -o jsonpath='{.spec}{"\n"}{.status}{"\n"}'
kubectl -n <ns> get pods -l eduide.github.io/state=idle,eduide.github.io/pool=<pool>
kubectl -n <ns> describe pod <idle-pod>
kubectl -n <ns> get events --sort-by=.lastTimestamp | tail -30
kubectl -n <ns> logs deploy/<operator> --since=15m | grep -i -E 'pool|claim|capacity'
```

## Causes and fixes

| Cause | Sign | Fix |
|---|---|---|
| Target too low for the burst | `idleReady` 0, `targetIdle` = `minIdle` | Raise `minIdle` in the chart values before an exam. The pool scaler raises `targetIdle` on its own, check that it runs. |
| Pool scaler down or in override | `scaler-mode` or `scaler-override` annotation on the `IDEPool`, no pool-scaler pod | Restart the pool scaler, or remove the override annotation. |
| Capacity pressure | `eduide_capacity_pressure` is 1, pods `Unschedulable` or quota errors | Add nodes or quota. The operator keeps all pools of that node pool at 0 idle while pressure lasts and fails sessions with `capacity-exhausted` after `capacity.exhaustedAfter` (45 s). A pod that fits no node for another reason (the scheduler message names no `Insufficient` resource, for example a volume node affinity conflict) fails its session with `start-failed` after the same time and causes no pressure. |
| Idle pods not ready | `idleStarting` high | Image pull slow or failing (`ImagePullBackOff`): check the registry, enable prepull (`placement.prepull.enabled`). Agent readiness failing: `kubectl logs <pod> -c agent`. |
| `maxInstances` reached | `replicas` equals `maxInstances` | Raise `maxInstances`. |
| Session has a workspace | `startStrategy: cold` with persistence `volume` | Expected: sessions with a volume always start cold ([ADR 0007](../adr/0007-persistence-modes.md)). |
| Lost claims | `eduide_claim_total{result="conflict"}` very high | Workers collide on the same pod. Normal in a burst; if it stays high, report it ([ADR 0009](../adr/0009-claim-protocol.md)). |

Emergency scale without the scaler:

```bash
kubectl -n <ns> scale idepool <pool> --replicas=<n>
```

The scale subresource maps to `targetIdle`. The operator caps it at `maxInstances`.
