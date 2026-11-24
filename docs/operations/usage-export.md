# Usage export

Aggregated usage data of an installation for studies and capacity planning: how many sessions start, how long they take to prepare, how full the pools are, what a session costs in memory and CPU.
The export holds no user, session, pod or host identifier.

All usage figures are defined once, as Prometheus recording rules (`eduide:usage:*`).
The export command and the Grafana board "EduIDE usage" read the same recorded series, so the board shows what the export writes.

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
Counts are exact counter deltas, not `increase()`: `increase()` extrapolates to the window edges and drops the first increment of every new series (a counter appears at 1 on its first `Inc`).
A delta is the last value in the window minus the value before the window; a series that starts inside the window, or restarts at zero, counts from zero.
A delta loses the increments before a counter reset inside its window (1 or 5 minutes); resets come only from pod restarts, so the loss is at most what one window held when a pod restarted.
A scrape gap longer than the look-back of 15 minutes puts the whole count of a series into one window; `data_gaps` in the export lists such gaps.
Joins with `kube_pod_labels` fold the series of several kube-state-metrics replicas with `max` first.
A rule with a 5 minute delta runs in a 5 minute group, so consecutive windows tile without overlap.
Gauges that both operator pods report during a rollout are folded with `max` over the pods, never summed.

| Id | Recorded series |
|---|---|
| M1 | `launches_per_minute`, `launches_max_20s_in_minute`, `launches_max_60s_in_minute` by template and with the suffix `_all` for the installation, `running_sessions`, `running_sessions_max_1m`, `running_sessions_all_max_1m` |
| M2 | `startup_count_5m` by `start_mode`, `result` and `strategy`, `startup_bucket_5m` by `start_mode` and `result` |
| M3 | `pool_instances` by `state`, `pool_unclaimed_empty` (1 while a pool has no ready and no starting instance) |
| M4 | `phase_count_5m`, `phase_bucket_5m` for `data_delivered`, `clone`, `ide_usable`; `agent_claim_requests_5m`, `agent_claim_errors_5m` |
| M5 | `ide_memory_bytes:{p50,p95,max}`, `ide_cpu_cores:{p50,p95}`, `ide_pods` by `template` and `state`; the same with the suffix `_all` per namespace |
| M6 | `session_lifetime_count_5m`, `session_lifetime_bucket_5m` by `reason` |
| M7 | `ide_restarts_5m`, `ide_oom_kills_5m` by template: container restarts of `ide-*` pods, and the restarts whose last termination was an OOM kill; kube-state-metrics replicas folded with `max` per pod and container |

`cmd/usage-export/rules_test.go` runs the rules through `promtool test rules` with the cases in `cmd/usage-export/testdata/` (new series, operator rollout, two kube-state-metrics replicas, OOM kill).

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

Since 2026-11-20 the allowlist is active on stud, parma and aet.
It was added by hand, not through a Helm upgrade, because the values of the rancher-monitoring release differ from the live Prometheus resources and an upgrade would reset them:

```sh
kubectl -n cattle-monitoring-system patch deploy rancher-monitoring-kube-state-metrics --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--metric-labels-allowlist=pods=[eduide.github.io/state,eduide.github.io/template]"}]'
```

