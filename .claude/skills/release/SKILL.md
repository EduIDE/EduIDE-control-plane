---
name: release
description: Use when preparing a release of the control plane: one vX.Y.Z tag versioning the images, both charts, the OpenAPI spec and the TypeScript client, with changelog and generated code checks.
---

# Release

Rules:
- One tag `vX.Y.Z` versions everything: the images `ghcr.io/eduide/eduide-control-plane/{operator,service,authz,agent}`, the charts `eduide-control-plane-crds` and `eduide-control-plane`, the OpenAPI spec and the TypeScript client (U2, [ADR 0018](../../../docs/adr/0018-charts-oci.md)).
- Ask before tagging, committing or pushing. Never force-push a tag.
- Changes since the last release must follow the additive rules: no breaking API or CRD change without an ADR and a new version.
- Local cluster checks use an isolated `KUBECONFIG` (see run-e2e).

Steps:
1. Pick the version. Breaking change in the REST API means a new major API version, not just a new tag. A spec change bumps the spec version semantically in `api/openapi/v1.yaml`.
2. Run `make generate`, then `make verify-generate` (no diff), `make lint`, `make test` and `make e2e`. All must pass. The CI also runs `oasdiff`, the CRD schema diff, `govulncheck`, Trivy and Gitleaks. CI does not run on the tag itself: the release commit on `main` needs a green `ci-ok` of its push run first (the nightly run skips the code jobs).
3. Update `CHANGELOG.md`: added, changed, deprecated, fixed, and the CRD and API compatibility note. Set the chart `version` and `appVersion` and `clients/typescript/package.json` to the release version.
4. Ask the user to confirm, then create the tag `vX.Y.Z` on the release commit, which must be on `main`. The workflows stop unless the tag matches `vX.Y.Z` and its commit is on `main`.
5. `images.yml` builds every image, scans it with Trivy and pushes it; `release.yml` pushes the charts as OCI artifacts. No workflow publishes the TypeScript client to npm. Watch both workflows and act on failures.
6. Upgrade order for clusters: the CRD chart first (additive only), then the control plane chart per deployment. Note this in the release notes. EduIDE-Helm bumps its dependency versions in its own pull request.
7. Verify on a local kind cluster with the published chart and images, using the isolated `KUBECONFIG`.
8. Never roll a production deployment during an exam period.
