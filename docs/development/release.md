# Release

One tag `vX.Y.Z` versions everything ([ADR 0018](../adr/0018-charts-oci.md)):

| Artifact | Where |
|---|---|
| Images `operator`, `service`, `authz`, `agent`, `pool-scaler`, `landing-page` | `ghcr.io/eduide/eduide-control-plane/<name>:X.Y.Z` |
| Charts `eduide-control-plane-crds`, `eduide-control-plane`, `eduide-landing-page` | `oci://ghcr.io/eduide/eduide-control-plane/charts` |
| OpenAPI spec | `api/openapi/v1.yaml` (semantic spec version) |
| TypeScript client `@eduide/control-plane-client` | npm |

## Image tags

| Event | Tag |
|---|---|
| Pull request | `pr-<N>` |
| Push to `main` | `main` and `sha-<7>` |
| Release tag `vX.Y.Z` | `X.Y.Z` |

To test a pull request on a test deployment, point the chart values at the `pr-<N>` image tags. Do this only on test deployments.
Every image gets a Trivy scan in the workflow.

## Steps

The skill `release` in [.claude/skills/release/SKILL.md](../../.claude/skills/release/SKILL.md) has the checklist. In short:

1. Pick the version. A breaking REST change is a new API version, not just a new tag ([api/README.md](../api/README.md)).
2. `make generate`, `make verify-generate`, `make lint`, `make test`, `make e2e` all pass. CI also runs `oasdiff`, the CRD schema diff, `govulncheck`, Trivy and Gitleaks.
3. Update `CHANGELOG.md` and set chart `version` and `appVersion` and the `clients/typescript/package.json` version to the release version.
4. Ask the maintainer, then create the tag `vX.Y.Z`. CI checks the tag format. Never force-push a tag.
5. The workflows build and push the images, push the charts as OCI artifacts and publish the client. Watch them and act on failures.
6. Verify on a local kind cluster with the isolated `KUBECONFIG` ([testing.md](testing.md)).

## Upgrade order for operators

CRD chart first per cluster (additive only), then the control plane chart per deployment ([install.md](../operations/install.md)).
State this in the release notes. EduIDE-Helm bumps its dependency versions in its own pull request.
Never roll a productive deployment during an exam period.
