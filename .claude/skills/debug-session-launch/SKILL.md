---
name: debug-session-launch
description: Use when a session starts slowly, fails or never becomes ready, or a user gets 403 or a redirect loop on a session URL. Read status timestamps and startStrategy, find the trace in Jaeger, check typical causes.
---

# Debug a session launch

Rules:
- Read only, unless the user asks for a change. Never print user data or tokens from Secrets or logs.
- Check which cluster the kubeconfig points to before the first command: `kubectl config current-context`. On a developer machine the default can be a production cluster. Use an isolated `KUBECONFIG` for local clusters. For a real cluster act only with the user's approval and only read.

Steps:
1. Read the session: `kubectl -n <ns> get idesession <name> -o yaml`.
   - `status.phase`: Pending, Starting, Ready, Failed. `status.errorCode` and `status.message` on Failed.
   - `status.startStrategy`: `warm-ready` (idle pod claimed), `warm-starting` (waited for a starting idle pod), `cold` (no pool, a persistent workspace, or pool empty).
   - `status.timestamps`: `claimed`, `podReady`, `agentReady`, `ready`. The largest gap is the slow phase. Compare with the budgets: warm under 2 s, cold under 30 s p95.
2. Find the trace: read the annotation `eduide.github.io/traceparent` (the trace id is the second field). Open Jaeger (port-forward to the Jaeger of the cluster, it is not public) and search by that trace id. Spans: claim, pod ready, agent ready, ready.
3. Match the slow or failing phase to a typical cause:
   - Pool empty: `kubectl -n <ns> get idepool` (`idleReady` 0, `claimed` at `maxInstances`) and metric `eduide_claim_total{result="empty"}`. Fix: raise `targetIdle` or `maxInstances`, check the scaler.
   - Capacity pressure: pod `Unschedulable` or quota error, `eduide_capacity_pressure` is 1, `errorCode: capacity-exhausted` after 45 s. Check node pool resources and quota.
   - Image pull: `kubectl -n <ns> describe pod <pod>` shows `ImagePullBackOff` or a long pull. Check the template image and the prepull DaemonSet.
   - Agent not ready: `agentReady` missing. Check the agent container logs and `/readyz` (the IDE must answer on its loopback port). A 409 on claim means another session holds the pod.
   - Authz 403 on the URL: the session is not `Ready`, the first path segment is not its `status.instance`, or the token's user claim differs from `spec.owner`. Check `eduide_authz_decisions_total{reason}` and authz logs. A redirect loop: ID token cookie invalid, see [ADR 0005](../../../docs/adr/0005-session-auth-at-gateway.md).
   - Route not programmed: HTTPRoute `status` conditions, gateway logs.
   - Launch rejected: `session-limit-reached`, `rate-limited` or `unknown-key` from the API.
4. Logs carry `trace_id`: filter operator, service and authz logs by it.
5. Report the phase, the cause and the evidence (timestamps, condition, metric). Propose a fix, do not apply it unasked.
