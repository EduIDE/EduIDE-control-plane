---
name: run-e2e
description: Use when running the end-to-end tests on a local kind cluster (Envoy Gateway, Calico, Keycloak, fake IDE), including the variant matrix and the NetworkPolicy negative test. Enforces an isolated KUBECONFIG.
---

# Run the e2e tests

Rules:
- The default kube context on developer machines can be a real cluster. Every command in this skill runs with the isolated kubeconfig. Never run kubectl, helm or kind without it.
- Tests are mandatory for new behavior. A failing e2e test is fixed by checking first whether the implementation is wrong, not by editing the test.

Setup (once per shell):
```sh
export KUBECONFIG=$(pwd)/.e2e/kubeconfig
mkdir -p .e2e
```

Guard before changing anything:
```sh
kubectl config current-context        # must be kind-eduide-e2e
kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}'   # must be https://127.0.0.1:<port>
```
If the context is not `kind-eduide-e2e` or the server is not 127.0.0.1, stop.

Steps:
1. `make e2e` creates the kind cluster `eduide-e2e` (if missing, written to `.e2e/kubeconfig`), installs Envoy Gateway, Calico, Keycloak and the charts, builds the images, loads them into kind and runs `test/e2e`. Check the Makefile for the exact flow.
2. The suite covers the matrix: `none` cold, `none` warm (pool), `volume` cold, `volume` with pool (cold start, `startStrategy: cold`). It also covers gateway auth (owner, foreign user, free instance, after release, no cookie gives a login redirect and never IDE content), the NetworkPolicy negative test (a foreign pod cannot reach the IDE or the agent control port), SSE, timeouts, and a search of logs and spans for the launch token.
3. To run one case: `go test ./test/e2e/... -run <Name> -v` with the same `KUBECONFIG`.
4. On failure: `kubectl -n <ns> get idesession,idepool -o yaml` and operator and authz logs (see debug-session-launch). Keep the cluster for inspection.
5. Clean up: `kind delete cluster --name eduide-e2e` with the isolated `KUBECONFIG` exported. Never delete a cluster that is not named `eduide-e2e`.
6. Nightly runs with the real IDE image are a CI job, not a local default.