The deployment carries the annotation `eduide.github.io/note`.
A rancher-monitoring upgrade drops the flag: add it again, or set `kube-state-metrics.metricLabelsAllowlist` in the Rancher app values.
On stud and parma `kube_pod_labels` carries only these two labels.

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
| `--from`, `--to` | First and last day, inclusive, UTC (default: the last 7 days). The command rounds `--from` down to a Monday and `--to` up to a Sunday and exports only complete ISO weeks (Monday 00:00 to Sunday 24:00 UTC); weeks that end after now are dropped and listed in `context.json` |
| `--partial-weeks` | Also export the incomplete current week; its rows carry `days` below 7 |
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
- The command reads only recorded series for the weekly figures; raw series are read for the context alone (pool sizes, template resources and timeouts, nodes, scrape interval, `data_gaps`). Counts are sums of the recorded deltas over the week (`sum_over_time`), so they inherit the exactness and the limits of the rules.
- `peak_minute` is the largest `launches_per_minute`, `peak_hour` the largest sum of `launches_per_minute` in a clock hour, `max_20s` and `max_60s` the largest value of the window rules in the week. A window needs two scrapes, otherwise its column stays empty.
- Restarts and OOM kills (M7) are the sums of `ide_restarts_5m` and `ide_oom_kills_5m`. Pods without a template label get the template `unknown`.
- `data_gaps` covers the control plane targets and kube-state-metrics, starting with the hour before the period (the look-back of the delta rules reaches into it).
- Pool slot-hours are the sum of the per-minute `pool_instances` samples divided by 60. During an operator handover the old pod's series can end before the new one's starts; each handover can lose up to one minute, so the hours are slightly too low.
- Running sessions are the per-minute maximum of the gauge, with the operator pods folded by `max`; their `count` is the launch count of the template in the week.
- The scrape interval comes from the control plane targets (operator, service, authz) over the whole period; `context.json` holds the median, minimum and maximum. `data_gaps` looks at the same targets.
- A cell with fewer than 10 sessions carries its count and `suppressed=true`, no distribution. A row without a count is suppressed as well, except restarts, OOM kills and the hours of idle pool slots, which are not per-session figures. Hours of claimed slots and hours without an unclaimed slot take the launch count of the template as their count, because they would otherwise show the load of a template with few sessions. For `startup` the quantiles come from ready sessions, so they are also suppressed when fewer than 10 ready sessions stand behind them, even when the count including failed sessions is larger.
- A week without a series gets a row with count 0 when the series exists, and `missing_series_by_week` lists the ids that returned nothing that week.
- `--check` also verifies the small-cell rule in `weekly.csv`.
- Template names are exported as they are. If an installation has one template per course, the names reveal the course; rename or merge them before sharing.
- With a 30 s scrape interval the 20 s column stays empty and `context.json` says so.
- A series that does not exist yields no rows and appears under `missing_series`.
- The same period gives the same files while Prometheus retains the recorded series.

The share of claims on a not-ready instance is the share of `strategy="warm-starting"` among the warm starts (`warm-ready` and `warm-starting`) of `eduide_session_startup_seconds_count`.
Not exported: peak-hour aggregates of memory and CPU.
The node figures cover all nodes that kube-state-metrics knows, not only the ones that run sessions.

## Retention

| Installation | Retention | Global scrape interval | kube-state-metrics pod labels | Consequence |
|---|---|---|---|---|
| stud | 90 days | 10 s, but the PodMonitor of the chart set 30 s | Allowlist set by hand on 2026-11-20 | Run the export at least monthly; set `monitoring.podMonitor.interval: 10s` for 20 s windows |
| aet (production) | 90 days | 30 s | Allowlist set by hand on 2026-11-20 | At least monthly; 20 s windows are not resolvable unless the PodMonitor sets 10s |
| parma (Bonn) | 365 days | 10 s | Allowlist set by hand on 2026-11-20 | Quarterly is enough |

All three run rancher-monitoring.
The allowlist is a manual patch: a rancher-monitoring upgrade drops it (see above).
Rules and exports that need the labels give empty M5 rows after such an upgrade until the flag is back.

Recorded series exist only from the moment the rules are deployed, and they expire with the same retention as the raw series.
The usage archive keeps them beyond that (next section).
Keep the exported directories in a Git repository or in the replication package.

### Usage archive

`usageArchive.enabled` in the control plane chart (needs `monitoring.rules.enabled`) adds a daily CronJob and a PersistentVolumeClaim, both named `<fullname>-usage-archive` (for release `eduide`: `eduide-eduide-control-plane-usage-archive`; long names are shortened).
Each run of `usage-export` writes two things to the volume:

| Path | Content |
|---|---|
| `/archive/raw/<namespace>/YYYY-MM-DD.om.gz` | Every sample of every `eduide:usage:*` series of the namespace for that UTC day, as gzipped OpenMetrics |
| `/archive/weekly/<installation>/<week>/` | The weekly export of the last complete week, as described above |

A run archives every day among the last `usageArchive.days` (default 85, keep it close to the retention) that has no file yet, so missed nights are caught up on the next run.
Days without samples get no file and are tried again; an empty yesterday fails the job, because then the rules do not reach this Prometheus.
A failed day fails the job, the other days are still written.
The alert `EduIDEUsageArchiveStale` fires when the CronJob has not succeeded for 36 hours.
`helm uninstall` keeps the volume (`helm.sh/resource-policy: keep`); delete it by hand only after copying the files.

