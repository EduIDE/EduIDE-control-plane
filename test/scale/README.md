# Scale scenarios with kwok

These scenarios load the control plane with many simulated sessions (ADR 0015, level "Scale").
They are not part of `make e2e`. They run nightly once the control plane is complete.
Status: written against the contracts, not yet run green, because the operator did not exist when this was written.

## What kwok simulates

[kwok](https://kwok.sigs.k8s.io) fakes nodes and pods: the pods get status `Running` and `Ready` without any container.
That is enough to measure what grows with the object count:

- Operator reconcile throughput and memory (informers) with 1,000 sessions and pods.
- Load on the API server (request counts by verb, from `/metrics`).
- Number of `HTTPRoute` objects (one per instance, ADR 0004).
- Time from creating an `IDESession` to phase `Ready`.

What it does not show: the agent claim and the activity polling (the fake pod IPs are not reachable), the start time of a real IDE, and the gateway behavior with many routes.
The gateway is measured separately with a real Envoy (ADR 0015), because kwok routes do not reach an upstream service.

## Scenarios

| Scenario | Does |
|---|---|
| `sessions1000` | Creates 1,000 `IDESession` objects with their data Secrets at once, waits until all are `Ready` (10 minutes at most), prints the result. |
| `burst200` | Creates 200 sessions per minute for 3 minutes, waits one more minute, prints the result. |

Both create the sessions directly (the REST service limits sessions per user and would need 300 users), so the service itself is not loaded.
The service is covered by its own load test.

## Run

```sh
E2E_INFRA_ONLY=1 ./test/e2e/run.sh      # or make e2e: cluster, Calico, Envoy Gateway, Keycloak
# install the charts as in run.sh, then:
./test/scale/run.sh all                 # or sessions1000 | burst200
```

Settings: `SCALE_NODES` (20 fake nodes, 110 pods each), `SCALE_SESSIONS` (1000), `SCALE_BURST_PER_MINUTE` (200), `SCALE_BURST_MINUTES` (3).
The script installs kwok `v0.8.0` into the kind cluster of the e2e harness and creates the fake nodes with the taint `kwok.x-k8s.io/node=fake`.
It sources `test/e2e/safe.sh`, so it refuses to run against any cluster except the local kind cluster (`KUBECONFIG=.e2e/kubeconfig`).
Delete everything with `make e2e-down`.

## Output

The script prints the sessions by phase, pod and route counts, p50, p95 and max of the time to `Ready`, and the API server request counts by verb.
Compare the numbers between runs; there are no absolute gates yet.
