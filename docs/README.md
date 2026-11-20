# EduIDE control plane documentation

Planned parts are marked **Planned** in the pages.

| Section | Content |
|---|---|
| [adr/](adr/README.md) | Architecture decisions (MADR) and the table of grilling decisions |
| [architecture/overview.md](architecture/overview.md) | Components, deployment, responsibilities, trust boundaries |
| [architecture/start-path.md](architecture/start-path.md) | Warm and cold start, phases, time budgets |
| [architecture/data-model.md](architecture/data-model.md) | CRDs, labels, annotations, naming |
| [architecture/session-access.md](architecture/session-access.md) | How a request reaches an IDE (gateway auth and authz) |
| [architecture/diagrams/](architecture/diagrams/README.md) | UML diagrams and their generator |
| [api/](api/README.md) | REST and agent API references, versioning rules |
| [contracts/metrics.md](contracts/metrics.md) | Metric names and labels |
| [operations/install.md](operations/install.md) | Install the CRDs and the control plane |
| Runbooks | [pool empty](operations/pool-empty.md), [slow launch](operations/slow-launch.md), [authz errors](operations/authz-errors.md), [gateway backlog](operations/gateway-backlog.md), [workspace volume lost](operations/workspace-volume-lost.md) |
| [operations/usage-export.md](operations/usage-export.md) | Usage metrics, recording rules and the anonymous usage export |
| [operations/tracing.md](operations/tracing.md) | Read a launch trace in Jaeger: spans, normal values, slow causes |
| [operations/logging.md](operations/logging.md) | Log format, fields and how to switch on debug logs |
| [operations/migration.md](operations/migration.md) | Move a deployment from the Java stack |
| [development/setup.md](development/setup.md) | Tools and first build |
| [development/testing.md](development/testing.md) | Test pyramid and how to run it |
| [development/release.md](development/release.md) | Versioning and release steps |

Rules for agents and contributors: [AGENTS.md](../AGENTS.md).
Check the Markdown links with `docs/check-links.sh`.
