# EduIDE Pool-Scaler

> **Experimental.** Part of the [EduIDE control plane](https://github.com/EduIDE/EduIDE-control-plane)
> monorepo (`pool-scaler/`, own Go module). The interfaces may change.

Decides, per `IDEPool`, how many **free** prewarmed IDE instances an EduIDE
installation keeps ready. It writes one number, `spec.targetIdle`, through the
`/scale` subresource of the pool. The operator does the rest: it keeps that many
unused instances, refills after every claim and removes surplus ones.

```
 providers                     strategies                  guardrails                   actor
 kubernetes  -+                + static          +
 prometheus  -+                + reactive        +   floor/ceiling, reactive    PATCH idepools/scale
 artemis     -+-> snapshot ->  + predictive      +-> lower bound, step limits, -> (mode: active)
 calendar    -+                + decision-model  +   hysteresis, cooldown,
                                 (one active,         fallback chain, override,
                                  rest shadow)        budget
```

- The operator is the mechanism, the scaler is the policy.
- Without the scaler `targetIdle` stays where it is; `minIdle` is the pool floor.
- Fail-static: when the scaler cannot see the pools, it changes nothing.
- Under capacity pressure (condition `CapacityPressure` on the pool) the operator
  keeps 0 idle instances. The scaler leaves such a pool alone until it clears.

## Install

The control plane chart ships the scaler as an optional component:

```yaml
poolScaler:
  enabled: true
  mode: shadow          # start here, compare, then switch to active
  config:
    timezone: UTC
    strategies: [reactive, predictive]
    prometheus:
      url: http://prometheus-operated.monitoring:9090
```

One replica, `Recreate`, no leader election. RBAC in its namespace:
`get/list/watch` on `idepools` and `idetemplates`, `get/update/patch` on
`idepools/scale`, `create` on `events.k8s.io` events, read-only `resourcequotas`; read-only `nodes` and `pods`
cluster-wide (for the node budget).

## Events

For every change of `targetIdle` the scaler writes a Kubernetes event on the `IDEPool` (reporting controller `pool-scaler`): reason `ScaledUp` or `ScaledDown`, message with strategy, old and new value and the reason.
When a guardrail withholds a planned change it writes `Held` with the strategy, the wish and the guardrail, once until the message changes.
Shadow mode and pools without a change write nothing. The admin panel of the service shows these events as scaler decisions.

## Strategies

All strategies run every round. One is active, the others run in shadow and
export what they would have done (`pool_scaler_desired_idle{active="false"}`).

| Strategy | Recommends |
|---|---|
| `static` | A fixed value per pool (`poolLimits.<pool>.static`, else the floor). The baseline of the switchback experiment. Not lifted to reactive. |
| `reactive` | claim rate of the last `reactive.window` × p95 pool pod ready time × (1 + `safetyFactor`). Base, last fallback and lower bound for every other strategy. |
| `predictive` | Weekly profile of claims per weekday and quarter hour (EWMA over `trainWeeks`), plus an uplift around known events (participants × factor, spread over the event window). Horizon = p95 ready time + `horizonBuffer`. The forecast is a Poisson distribution over `levels`; `targetIdle` is the smallest level with P(demand ≤ level) ≥ `quantile`. Trained from Prometheus at start and every `retrainInterval`, no database. Event factors are re-estimated from past events once `minEventsForFactor` of a kind exist. |
| `decision-model` | Asks an externally hosted decision model (Open-Jev style) for a probability per demand level, given the state as text (aggregated numbers only, the predictive forecast, upcoming events). Same quantile rule. Called every `decisionModel.interval`; the answer is reused in between and dropped after two intervals. |

### Decision model contract

`DecisionClient` is the seam. `HTTPDecisionClient` posts

```json
{"question": "<state as text>", "choices": ["0", "1", "2", "...", "160+"]}
```

with `Authorization: Bearer $DECISION_MODEL_TOKEN` over https only, and
expects `{"probabilities": [...]}`, one per choice. Adapt `Decide` once the
hosted model's schema is fixed. `FakeDecisionClient` answers from a function.

An answer counts as failed on timeout, error, an invalid distribution or a
target more than `plausibilityFactor` off predictive. `breakerFailures`
failures in a row open a circuit breaker for `breakerPause`, then one test call
goes through.

## Guardrails

Applied after the strategy, in this order:

1. **Fallback chain**: active strategy → `predictive` → `reactive` → last
   valid value → floor. Every step down the chain counts in
   `pool_scaler_strategy_fallbacks_total{strategy, reason}`.
2. **Never below reactive** (except for `static`).
3. **floor / ceiling** per pool (`defaults`, `poolLimits`; the floor is at least the pool's
   `minIdle`), and never above `maxInstances`.
4. **Step limits**: at most `maxStepUp` up and `maxStepDown` down per round.
   **Hysteresis**: no scale-down by less than `hysteresis`. **Cooldown**: no
   scale-down within `scaleDownCooldown` of an increase. Floor and ceiling win
   over step limits.
5. **Budget** (below).

The guard keeps its own "current value", so shadow mode behaves exactly like
active mode would.

## Budget

Free budget = minimum of

1. the namespace ResourceQuota rest (`requests.cpu`, `requests.memory`),
2. unrequested allocatable on the nodes matching `budget.nodeSelector`,
3. `usageHeadroomFraction` × free memory by actual usage (`budget.usageQuery`,
   off when empty),

plus what the current idle pods already hold. Every floor is granted first;
the rest is shared in proportion to each deficit (`desired - floor`), rounded
down, with one scale factor on the scarcest resource. A pool pod's size is the
the IDETemplate's resource requests plus `budget.podOverhead` (the
agent). There is no reserve: real sessions push idle pods out
(operator, phase C). Cuts count in `pool_scaler_budget_clamped_total`.

