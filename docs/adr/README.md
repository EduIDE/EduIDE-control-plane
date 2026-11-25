# Architecture decision records

Decisions are written as ADRs in [MADR](https://adr.github.io/madr/) format, one file per decision.
The tables [decisions.md](decisions.md) list the grilling decisions G1 to G19 and the implementation decisions U1 to U14 and link each to its ADRs.

| Nr. | Title | Status |
|---|---|---|
| [0001](0001-adr-madr.md) | Architecture decisions as ADRs (MADR) | Accepted |
| [0002](0002-go-single-module-four-binaries.md) | Go, controller-runtime, one module, four binaries with their own images | Accepted |
| [0003](0003-own-crd-group.md) | Own CRD group, no compatibility with theia.cloud | Accepted |
| [0004](0004-gateway-api-only.md) | Gateway API only, routes for pool instances created up front | Accepted |
| [0005](0005-session-auth-at-gateway.md) | Session auth at the gateway: SecurityPolicy (OIDC) per deployment and ext_authz, no oauth2-proxy | Accepted |
| [0006](0006-agent-sidecar.md) | Agent sidecar as the only data path into the pod | Accepted |
| [0007](0007-persistence-modes.md) | Persistence modes `none` and `volume`, matrix with prewarming, `snapshot` as later extension | Accepted |
| [0008](0008-idepool-scale-bare-pods.md) | `IDEPool` as its own CRD with `/scale`, bare pods | Accepted |
| [0009](0009-claim-protocol.md) | Claim protocol with optimistic locking | Accepted |
| [0010](0010-rest-api-spec-first.md) | REST spec-first, `/api/v1`, Problem Details, versioning rules | Accepted |
| [0011](0011-crd-versioning-cel.md) | CRD versioning and CEL instead of an admission webhook | Accepted |
| [0012](0012-user-data-secrets-allowlist.md) | User data only in Secrets, allowlist for injectable keys | Accepted |
| [0013](0013-observability.md) | Observability with OpenTelemetry, Prometheus and Jaeger v2 | Accepted |
| [0014](0014-operator-service-per-deployment.md) | One operator and one service per deployment | Accepted |
| [0015](0015-test-strategy.md) | Test strategy | Accepted |
| [0016](0016-activity-detection.md) | Activity detection through the agent (pull by the operator) | Accepted |
| [0017](0017-migration-parallel-run.md) | Migration by parallel run per deployment | Accepted |
| [0018](0018-charts-oci.md) | Charts in the same repo, published as OCI | Accepted |
| [0019](0019-network-isolation.md) | Network isolation of session pods with NetworkPolicy, default deny | Accepted |
| [0020](0020-landing-page-data-in-the-api.md) | Start estimates, image metadata, lifecycle times and one-call delete in the API | Accepted |
| [0021](0021-admin-api-and-public-status.md) | Read-only admin API and public status | Accepted |
| [0022](0022-launch-root-span.md) | The launch span is the root of the launch trace | Accepted |
| [0023](0023-in-ide-feedback-path.md) | In-IDE feedback path with the instance token | Accepted |

## Format

Each ADR follows [0000-template.md](0000-template.md): Context and Problem Statement, Decision Drivers, Considered Options, Decision Outcome and Consequences.
The header lists the status, the date, the G and U numbers it builds on, and the contract files it shapes.
Statuses: Proposed, Accepted, Deprecated, Superseded by NNNN.
An accepted ADR is not rewritten. A changed decision gets a new ADR and the old one is marked superseded.

## Add an ADR

Follow the [write-adr skill](../../.claude/skills/write-adr/SKILL.md).
In short: copy the template to the next free number, fill it in, add a row to the table above and to [decisions.md](decisions.md) where it implements a G or U row.
A change to an API, a CRD or a metric that breaks the versioning rules needs an ADR before the code.
