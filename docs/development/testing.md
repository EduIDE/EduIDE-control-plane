# Testing

Strategy: [ADR 0015](../adr/0015-test-strategy.md). New logic needs tests.

## Test pyramid

| Level | Tool | Covers | Command |
|---|---|---|---|
| Unit | `go test`, table-driven | Claim selection, pool math, validation, authz decision | `make test-go` |
| Controller | envtest (real API server and etcd) | Reconcile behavior, owner references, claim conflicts, status | `make test-go` |
| API contract | Requests and responses validated against the OpenAPI spec; `oasdiff` breaking-change check in CI | REST stability | CI, `make test-go` |
| e2e | kind with Envoy Gateway, Calico, Keycloak and the fake IDE image | The four variants of the matrix, gateway auth (owner, foreign user, free instance, after release, no cookie), NetworkPolicy negative test, SSE, timeouts, no launch token in logs and spans | `make e2e` |
| Scale | kwok (simulated nodes and pods) | 1000 sessions, launch burst, API server load | skill `run-scale-test` |
| Fuzzing | Go native fuzzing | Input at trust boundaries, ext_authz requests, payload parsing | `go test -fuzz` |
| Performance | Start path benchmark with trace evaluation | Time budgets as CI gate for warm start. **Planned.** | CI |
| TypeScript | npm test | Generated client, extension, landing page | `make test-ts` |
| Charts | `helm lint`, template tests | Charts | `make helm-lint` |
| Vulnerabilities | `govulncheck`, Trivy, `npm audit`, Gitleaks | Dependencies, images, secrets in the history | CI on change; `govulncheck` and the Trivy filesystem scan also nightly |

No workflow runs `make e2e` or the scale scenarios; run them locally before a release.
The job `ci-ok` fails when any CI job fails. It is the one check to require on `main`.

Matrix of the e2e suite: `none` cold, `none` warm (pool), `volume` cold, `volume` with pool (starts cold, `startStrategy: cold`).

## Run

```bash
make test        # all Go modules and TypeScript packages
make lint
```

envtest needs binaries. The Makefile downloads them:

```bash
KUBEBUILDER_ASSETS="$(go tool setup-envtest use 1.37.0 -p path --bin-dir $(pwd)/bin/envtest)" go test ./internal/controller/...
```

## Isolated KUBECONFIG rule

Every local cluster command runs with `KUBECONFIG` pointing to the kind kubeconfig in `.e2e/`, and the guard from [setup.md](setup.md) must pass first.
`make e2e` sets it for you. For manual runs:

```bash
export KUBECONFIG=$(pwd)/.e2e/kubeconfig
kubectl config current-context     # kind-eduide-e2e
make e2e
go test ./test/e2e/... -run <Name> -v
kind delete cluster --name eduide-e2e
```

Never run kubectl, helm or kind against a cluster you did not create for the test. Never delete a cluster that is not `eduide-e2e`.
The skill `run-e2e` in [.claude/skills](../../.claude/skills) has the full steps.

## Rules

- A failing test is not edited first. Check whether the implementation is wrong.
- Launch data (tokens, Git credentials) never appears in logs, traces, metrics or status. The e2e suite searches for it.
- Controllers use envtest where Kubernetes behavior matters. Pure logic (`internal/claim`) uses table-driven unit tests.
- Metric names and labels follow [docs/contracts/metrics.md](../contracts/metrics.md); a test checks that table against the code.
