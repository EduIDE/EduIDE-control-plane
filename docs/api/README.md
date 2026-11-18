# API references

The OpenAPI files are the source. This repository keeps no hand-written copy of them.

| API | Source | Generated from it |
|---|---|---|
| REST API v1 (landing page and clients, base path `/api/v1`) | [api/openapi/v1.yaml](../../api/openapi/v1.yaml) | Server stubs `internal/httpapi/api.gen.go`, TypeScript client `@eduide/control-plane-client` in [clients/typescript](../../clients/typescript) |
| Agent API (operator to agent, extension to agent) | [api/agent/v1.yaml](../../api/agent/v1.yaml) | Types `internal/agentapi/types.gen.go` |
| CRDs (`eduide.github.io/v1alpha1`) | Go types in [api/v1alpha1](../../api/v1alpha1) | YAML in [config/crd](../../config/crd) |

Read the endpoint reference in the OpenAPI file or render it with any OpenAPI viewer.
**Planned:** a rendered HTML reference built from the spec in CI.
The data model behind the endpoints: [data-model.md](../architecture/data-model.md).

REST endpoints (operation ids): `listTemplates`, `watchTemplates` (SSE), `listSessions`, `launchSession`, `getSession`, `deleteSession`, `watchSession` (SSE), `listWorkspaces`, `createWorkspace`, `deleteWorkspace`, `getOverview`, `watchOverview` (SSE).
Since [0020](../adr/0020-landing-page-data-in-the-api.md): `Session.stopsAt` and `stopReason`, `Workspace.deletesAt`, the same on `OverviewItem`, `Template.startEstimate`, `variants`, `includes` and `imageVersion`.
`DELETE /workspaces/{name}` stops a running session on the workspace first and answers 204. The error code `workspace-in-use` is unused and stays in the enum for compatibility.
Health and metrics are on a separate port and are not part of the spec.
Since [0021](../adr/0021-admin-api-and-public-status.md):
- Public, no token (U17): `getPublicStatus` (`GET /status`, cached 15 s, rate limited per IP).
- Admin API, read-only, tag `admin` (U16): `getAdminStatus`, `watchAdminStatus` (SSE), `listAdminPools`, `listAdminSessions`, `watchAdminSessions` (SSE), `getAdminSession`, `listAdminWorkspaces`, `getAdminProblems`, `listAdminTemplates`, `getAdminConfig`. They need the Keycloak role of `oidc.admin` (403 without it) and answer 404 while the deployment configures no role. Users appear only as `ownerHash`, launch data only as key names.
Admin changes go through Helm values and the CRDs; there is no write endpoint.

## Errors

Errors are RFC 9457 Problem Details with stable codes, for example `capacity-exhausted` and `session-limit-reached`. A client matches on the code, never on the text.

## Versioning rules

REST ([ADR 0010](../adr/0010-rest-api-spec-first.md)):

- Inside v1 only additive changes: new endpoints, new optional fields, new enum values the client can ignore.
- A breaking change needs `/api/v2` running next to v1, and an ADR.
- Deprecations are announced with the `Deprecation` and `Sunset` headers.
- The spec version is semantic. CI runs `oasdiff` and fails on a breaking change.
- The TypeScript client has the release version (see [release.md](../development/release.md)).

CRDs ([ADR 0011](../adr/0011-crd-versioning-cel.md)):

- `v1alpha1` now, `v1beta1` from the first productive use, `v1` after a stable semester.
- Changes are additive. Fields are removed only when no deployment in the cluster uses them. CI diffs the CRD schemas (`hack/crddiff`).
- Validation uses CEL rules in the CRD, not an admission webhook.
- Upgrade order: the CRD chart first per cluster, then the control plane chart per deployment ([install.md](../operations/install.md)).

Agent API: same additive rule inside `v1`. The operator and the agent of one release always match, because both ship under one tag.

Change a contract with the skills `add-api-endpoint` and `add-crd-field` in [.claude/skills](../../.claude/skills).
