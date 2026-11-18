# 0015. Test strategy

- Status: Accepted
- Date: 2026-11-18
- Decisions: G1, G13, G18, U4, U9
- Contracts: [Makefile](../../Makefile), [api/openapi/v1.yaml](../../api/openapi/v1.yaml), [api/agent/v1.yaml](../../api/agent/v1.yaml), [docs/contracts/metrics.md](../contracts/metrics.md)

## Context and Problem Statement

Most components are built by agents in parallel work packages (G13) against fixed contracts.
Without tests on every level, integration errors surface late, and a cluster mistake can hit a real cluster.
We define what is tested where and what blocks a merge.

## Decision Drivers

- Contracts (CRD, REST, agent protocol, metrics) must hold.
- The claim, auth and controller logic is concurrency and security sensitive.
- Local cluster tests must never touch a real cluster.
- Time budgets of the start path are a requirement, not a hope ([0013](0013-observability.md)).

## Considered Options

1. Unit tests plus manual cluster checks.
2. A layered suite from unit to e2e to scale, all in CI.

## Decision Outcome

Chosen option: "layered suite in CI".

| Level | Tool | Covers |
|---|---|---|
| Unit | `go test`, table-driven | claim selection, pool math, validation, authz decision |
| Controller | envtest (real API server and etcd) | reconcile, OwnerReferences, claim conflicts, status, CEL rules |
| API contract | requests and responses validated against the OpenAPI spec | REST stability |
| e2e | kind with Envoy Gateway, Calico and Keycloak, fake IDE image | all four matrix cells ([0007](0007-persistence-modes.md)), gateway auth (owner, foreign, free, after release, no cookie), NetworkPolicy negative test, SSE, timeouts, a grep for the token in logs and spans |
| e2e nightly | kind with the real IDE image | data path, monitor, real start times |
| Scale | kwok, nightly | 1,000 sessions, 200 launches per minute, API server load. The gateway with many HTTPRoutes is measured separately with real Envoy |
| Fuzzing | Go native fuzzing | input validation, claim payload, ext_authz requests |
| Performance | start-path benchmark with trace analysis | warm start time budget as a CI gate |

- Rules: new logic comes only with tests. `claim`, `auth` and the controllers have coverage targets. Before changing an existing test, check that the test and not the implementation is wrong.
- CI (U4): `golangci-lint`, `go test` with envtest, `govulncheck`, Trivy for images and dependencies, `npm audit` and ESLint, Gitleaks, Renovate. API fidelity: OpenAPI lint, `oasdiff` breaking-change check, contract tests, CRD schema diff, `make generate` without diff (`make verify-generate`). Path filters build only affected parts.
- The Makefile targets are `make generate`, `make test`, `make lint`, `make e2e` and `make verify-generate`.
- Every local cluster command exports an isolated `KUBECONFIG`. The default kube context on a developer machine can be a real cluster. The skills [run-e2e](../../.claude/skills/run-e2e/SKILL.md) and [run-scale-test](../../.claude/skills/run-scale-test/SKILL.md) enforce it.
- The shared test deployment on stud (U9) is touched only with explicit approval and only the namespace and cluster-wide CRDs agreed there. Existing namespaces, the shared gateway and the production context stay untouched.

### Consequences

- Good: Contract drift and regressions in claim and auth fail early.
- Good: Isolation of the NetworkPolicy and the gateway auth are tested, not assumed ([0005](0005-session-auth-at-gateway.md), [0019](0019-network-isolation.md)).
- Bad: A long suite and nightly jobs. Path filters and the fake IDE image keep the per-pull-request time down.
- Follow-up: Coverage targets and flaky test handling are set after the first runs.

## Pros and Cons of the Options

### Unit plus manual

- Good, because cheap.
- Bad, because it misses concurrency, gateway and network behavior.

### Layered suite

- Good, because each risk has a test level.
- Bad, because it costs build and CI time.
