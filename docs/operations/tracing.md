# Reading launch traces in Jaeger

One launch is one trace, from the click on the landing page to a usable IDE. Jaeger is not public: use a port-forward to the Jaeger of the cluster, for example `kubectl -n <ns> port-forward svc/jaeger-collector 16686:16686`, and open `http://localhost:16686`. Design and privacy rules: [ADR 0013](../adr/0013-observability.md).

## Find a launch

Service `eduide-service`, operation `launch`, then a tag filter:

| You know | Search tag |
|---|---|
| The session name (`s-1a2b3c4d5e`) | `eduide.session=<name>` |
| The user | `eduide.user_id=<pseudonym>`. It is an HMAC of the owner with a salt per deployment (`telemetry.userSaltFile`). Compute it from the operator log line of a session, which carries the same `user` field, or ask the user to send their session name from the landing page. Names and e-mail addresses are never in a span. |
| The template | `eduide.template=<name>` |
| Only failures | `error=true`, or `eduide.error_code=<code>` (`capacity-exhausted`, `start-failed`, `rate-limited`, ...) |
| Slow ones | Min duration on the operation `start` (for example `5s`) |

With the trace id (second field of the annotation `eduide.github.io/traceparent` on the session, or `trace_id` in any log line) open `/trace/<id>` directly. API: `/api/v3/traces/<id>`.

Every span carries `eduide.session`, `eduide.template`, `eduide.persistence` (`none` or `volume`) and `eduide.strategy` (`warm-ready`, `warm-starting`, `cold`, or `none` before the start path knows it). The operator spans also carry `eduide.pool` (`none` for a cold instance), `eduide.node_pool` and `eduide.instance`. `eduide.user_id` is on the service spans and on the operator `start` span. A failed span has status error and `eduide.error_code`.

## Span tree

Warm launch (an idle instance is claimed):

```
launch                      service   click to response (the landing page is the parent)
  validate                  service   template, display, launch data allowlist, persistence, session limit
  session.create            service
  secret.create             service   the data Secret with the launch data
  start                     operator  session created until Ready
    claim                   operator  attributes: idle_pods, candidates, result; events: claim.conflict, claim.won
    agent.claim             operator  launch data pushed to the agent
    status.ready            operator  status patch that makes the session Ready
  browser.connect           operator  claim received by the agent until the first proxied request (late, from agent milestones)
  ide.load                  operator  first request until the plugin host started: the IDE plugin host start
  data.delivered            operator  plugin host started (and claim received) until the extension got the launch data
  workspace.ready           operator  plugin host started until the workspace folder is open
  ide.usable                operator  claim received until the workspace is open
```

Cold launch (no idle instance, or a persistent workspace) replaces the claim with creating the pod, and adds the pod phases:

```
start
  claim                     (result=empty, only for sessions without a workspace)
  instance.create           operator  pod, Service, route, token Secret
  pod.scheduled             pod created until PodScheduled
  image.pulled              scheduled until the first container started: sandbox, image pull, container create
  containers.started        first until last container started
  pod.ready                 last container started until the Ready condition (the readiness probe: the IDE answers)
  agent.claim
  status.ready
```

A warm-starting claim (the idle pod was still starting) shows the part of the pod phases after the session was created.

Pod phases come from the real Kubernetes timestamps, which have a resolution of one second: a phase of 0 ms means "under a second". These spans carry `eduide.timestamp_resolution=1s` and are clamped into the time of `start`, so they never begin before it or end after it; their order is kept. `start` begins when the operator first sees the session (precise), or at the creation timestamp (resolution one second) when the operator was more than a second late. The late spans use the precise milestone times of the agent, not status timestamps.

**Which span shows the IDE plugin host start?** `ide.load`: from the first proxied request of the browser to the `plugin-host-started` milestone the extension reports on activation. The IDE starts its plugin host only when the browser connects, so this is the largest share of the time to a usable IDE today (about 13 s warm). `data.delivered` is the short wait after that, until the extension has polled and received the launch data. `workspace.ready` follows until the workspace folder is open. The late spans after Ready arrive up to about 2 minutes after the launch (the operator reads the agent at 30 s, at 120 s and in its regular status poll), so a fresh trace may still lack them.

