# 0018. Charts in the same repo, published as OCI

- Status: Accepted
- Date: 2026-11-18
- Decisions: G10, G11, U2, U3, U6
- Contracts: [config/crd](../../config/crd), [internal/config/config.go](../../internal/config/config.go)

## Context and Problem Statement

The control plane needs Helm charts for the CRDs (once per cluster) and for the control plane (once per deployment).
The old setup keeps charts apart from the code, which makes a change to a CRD and its chart a two-repository change.
`EduIDE-Helm` remains the deployment chart that assembles a full EduIDE.

## Decision Drivers

- A CRD or config change and its chart change ship in one pull request.
- One version for images, charts, spec and TypeScript client.
- Charts usable by others: no institution specific defaults (U6).

## Considered Options

1. Charts in the deployment repository only.
2. Charts in this repository, published as OCI artifacts, included by the deployment chart.

## Decision Outcome

Chosen option: "charts in this repository, published as OCI".

- Two charts under `charts/`: `eduide-control-plane-crds` (per cluster, installed first, additive only, see [0011](0011-crd-versioning-cel.md)) and `eduide-control-plane` (per deployment). A chart for the landing page lives in the repository too (U2).
- The CRD YAML in the chart comes from `config/crd` by `make generate`. A check fails when they differ.
- The control plane chart holds the operator, service and authz Deployments, the PodDisruptionBudgets, the RBAC, the SecurityPolicy ([0005](0005-session-auth-at-gateway.md)), the NetworkPolicies ([0019](0019-network-isolation.md)), the config ConfigMap, the ServiceMonitors, and dashboards and alerts ([0013](0013-observability.md)).
- Charts are published as OCI artifacts to `ghcr.io/eduide/eduide-control-plane/` by the release workflow, with the same `vX.Y.Z` tag as the images (G11, U3, [release skill](../../.claude/skills/release/SKILL.md)).
- `EduIDE-Helm` includes both charts and adds landing page configuration, the gateway, the issuer, hosts and realm. Defaults in this repo are neutral and use `example.org` in examples (U6).
- Helm checks: `helm lint` and template tests in CI.

### Consequences

- Good: One pull request changes types, CRD and chart. One tag versions everything.
- Good: Others can install the charts without our deployment repository.
- Bad: The repository grows (landing page, scaler, charts) and needs path-filtered CI ([0015](0015-test-strategy.md)).
- Bad: The deployment chart must follow chart versions. The rule that the CRD chart goes first is documented in the release notes.
- Follow-up: The CI renders both charts with test values and runs the schema diff on the CRDs.

## Pros and Cons of the Options

### Charts in the deployment repository

- Good, because one place for deployment values.
- Bad, because every API change touches two repositories.

### Charts in this repository

- Good, because atomic changes and one version.
- Bad, because chart changes by deployment operators need a pull request here.
