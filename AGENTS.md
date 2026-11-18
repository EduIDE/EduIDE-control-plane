# AGENTS.md

Instructions for coding agents (and humans) working in this repository.
`CLAUDE.md` points here.

## What this is

The EduIDE control plane in Go: operator, service, authz and agent, plus the pool scaler, the landing page, the IDE extension `eduide-session`, the Helm charts, docs and ADRs.
It replaces `EduIDE-Cloud` (Java) and `workspace-garbage-collector`.
Status: **experimental**.

## Layout

| Path | Content | Owner binary or package |
|---|---|---|
| `api/v1alpha1/` | CRD Go types, CEL rules, labels and annotations (`labels.go`) | contract |
| `api/openapi/v1.yaml` | REST API v1, source for server stubs and TypeScript client | contract |
| `api/agent/v1.yaml` | Protocol operator to agent and extension to agent | contract |
| `internal/config/` | Typed deployment config (`config.yaml` from a ConfigMap) | contract |
| `internal/telemetry/` | Logging, tracing, metrics; metric names in `docs/contracts/metrics.md` | contract |
| `cmd/operator`, `internal/controller`, `internal/claim` | Template, pool, session and workspace controllers | operator |
| `cmd/service`, `internal/httpapi`, `internal/auth` | REST API, OIDC, launch, SSE, read-only admin API (`admin*.go`), public status (`public_status.go`) | service |
| `cmd/authz`, `internal/authz` | ext_authz gRPC for session access | authz |
| `cmd/agent`, `internal/agent`, `internal/agentapi`, `internal/agentclient` | Sidecar in every session pod | agent |
| `extensions/eduide-session/` | IDE extension, talks to the agent on localhost | IDE |
| `charts/` | `eduide-control-plane-crds` (per cluster), `eduide-control-plane` (per deployment), `eduide-landing-page` | Helm |
| `pool-scaler/` | Pool scaler, own Go module (imported with history via `git subtree`) | pool-scaler |
| `landing-page/` | Landing page (imported with history via `git subtree`) | landing page |
| `clients/typescript/` | Generated TypeScript client `@eduide/control-plane-client` | generated |
| `config/` | Generated CRD YAML and RBAC | generated |
| `test/e2e`, `test/scale`, `test/fake-ide` | kind e2e, kwok scale tests, minimal IDE image | tests |
| `docs/` | `adr/`, `architecture/`, `contracts/`, `operations/`, `development/` | docs |

## Commands

| Command | Does |
|---|---|
| `make generate` | CRD YAML, deepcopy, RBAC, REST stubs, agent types, TypeScript client |
| `make verify-generate` | Fails if generated files are stale (CI) |
| `make test` | Unit and envtest tests of all Go modules, TypeScript tests |
| `make lint` | golangci-lint |
| `make build` / `make images TAG=x` | Binaries / images |
| `make e2e` | kind cluster with Envoy Gateway, Calico, Keycloak; full matrix |
| `make helm-lint` | Lint all charts |

## Terminology

Use exactly these words: **Template** (`IDETemplate`), **Pool** (`IDEPool`), **Instance** (a session pod, idle or claimed), **Session** (`IDESession`), **Workspace** (`IDEWorkspace`), **Claim** (assigning an idle instance to a session), **Release** (ending a session), **launch data** (the Secret with user data).
API group and label prefix: `eduide.github.io`.

## Rules

- Generated code (`*.gen.go`, `zz_generated*.go`, `config/`, `clients/typescript/src/schema.gen.ts`) is never edited by hand. Change the source and run `make generate`.
- API and CRD changes are additive within a version. Anything else needs an ADR (`docs/adr/`, skill `write-adr`) and the versioning rules in ADR 0010 and 0011.
- The contracts (`api/`, `internal/config`, `internal/telemetry/metrics.go`, `docs/contracts/`) change only with an explicit decision; work packages build against them.
- New logic comes with tests. Table-driven unit tests; envtest for controllers.
- No commit or push without asking the maintainer.
- **Isolate `KUBECONFIG`** for every local cluster command: `export KUBECONFIG=$(pwd)/.e2e/kubeconfig`, then check `kubectl config current-context` and that the server is `127.0.0.1` before changing anything. The default context on developer machines can be a production cluster. Never rely on a wrapper that sets it only for one command in a pipe.
- No university-specific defaults in the repo (hosts, realms, node labels). They live in values outside the repo.
- Launch data (tokens, Git credentials) never appears in CR specs, logs, traces, metrics or status (G18).
- Admin API responses show users only as `ownerHash` and launch data only as key names; the public `GET /status` answers only the fields of its allowlist test (ADR 0021).

## Writing style for docs

English, active voice, present tense, short sentences.
No em dashes or en dashes; use a hyphen.
Name the component (landing page, service, operator) instead of generic layer words.
