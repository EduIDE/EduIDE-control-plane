# Usage export

Aggregated usage data of an installation for studies and capacity planning: how many sessions start, how long they take to prepare, how full the pools are, what a session costs in memory and CPU.
The export holds no user, session, pod or host identifier.

The metric names `start_mode`, `eduide_session_phase_seconds{template}`, `eduide_pool_min_idle`, `eduide_pool_max_instances`, `eduide_template_resources` and `eduide_session_lifetime_seconds` come from the usage metrics change (branch `feat/usage-metrics`).
Without it the export still runs and marks those series as missing.

## Metrics and where the timers run

| Id | Metric | Starts | Stops |
|---|---|---|---|
| M1 | `eduide_launch_total{template,result="created"}` | The service accepted a launch request and created the session | Counter, no timer |
| M1 | `eduide_sessions{template,phase="Ready"}` | - | Gauge of sessions in phase `Ready`; idle pool instances are not sessions |
| M2 | `eduide_session_startup_seconds{template,strategy,result,persistence,start_mode}` | The operator sees the new `IDESession` | The session reaches `Ready` (`result=ready`) or `Failed` (`result=failed`) |
| M3 | `eduide_pool_instances{pool,state}` | - | Gauge by `idle_ready`, `idle_starting`, `claimed` |
| M4 | `eduide_session_phase_seconds{phase,strategy,template}` | `claim`: session creation; `pod_ready`: claim; `agent_ready`: pod ready | Phases after `Ready` come from agent milestones: `data_delivered` (launch data reached the agent after the claim), `clone`, `ide_usable` |
| M4 | `eduide_agent_requests_total{op="claim"}` | - | Counter of claim requests to agents by `result` |
| M5 | `container_memory_working_set_bytes`, `container_cpu_usage_seconds_total` | - | cAdvisor series of the `ide-*` pods |
| M6 | `eduide_session_lifetime_seconds{template,reason}` | Session creation | End of the session; `reason` is `timeout`, `error` or `user` |
| M7 | `kube_pod_container_status_restarts_total`, `kube_pod_container_status_last_terminated_reason` | - | kube-state-metrics, `ide-*` pods |

The timer of M2 runs in the operator, not in the service and not in the browser.
It starts a little after the service received the request and ends before the browser has loaded the IDE, so a benchmark timer on the client reads higher by the network round trip and the IDE load.
`start_mode` is `eager` (claimed a warm instance, strategy `warm-ready` or `warm-starting`), `lazy` (cold start), `lazy_fallback` (the pool had no unclaimed instance and the launch took the cold path) or `none`.

## Recording rules

`charts/eduide-control-plane/files/usage-rules.yaml` is part of the PrometheusRule of the chart (`monitoring.rules.enabled`).
All series start with `eduide:usage:` and carry the labels `namespace` and `template` (or `namespace` alone), never a pod, instance, session or owner label.
Rules with `increase()[5m]` run in a 5 minute group, so their values do not overlap and weekly sums are exact.

| Id | Recorded series |
|---|---|
| M1 | `launches_per_minute`, `launches_max_20s_in_minute`, `launches_max_60s_in_minute`, `running_sessions`, `running_sessions_max_1m` |
| M2 | `startup_count_5m`, `startup_bucket_5m` by `start_mode` and `result` |
| M3 | `pool_instances` by `state`, `pool_unclaimed_empty` (1 while a pool has no ready and no starting instance), `lazy_fallback_share_5m` |
| M4 | `phase_count_5m`, `phase_bucket_5m` for `data_delivered`, `clone`, `ide_usable`; `agent_claim_requests_5m`, `agent_claim_errors_5m` |
| M5 | `ide_memory_bytes:{p50,p95,max}`, `ide_cpu_cores:{p50,p95}`, `ide_pods` by `template` and `state`; the same with the suffix `_all` per namespace |
| M6 | `session_lifetime_count_5m`, `session_lifetime_bucket_5m` by `reason` |
| M7 | `ide_restarts_5m`, `ide_oom_kills_5m` per namespace; `ide_restarts_by_template_5m`, `ide_oom_kills_by_template_5m` per template |

The 20 s window needs two scrapes inside it, so the scrape interval of the control plane must be 10 s or less. The chart default of `monitoring.podMonitor.interval` is therefore 10s; a PodMonitor overrides the global scrape interval of Prometheus, so a global 10 s does not help when the PodMonitor sets 30 s.
A pool carries the name of its template in the chart, so the pool rules label the pool as `template`.

