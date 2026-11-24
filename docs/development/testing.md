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

## Git accounts e2e

The Git accounts feature (ADR 0023) has its own run inside `make e2e`.
`run.sh` first runs the whole suite with the feature off, then installs the chart again with `test/e2e/values-git.yaml` (one provider, one extra egress rule for git-proxy) and runs the tests named `TestGit*` (`test/e2e/git_test.go`).
`E2E_RUN=TestGitAccounts make e2e` runs only that phase.

The provider is `test/fake-git`, a Go program (standard library only, image with `git`) behind the Service `fake-git` in the e2e namespace.
Its name `fake-git.eduide-e2e.svc` is the provider host, so the service and git-proxy reach it at `https://fake-git.eduide-e2e.svc` on port 443.
Its certificate comes from a CA of its own (`.e2e/certs-git`, ConfigMap `fake-git-ca`, `caConfigMapRef` in the values), which tests that setting too.
The fake does what GitLab does for the flow:

- `/oauth/authorize` redirects at once to `redirect_uri` with `code` and `state`; the test reads the `Location` header and never follows it.
- `/oauth/token` checks PKCE (S256), the redirect URI and single use of the code; the refresh token grant rotates: a used refresh token answers `invalid_grant`.
- Access tokens live 75 seconds (`FAKE_GIT_ACCESS_TTL`), so the refresh test needs one wait of about a minute.
- `/oauth/revoke` invalidates the whole grant, `/api/v4/user` answers id 4242, login `e2euser`.
- Git smart HTTP through `git http-backend` for `course/ex1` (private, the session repository), `course/other` (private, the account may read it) and `open/public` (anonymous reads). Private repositories and every push need basic auth with user `oauth2` and a valid access token.
- A second listener (port 9000, plain HTTP, not used by any control plane pod) serves `/_test/state`: counters (authorizes, exchanges, refreshes, invalid refreshes, revokes), per repository the requests, the basic auth users, valid token requests, rejections, pushes and the last pushed ref, and the issued codes and tokens. The tests reach it with `kubectl port-forward` and use the values only to search logs and containers for leaks. The fake logs method and path, never a query or a header.

The fake IDE (`test/fake-ide`) acts like the extension: it reads `GET /v1/git/hosts` from the agent and writes the `url.<agent>/git/<host>/.insteadOf` entries.
Its endpoint `POST /git-test?target=session|second|public` runs a fixed clone, commit and push and returns exit codes and scrubbed stderr only. It is reachable through the gateway by the session owner only, like the other endpoints of the fake IDE.

| Test | Covers |
|---|---|
| `feature is on` | git-proxy ready, `GET /status` lists the provider, `GET /git/providers` lists it unconnected |
| `denied at the provider` | `access_denied` gives `git-denied` |
| `preflight before connect`, `preflight after connect` | `connect`, `ok`, `no-access`, `not-a-provider` |
| `another user cannot complete my flow` | the state of another user gives `git-invalid-state` and stores nothing; the owner completes the same flow |
| `session with a connected account` | identity (`GIT_USER`, `GIT_MAIL`), clone, commit and push of the session repository, second private repository denied with the rule message, push elsewhere denied, session pod cannot reach the provider, refresh with rotation after expiry, disconnect gives the connect message within 8 seconds (git-proxy caches allowed answers for 5 seconds), no token, code or state in the IDE container, logs, pod specs and launch data |
| `session with a connected account` (approval) | repository approval: refusal of the second private repository carries the approval link (session, encoded repository, `access=read`, no token); PUT `read` makes the clone work and keeps the push denied with an `access=write` link; PUT `write` makes the push work (the fake sees a valid credential); GET lists session repository and approval; another user gets 404; http URL, credentials in the URL and a query give `invalid-input`, an unknown host `git-not-a-provider`; the pod cannot approve (`POST /try-approve` of the fake IDE sends a PUT without a token to the service and to the agent Git path, status codes only) and the approved list stays as it is; DELETE makes the clone fail again |
| `session without an account` | public clone works, private read gives the connect message |
| `idle instance cannot use the proxy` | the agent of an unclaimed instance answers 403 |
| `foreign pods cannot reach git-proxy or the internal port of the service` | NetworkPolicy negative test with a positive control |

Not covered: the approval modes `read` and `off` (they need another `helm upgrade` with `config.gitAccounts.approval.mode`); the service unit tests cover them.

The unit tests of the two helper programs run with `cd test/fake-git && go test ./...` and `cd test/fake-ide && go test ./...` (own Go modules, so `make test` does not run them).

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
