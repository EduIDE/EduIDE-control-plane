# Runbook: gateway backlog

Symptom: new sessions are `Ready` but the URL returns 404 or 503 for a while, or route changes take long to apply. Typical during a burst, because every cold session and every pool instance adds an HTTPRoute.

Claims and releases change no gateway state. Only creating pods (pool refill, cold starts) adds routes ([ADR 0004](../adr/0004-gateway-api-only.md)).

## Metrics

| Metric | Look for |
|---|---|
| `eduide_session_phase_seconds{phase="agent_ready"}` and `eduide_session_startup_seconds` | Startup fine in the operator, but users still wait |
| `eduide_pool_instances{state="idle_starting"}` | Many instances created at once |
| `eduide_launch_total`, `eduide_api_requests_total` | Burst size |
| Envoy Gateway metrics of the cluster (outside this repo) | xDS update latency, config push errors, memory of the Envoy pods |

**Planned:** a dashboard panel for HTTPRoute count per deployment comes with the chart.

## Checks

```bash
kubectl -n <ns> get httproute | wc -l
kubectl -n <ns> get httproute ide-<instance> -o jsonpath='{.status.parents}{"\n"}'
kubectl -n <gateway-ns> get pods
kubectl -n <gateway-ns> logs deploy/envoy-gateway --since=15m | tail -50
kubectl -n <ns> get gateway,securitypolicy
```

The route status shows `Accepted` and `ResolvedRefs` when the gateway took it.

## Causes and fixes

| Cause | Sign | Fix |
|---|---|---|
| Too many routes created at once | Many `idle_starting`, route `Accepted` lags | Lower the pool refill rate: reduce `minIdle` jumps, let the scaler raise `targetIdle` stepwise; prewarm well before the exam |
| Total route count too high | Hundreds of routes per deployment, several deployments on one gateway | Reduce `maxInstances` and idle targets; measure with the real gateway. **Planned fallback:** one route per pool with header or path matching. |
| Gateway pods short on memory or CPU | Restarts, OOM | Raise Envoy Gateway resources (cluster admin) |
| Route not accepted | `status.parents` shows a reason | `parentRef` wrong: check `gateway.name`, `gateway.namespace`, `gateway.sectionName`; hostnames not allowed by the listener |
| 503 from the gateway while the pod is ready | Backend Service has no endpoint yet | `kubectl -n <ns> get endpoints ide-<instance>`; wait for pod readiness; check the agent `/readyz` |
| 404 on the instance path | Route not yet programmed, or the first path segment is wrong | Compare the URL segment with `status.instance`; wait and retry |

Never edit routes by hand: the operator reconciles them back.
