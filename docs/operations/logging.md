# Logs

Every component logs one JSON object per line on stdout.
Common fields: `time`, `level`, `msg`, `service` (`eduide-operator`, `eduide-service`, `eduide-authz`, `eduide-agent`, `eduide-pool-scaler`, `eduide-landing-page`).
When a request carries a sampled trace, the line also has `trace_id`.
Users appear as `user`, a pseudonym (HMAC-SHA256 with the deployment salt, 16 hex characters).
Logs never contain tokens, cookies, launch data values, owner names, or e-mail addresses.
Launch data shows up as key names only.

## Levels

`LOG_LEVEL` is `debug`, `info` (default) or `warn`.
Debug lines cost one level check when off.

| Component | Chart value | Effect of a change |
|---|---|---|
| operator, service, authz | `operator.logLevel`, `service.logLevel`, `authz.logLevel` | Rolling restart of that Deployment |
| pool-scaler | `poolScaler.logLevel` | Restart of the pool-scaler |
| agent | `config.agent.logLevel` (sets `AGENT_LOG_LEVEL` on new session pods) | Operator restarts; only pods created afterwards get the level |
| landing page | `LOG_LEVEL` env of the landing page container | Restart |

The level is read at start, so a change always needs a restart.
To debug one component for a short time without a chart change:

```sh
kubectl -n <ns> set env deploy/<release>-eduide-control-plane-authz LOG_LEVEL=debug
kubectl -n <ns> set env deploy/<release>-eduide-control-plane-authz LOG_LEVEL=info   # back
```

The next `helm upgrade` resets the value to the chart's.

## Lifecycle lines (all components)

| msg | Fields |
|---|---|
| `starting` | `version` (VCS revision), listen addresses, non-secret settings (`deployment`, `namespace`, hosts, `oidc_issuer`, `otlp_endpoint_set`, `log_level`) |
| `ready` | `detail` (caches synced, OIDC discovered, first decision round done, serving) |
| `shutdown` | `reason` (`signal`, `server stopped`, `manager stopped`) |

An error that ends the process is logged before exit (`service failed`, `authz failed`, `operator failed`, `agent failed`, `exiting`).

## service

One `request` line per API call at info: `method`, `route` (mux pattern), `op` (operationId), `status`, `duration_ms`, `bytes`, `user` when authenticated.
`/healthz`, `/readyz` and `/metrics` are not logged.
Event streams log `stream open` and `stream close` (with `duration_ms`).
5xx is level error with `error` (the problem body); 4xx is info.
`launch` lines keep their fields (`session`, `template`, `result`, `code`, `user`).
Debug adds `launch validated`, `launch rejected` (with the problem detail), idempotent launch hits, `launch rate limit` (tokens left), and `stream subscribed` with subscriber counts.

## authz

`authz decision` with `result` (`allow`, `deny`, `login`), `reason`, `host`, `duration_ms`, `user`.
`deny` and `login` are info, at most 20 per reason and minute; the first line of the next minute carries `suppressed`.
`allow` is debug only.
Warnings: `oidc discovery failed, retrying`, `jwks fetch failed`.
Info: `ready` for OIDC discovery and for the session cache.

## agent

Info: `claimed` and `re-claimed` (`session`, `keys`), `claim refused`, `notice queued` (`kind`), `ide port reachable` and `ide port unreachable` (state changes only), `token check failed` (`endpoint`, rate limited).
Warn: `proxy upstream error` (rate limited).
Debug: `proxy request` (`method`, first path segment as `prefix`, `status`, `duration_ms`), `heartbeat`, `long poll finished`, `readiness probe`.

## operator

Controller-runtime and client-go errors go through the same logger, once per failure.
Info lines mark state changes only (`session ready`, `workspace expired`, quota refusals), never one line per reconcile.
Debug adds `reconcile session`, `claim candidates`, `claim conflict`, `claim won`, `agent claim call` (`duration_ms`), `status patch` (the diff), `pool sizing`, `pool create` and `pool delete` with the reason, and `workspace ttl`.

## pool-scaler

Info: one `decision` line per pool and round (`targetIdle`, `mode`, `why`), `active strategy` changes, `predictive trained`.
Debug: `pool inputs` and `strategy recommendation` per pool.