The volume alone is not a backup: deleting the namespace or losing the cluster deletes it, and with the reclaim policy `Delete` of most storage classes the data goes with it.
Copy the archive off the cluster regularly (command below) and keep the copies with the replication package.
Point `prometheusURL` at a single Prometheus: behind a Service with several replicas a query can hit one with a gap, and that day is then archived with the gap.

The flags are usable without the chart: `usage-export --prometheus <url> --namespace <ns> --archive <dir> --archive-days 90` archives only; add `--out` and the export flags for both.

Copy the files with a pod that mounts the volume while no job runs (the image has no shell).
The chart shortens long release names, so look the claim name up instead of building it:

```sh
NS=<namespace>
PVC=$(kubectl -n $NS get pvc -l app.kubernetes.io/component=usage-archive -o jsonpath='{.items[0].metadata.name}')
kubectl -n $NS run usage-archive-read --restart=Never --image=busybox:1.37 \
  --overrides='{"spec":{"volumes":[{"name":"a","persistentVolumeClaim":{"claimName":"'$PVC'"}}],"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"r","image":"busybox:1.37","command":["sleep","600"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}},"volumeMounts":[{"name":"a","mountPath":"/archive","readOnly":true}]}]}}'
kubectl -n $NS wait --for=condition=Ready pod/usage-archive-read
kubectl -n $NS exec usage-archive-read -- tar czf - -C /archive --exclude=./lost+found . > usage-archive.tgz
kubectl -n $NS delete pod usage-archive-read
gzip -t usage-archive.tgz && tar tzf usage-archive.tgz | wc -l
```

`lost+found` is left out: an ext4 volume has it, the job user cannot read it, and `tar` would fail on it. Stop if `kubectl exec` or `gzip -t` fails; the file is then incomplete.

Load raw days back into a TSDB, for example a local Prometheus for a new export:

```sh
gunzip -c 2026-11-22.om.gz > day.om
promtool tsdb create-blocks-from openmetrics day.om blocks/
```

### Backfill

For the time before the rules were deployed, evaluate them on the raw data that Prometheus still holds and load the result as blocks.
`promtool` evaluates every group at `--eval-interval`, not at the group interval, so backfill each group on its own; a 5 minute rule evaluated every minute counts five times.

```sh
sed 's/__NAMESPACE__/eduide/g' charts/eduide-control-plane/files/usage-rules.yaml | (echo groups:; cat) > rules.yml
# split rules.yml at "- name: eduide-usage-5m" into rules-1m.yml and rules-5m.yml (each starting with "groups:")
promtool tsdb create-blocks-from rules --url http://127.0.0.1:9090 --start 2026-11-16T00:00:00Z --end 2026-11-21T00:00:00Z --eval-interval 1m --output-dir blocks rules-1m.yml
promtool tsdb create-blocks-from rules --url http://127.0.0.1:9090 --start 2026-11-16T00:00:00Z --end 2026-11-21T00:00:00Z --eval-interval 5m --output-dir blocks rules-5m.yml
```

End the backfill before the deployment of the rules, so the blocks do not overlap the series that Prometheus records itself.
Copy the blocks into the data directory of Prometheus; this needs write access to its volume.
Without it, run a local Prometheus on the blocks and point the export at it (`--prometheus http://127.0.0.1:9091`); the backfill itself only reads the query API.

## Grafana board

`charts/eduide-control-plane/files/usage-dashboard.json` ("EduIDE usage", uid `eduide-usage`) ships in the dashboard ConfigMap with `monitoring.dashboard.enabled`.
It has one row per metric M1 to M7 and reads only `eduide:usage:*` series; every panel says what it counts in its description.
The board shows small cells: the small-cell rule applies to the export, not to the board.

## Alternatives

| Option | Pro | Con |
|---|---|---|
| This command, run by hand or from a scheduler outside the cluster | One tool for all installations, reads only the Prometheus API, checker and small-cell rule in one place, no new component in the cluster | Someone has to run it |
| Weekly CronJob in the cluster that writes to object storage | No human step | Needs object storage, credentials and a new workload on every installation; the small single-node installations may have neither |
| Grafana CSV export | No code | Manual per panel, no suppression, no check for identifiers, not reproducible |

Recommendation: the command, run monthly (90 day installations) or quarterly (365 days).
A CronJob can wrap the same binary later without changing the files.