Milestones: `claim-received`, `first-request`, `first-websocket`, `data-delivered` are set by the agent. `plugin-host-started`, `workspace-ready`, `first-input` are reported by the extension. `clone-done` is allowed but not reported yet: the Scorpio clone offers no hook. A span is left out when its milestones are missing or out of order. For an instance that was idle in a pool the plugin host started before the browser came, so `ide.load` is missing there.

## What is normal

Measured on test3 (prepulled images, session without persistence):

| Span | Warm | Cold |
|---|---|---|
| `launch` (server side) | about 30 ms (6 to 20 ms in the old traces) | about 30 ms |
| `claim` | under 1 ms (one outlier of 21 ms) | not present for sessions with a workspace, otherwise under 1 ms |
| `instance.create` | not present | about 2 to 30 ms (median 23 ms) |
| `pod.scheduled` to `pod.ready` | not present | together about 3 to 5 s with prepulled images |
| `agent.claim` | 1 to 8 ms | 1 to 8 ms |
| `status.ready` | 4 to 10 ms | 4 to 10 ms |
| `start` in total | about 100 ms | 3 to 5 s |

The figures for `launch`, `claim`, `instance.create`, `agent.claim` and `status.ready` are from the traces of test3 before the new spans. The pod phases and the browser phases are new: record their normal values from your own deployment before you alert on them.

Budgets: warm under 2 s, cold under 30 s at p95. The browser phases after Ready depend on the IDE and the network.

## Slow causes and the span that shows them

| Symptom | Span | Cause and next step |
|---|---|---|
| `start` long, `claim` result `empty` | `claim` | Pool empty or no pool for the template: [pool-empty.md](pool-empty.md). |
| `claim` has `claim.conflict` events, then cold | `claim` | Many launches at once for few idle pods; raise `targetIdle`. |
| `pod.scheduled` long | `pod.scheduled` | No capacity: scheduler or autoscaler, quota. Check `eduide_capacity_pressure` and the node pool. |
| `image.pulled` long (more than 10 s) | `image.pulled` | Image not prepulled on the node, slow registry. Check the prepull DaemonSet. |
| `containers.started` long | `containers.started` | Init work, volume mount (`persistence=volume`), slow node. |
| `pod.ready` long | `pod.ready` | The IDE needs long before it answers on its port. Check the IDE container logs. |
| `agent.claim` long or failing | `agent.claim` | Agent not reachable: NetworkPolicy to :8081, agent container logs. |
| `start` ends with error | `start` | The `eduide.error_code` is the stable code of the session status. |
| `validate` long | `validate` | Live session list in the API server is slow. |
| `browser.connect` long | `browser.connect` | The user opened the URL late, or the gateway route is not yet programmed ([gateway-backlog.md](gateway-backlog.md)). |
| `ide.load` long | `ide.load` | The IDE plugin host starts slowly after the browser connected (about 13 s warm today). Check the IDE container resources and the plugins it loads. |
| `workspace.ready` long | `workspace.ready` | The workspace folder opens late: clone or import is slow (the clone itself is not a span yet). |
| `data.delivered` long | `data.delivered` | The extension is active but its poll is slow to return the data: check the agent log. |

The same phases are in the metric `eduide_session_phase_seconds{phase,strategy}` ([metrics](../contracts/metrics.md)). The authz service adds `authz.check` (attributes `eduide.authz_result` and `eduide.authz_reason`) only below a sampled `traceparent`, so it appears when a request carries the trace of a launch, not for ordinary traffic.

## Limits

- The operator keeps the `start` span in memory. When the operator restarts during a start, that start has no `start` span and its children hang directly below `launch`. After a restart, milestone spans of sessions that became Ready in the last 10 minutes may appear twice.
- The agent has no network egress by design. It never exports spans; it only stores milestone timestamps in memory (at most 8) and the operator reads them.
- Reconciles that change nothing leave no span.
