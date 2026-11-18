---
name: run-scale-test
description: Use when running or evaluating the kwok scale scenarios (1000 sessions, launch burst, API server load) or measuring the gateway with many HTTPRoutes. Enforces an isolated KUBECONFIG.
---

# Run a scale test

Rules:
- Use a local kind cluster with kwok only. The default kube context can be a real cluster, so every command runs with an exported isolated `KUBECONFIG`.
- Never point load at a shared or production cluster.

Setup and guard:
```sh
export KUBECONFIG=$(pwd)/.e2e/kubeconfig
kubectl config current-context   # must be a kind context
kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}'   # must be https://127.0.0.1:<port>
```
Stop if the check fails.

Targets (ADR 0013, 0015): 1,000 concurrent sessions per deployment, a burst of 200 launches per minute, control plane below 150 MiB per deployment.

Steps:
1. Create or reuse the kind cluster (see run-e2e) and install kwok. kwok simulates nodes and pods without a kubelet, so the IDE itself is not tested.
2. Run the scenarios in `test/scale/` (for example `go test ./test/scale/... -v -timeout 60m` with the exported `KUBECONFIG`). Scenarios: fill 1,000 sessions, a launch burst against a pool, pool refill, mass release.
3. Evaluate: API server request rate and latency, operator workqueue depth and reconcile time, `eduide_claim_total` (look at the `conflict` share), `eduide_session_startup_seconds`, operator and authz memory. Compare with the targets above.
4. Gateway with many HTTPRoutes is measured separately with real Envoy (not kwok). If the route count hurts, the fallback is one route per pool with header matching ([ADR 0004](../../../docs/adr/0004-gateway-api-only.md)).
5. Record numbers in the pull request or in `docs/operations/`. A regression against the budgets is a bug.
6. Clean up: `kind delete cluster --name <the cluster you created>` with the isolated `KUBECONFIG` exported.
7. Nightly CI runs the same scenarios. Do not weaken a budget to make a run pass.