### Idle and claimed pods

The split of M5 by `template` and `state` joins the pods with `kube_pod_labels`.
kube-state-metrics exposes pod labels only when you allow them:

```yaml
# kube-state-metrics values
metricLabelsAllowlist:
  - pods=[eduide.github.io/state,eduide.github.io/template]
```

Without the labels the join returns nothing.
The `_all` rules still record memory, CPU and pod count per namespace over all `ide-*` pods without the split.
The export uses the split rows when they exist and the fallback rows otherwise (template `all`, dimension `all`).

## Export command

```sh
go run ./cmd/usage-export \
  --prometheus http://127.0.0.1:9090 --namespace eduide --installation aet \
  --release 3.0.0 --chart-version 0.2.1 \
  --from 2026-11-09 --to 2026-11-15 --out ./exports
go run ./cmd/usage-export --check ./exports
```

| Flag | Meaning |
|---|---|
| `--prometheus` | Base URL; the command sends `GET /api/v1/query` only |
| `--namespace` | Namespace of the control plane and the session pods |
| `--installation` | Name in the output path (a-z, 0-9, `-`) |
| `--from`, `--to` | First and last day, inclusive, UTC (default: the last 7 days) |
| `--release`, `--chart-version` | EduIDE release and chart version of the installation; required, written to `context.json` |
| `--out` | Output directory; the command writes `<out>/<installation>/<from>_<to>/` |
| `--check DIR` | Scan `DIR` for e-mail addresses, IPv4 and IPv6 addresses, `ide-` pod names, `s-` session names, hex strings of 16 characters or more and host names; exit code 1 on a finding |

Port-forward Prometheus first (`kubectl -n monitoring port-forward svc/prometheus-operated 9090`) and run the command on a machine with read access only.

The output directory holds three files:

- `weekly.csv`: one row per ISO week, template and dimension. `README.md` in the directory explains every column.
- `context.json`: installation, release, chart version, period, session timeouts per template (`eduide_template_timeout_seconds{template,kind}` with kind `idle`, `warning`, `max`, the values of `IDETemplate.ResolvedTimeouts()`), scrape interval read from Prometheus, pool sizes, template resources, node count and allocatable CPU and memory, data gaps from `up`, series that were missing.
- `README.md`: the column reference, written by the command.

Rules the command enforces:

- A fixed list of queries in `cmd/usage-export`; no PromQL comes from flags or files.
- A cell with fewer than 10 sessions carries its count and `suppressed=true`, no distribution.
- The 20 s and 60 s launch windows are derived from `eduide_launch_total` at the scrape resolution: a window needs two scrapes. With a 30 s interval the 20 s column stays empty and `context.json` says so.
- A series that does not exist yields no rows and appears under `missing_series`.
- The same period gives the same files while Prometheus retains the data.

The share of claims on a not-ready instance is the share of `strategy="warm-starting"` among the warm starts (`warm-ready` and `warm-starting`) of `eduide_session_startup_seconds_count`.
Not exported: peak-hour aggregates of memory and CPU.
The node figures cover all nodes that kube-state-metrics knows, not only the ones that run sessions.

## Retention

| Installation | Retention | Global scrape interval | Consequence |
|---|---|---|---|
| stud | 90 days | 10 s, but the PodMonitor of the chart set 30 s | Run the export at least monthly; set `monitoring.podMonitor.interval: 10s` for 20 s windows |
| aet (production) | 90 days | 30 s | At least monthly; 20 s windows are not resolvable unless the PodMonitor sets 10s |
| parma (Bonn) | 365 days | 10 s | Quarterly is enough |

All three run rancher-monitoring.
None sets the kube-state-metrics pod label allowlist today, so M5 and the per-template restart rules stay empty until you add it (see above); the `_all` fallback rows work.

A lost month cannot be rebuilt, because the raw series expire.
Keep the exported directories in a Git repository or in the replication package.

## Alternatives

| Option | Pro | Con |
|---|---|---|
| This command, run by hand or from a scheduler outside the cluster | One tool for all installations, reads only the Prometheus API, checker and small-cell rule in one place, no new component in the cluster | Someone has to run it |
| Weekly CronJob in the cluster that writes to object storage | No human step | Needs object storage, credentials and a new workload on every installation; the small single-node installations may have neither |
| Grafana CSV export | No code | Manual per panel, no suppression, no check for identifiers, not reproducible |

Recommendation: the command, run monthly (90 day installations) or quarterly (365 days).
A CronJob can wrap the same binary later without changing the files.
