# EduIDE usage export

Aggregated usage data of one EduIDE installation.
The files hold no user, session, pod or host identifier.
Check this with `usage-export --check <directory>` before you share them.
Schema version: 1 (`schema_version` in `context.json`).

## Files

| File | Content |
|---|---|
| `weekly.csv` | One row per ISO week, template and dimension (start mode, phase, reason, pool state or pod state), one metric per row |
| `context.json` | Setup of the installation and the quality of the data |
| `README.md` | This file |

## weekly.csv

Weeks run from Monday 00:00 to Sunday 24:00 in UTC.
Only complete weeks are exported, so `days` is 7 (an export made with `--partial-weeks` may have fewer).
An empty cell means: not applicable for this metric, not measurable at this resolution, or the series does not exist on this installation (see `missing_series` in `context.json`).

| Column | Meaning |
|---|---|
| `week` | ISO week, for example `2026-W46` |
| `week_start` | First day of the covered part of the week |
| `days` | Number of days covered |
| `metric` | See the metric table |
| `template` | Template name, `all` for the whole installation, `unknown` when the series has no template label |
| `dimension` | Second grouping, see the metric table |
| `unit` | Unit of `value`, `p50`, `p95` and `max` |
| `count` | Number of sessions, launches or requests in the cell (for memory and CPU: largest number of pods seen at once) |
| `suppressed` | `true` when `count` is below 10; then only `count` is filled |
| `value` | Single figure of metrics without a distribution |
| `p50`, `p95` | Median and 95th percentile |
| `max` | Largest value; for histograms the upper bound of the highest bucket with an observation |
| `failure_share` | Share of failed sessions (`startup`) or failed agent claims (`agent_claim`) |
| `peak_hour` | Largest number of launches in one clock hour of the week |
| `peak_minute` | Largest number of launches in one clock minute |
| `max_20s`, `max_60s` | Largest number of launches in any window of 20 s or 60 s, cut at scrape samples (a window spans its length up to its length plus one scrape interval); empty when the scrape interval cannot resolve the window |

## Metrics

| `metric` | `dimension` | Meaning |
|---|---|---|
| `launches` | `created` | Launch requests that created a session; `count`, `peak_hour`, `peak_minute`, `max_20s`, `max_60s` |
| `running_sessions` | `Ready` | Sessions a user has claimed or started; per minute the maximum of the gauge, then `p50`, `p95`, `max` over the minutes of the week; `count` is the launch count of the template in the week |
| `startup` | start mode (`eager`, `lazy`, `lazy_fallback`, `none`) | Session preparation time of sessions that became ready; `count` includes failed sessions; `failure_share` is failed over all; the quantiles are suppressed when fewer than 10 ready sessions stand behind them |
| `not_ready_claim_share` | `warm-starting` | Share of warm starts (`warm-ready` and `warm-starting`) that claimed an instance whose pod was not ready yet, in `value` |
| `lazy_fallback_share` | `lazy_fallback` | Share of starts that found no unclaimed pool instance, in `value` |
| `phase` | `data_delivered`, `clone`, `ide_usable` | Late binding and IDE phases after the claim, in seconds |
| `agent_claim` | `error` | Claim requests from the operator to the agents; `failure_share` is the error share |
| `lifetime` | end reason (`timeout`, `error`, `user`) | Session lifetime in seconds |
| `pool_slot_hours` | pool state (`idle_ready`, `idle_starting`, `claimed`) | Instance-hours per state, in `value`; the template is the pool name |
| `pool_empty_hours` | `no_unclaimed` | Hours without any unclaimed instance (ready or starting), in `value` |
| `memory`, `cpu` | pod state (`idle`, `claimed`) or `all` | Working set memory in bytes, CPU in cores. `p50` is the median over the week of the per-minute median over pods, `p95` the 95th percentile over the week of the per-minute p95, `max` the largest value of any pod; weekly only |
| `restarts`, `oom_kills` | `ide_pods` | Container restarts and OOM kills of IDE pods, in `value` |

Quantiles come from histogram buckets and are as coarse as the bucket widths.
Memory and CPU rows with a template and state split need the kube-state-metrics pod labels; without them the rows have the template `all` and the dimension `all`.

## context.json

| Field | Meaning |
|---|---|
| `schema_version` | Version of the files |
| `release`, `chart_version` | EduIDE release and chart version, given with `--release` and `--chart-version` |
| `template_timeouts` | Session timeouts per template in seconds, kind `idle`, `warning` or `max` |
| `installation`, `period` | What the export covers: `from` and `to` are the inclusive days after rounding to complete ISO weeks, `requested_from` and `requested_to` what was asked for, `dropped_incomplete_weeks` the weeks that were not finished yet |
| `scrape_interval_seconds`, `scrape_interval_min_seconds`, `scrape_interval_max_seconds` | Median, minimum and maximum scrape interval of the control plane targets over the period, read from Prometheus |
| `launch_windows` | Step and method of the 20 s and 60 s windows and whether each one was resolvable |
| `pool_sizes` | `min_idle` and `max_instances` per template |
| `template_resources` | CPU and memory requests and limits per template |
| `nodes` | Count and allocatable CPU and memory of the nodes (`scope` says which nodes) |
| `data_gaps` | Days with five or more minutes without data or with a target down |
| `missing_series` | Query ids that returned no series, for example metrics that are not deployed yet |
| `missing_series_by_week` | The same per ISO week |
| `not_collected` | Context that Prometheus does not hold; add it by hand (currently the peak-hour aggregates of memory and CPU, which are not exported) |

## Small cells

A cell with fewer than 10 sessions carries its count and `suppressed=true`, nothing else.
A row without a count is suppressed too; pool hours and restarts are not per-session figures and carry no count.
The rule applies in the tool, not by hand, and `--check` verifies it.
Template names are exported as they are; per-course template names would reveal the course.
