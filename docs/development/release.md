# Release

One tag `vX.Y.Z` versions everything ([ADR 0018](../adr/0018-charts-oci.md)):

| Artifact | Where |
|---|---|
| Images `operator`, `service`, `authz`, `agent`, `pool-scaler`, `landing-page` | `ghcr.io/eduide/eduide-control-plane/<name>:X.Y.Z` |
| Charts `eduide-control-plane-crds`, `eduide-control-plane`, `eduide-landing-page` | `oci://ghcr.io/eduide/eduide-control-plane/charts` |
| OpenAPI spec | `api/openapi/v1.yaml` (semantic spec version) |
| TypeScript client `@eduide/control-plane-client` | `clients/typescript` (no workflow publishes it to npm yet) |

## Image tags

| Event | Tag |
|---|---|
| Pull request | `pr-<N>` |
| Push to `main` | `main` and `sha-<7>` |
| Manual run on another branch | `sha-<7>` only |
| Release tag `vX.Y.Z` | `X.Y.Z` |

To test a pull request on a test deployment, point the chart values at the `pr-<N>` image tags. Do this only on test deployments.
The workflow builds the amd64 image, scans it with Trivy and pushes the multi-arch image only when the scan passes.

## Steps

The skill `release` in [.claude/skills/release/SKILL.md](../../.claude/skills/release/SKILL.md) has the checklist. In short:

1. Pick the version. A breaking REST change is a new API version, not just a new tag ([api/README.md](../api/README.md)).
2. `make generate`, `make verify-generate`, `make lint`, `make test`, `make e2e` all pass. CI also runs `oasdiff`, the CRD schema diff, `govulncheck`, Trivy and Gitleaks. `govulncheck` and the Trivy filesystem scan also run every night.
   CI does not run on the tag itself, so wait for a green `ci-ok` of the push run on the release commit on `main` before you tag. The nightly run skips the code jobs and does not count.
3. Update `CHANGELOG.md` and set chart `version` and `appVersion` and the `clients/typescript/package.json` version to the release version.
4. Ask the maintainer, then create the tag `vX.Y.Z` on a commit of `main`. Both workflows stop unless the tag matches `vX.Y.Z` and its commit is on `main`. Never force-push a tag.
5. `images.yml` builds, scans and pushes the images; `release.yml` pushes the charts as OCI artifacts. Watch them and act on failures.
6. Verify on a local kind cluster with the isolated `KUBECONFIG` ([testing.md](testing.md)).

## Upgrade order for operators

CRD chart first per cluster (additive only), then the control plane chart per deployment ([install.md](../operations/install.md)).
State this in the release notes. EduIDE-Helm bumps its dependency versions in its own pull request.
Never roll a productive deployment during an exam period.