## Manual control

Annotations on the `IDEPool`:

| Annotation | Effect |
|---|---|
| `eduide.github.io/scaler-mode: shadow\|active\|off` | Per pool. `shadow` computes and exports the decision but writes nothing, `active` writes `targetIdle`, `off` skips the pool. Without the annotation the global `mode` applies. An unknown value counts as `shadow`. |
| `eduide.github.io/scaler-override: "40"` | Fixed target. It bypasses strategy and guardrails; budget, `maxInstances` and the pool mode still apply. There is no expiry: remove the annotation to end it. A value that is not a non-negative integer is ignored. |

```bash
kubectl annotate idepool java-17 eduide.github.io/scaler-override=40
kubectl annotate idepool java-17 eduide.github.io/scaler-mode=off --overwrite
```

The global `mode: shadow` stops all writing.

## Switchback experiment

With `switchback.enabled`, the active strategy changes per day (in
`timezone`): every weekday runs each of `switchback.strategies` once per block
of that many weeks, in an order drawn from `seed`. Days with an exam always run
`examDayStrategy`. `switchback.schedule` pins a strategy to a date. Every
decision metric carries `active_strategy`.

## Events

- **Artemis**: `GET <artemis.url>/api/programming/internal/online-ide/upcoming?days=N`
  with `Authorization: Bearer $ARTEMIS_TOKEN`. An exam becomes one `exam`
  event; an exercise a `release` (start date, else release date) and a `due`
  event. Fetched every `artemis.interval`; a failed fetch keeps the last
  events.
- **Calendar**: a YAML file (`calendar.file`), re-read every round. See
  [examples/calendar.yaml](examples/calendar.yaml).

## Metrics

Served on `:9090/metrics`.

| Metric | Labels |
|---|---|
| `pool_scaler_desired_idle` | `pool`, `strategy`, `active`, `active_strategy` |
| `pool_scaler_target_idle` | `pool`, `active_strategy` |
| `pool_scaler_forecast_starts` | `pool`, `strategy` |
| `pool_scaler_forecast_demand_probability` | `pool`, `strategy`, `level` (P(demand ≤ level)) |
| `pool_scaler_budget` | `resource` (cpu cores, memory bytes), `limit_source` (`quota`, `nodes`, `usage`) |
| `pool_scaler_budget_clamped_total` | `pool` |
| `pool_scaler_provider_up`, `pool_scaler_provider_errors_total` | `provider` |
| `pool_scaler_strategy_errors_total` | `strategy` |
| `pool_scaler_strategy_fallbacks_total` | `strategy`, `reason`, `active_strategy` |
| `pool_scaler_override_active`, `pool_scaler_paused` | `pool` |
| `pool_scaler_last_decision_timestamp_seconds` | |
| `pool_scaler_patches_total` | `pool`, `result` |
| `pool_scaler_decisions_total` | `pool`, `source` (a strategy, `last-valid`, `floor`, `override`), `active_strategy` |
| `pool_scaler_upcoming_events` | `pool` |
| `pool_scaler_active_strategy`, `pool_scaler_mode` | `strategy`, `mode` |
| `pool_scaler_decision_model_breaker_open` | |

Every round logs one JSON line per pool with the decided value and
why.

## Configuration

[examples/config.yaml](examples/config.yaml) lists every field with its
default. Unknown fields are rejected. Secrets come from the environment:
`ARTEMIS_TOKEN`, `DECISION_MODEL_TOKEN`.

## Inputs it reads

From the control plane:

- `IDEPool` `spec.templateRef`, `minIdle`, `maxInstances`, `targetIdle`,
  `status.idleReady`, `idleStarting`, `claimed`, the `CapacityPressure`
  condition and the annotations (`eduide.github.io/v1alpha1`).
- `IDETemplate` `spec.resources.requests` (pod size for the budget).
- `eduide_claim_total` with result `claimed` or `empty` (claim rate, training history; `conflict` is a lost attempt of a retried claim and no demand) and
  `eduide_session_startup_seconds` with `strategy="cold"` (p95 ready time),
  both by `template`; the `namespace` label is the scrape target's, added by
  Prometheus. The names are the contract in
  [docs/contracts/metrics.md](../docs/contracts/metrics.md). A pool maps to
  its template through `templateRef`; pools sharing a template share its rates.
  `eduide_pool_instances` and `eduide_capacity_pressure` are not queried: the
  pool status and condition carry the same facts.

## Backtesting

```bash
go run ./cmd/pool-scaler backtest \
  --prometheus http://127.0.0.1:9090 --namespace eduide \
  --from 2026-11-01T00:00:00Z --to 2026-11-08T00:00:00Z \
  --strategies static,reactive,predictive --config examples/config.yaml
```

Replays the recorded claims step by step against each strategy with the same
guardrails (no budget) and a simulated operator that refills to `targetIdle`
with pods that need `--ready-seconds` to become ready. Reports per strategy
and pool: claims served by a ready pod, by a starting pod, cold, the
warm-ready ratio and idle pod hours. `predictive` trains once on the
`trainWeeks` before `--from`. `decision-model` calls the configured model.

## Development

```bash
go test ./...
golangci-lint run ./...
go run ./cmd/pool-scaler --config examples/config.yaml   # uses your kubeconfig
```

Keep `mode: shadow` when running against a real cluster from a laptop.

## License

[Eclipse Public License 2.0](LICENSE), like the rest of EduIDE.
