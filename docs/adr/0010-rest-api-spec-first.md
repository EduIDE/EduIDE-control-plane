# 0010. REST spec-first, `/api/v1`, Problem Details, versioning rules

- Status: Accepted
- Date: 2026-11-18
- Decisions: G14, G15, G16, U4
- Contracts: [api/openapi/v1.yaml](../../api/openapi/v1.yaml), [api/openapi/oapi-codegen.yaml](../../api/openapi/oapi-codegen.yaml), [internal/config/config.go](../../internal/config/config.go) (`RateLimit`, `AllowedHosts`, `SessionsPerUser`)

## Context and Problem Statement

The landing page, the Artemis deep link and tests use the REST API of the service.
The old API mixes admin, performance and test endpoints with the launch path, uses HTTP 555 for no capacity, and the clients poll.
We need a stable, generated, testable contract.

## Decision Drivers

- One source of truth for server stubs, the TypeScript client and tests.
- Stable error codes clients can switch on.
- No polling in the start path.
- Additive evolution, so clients and servers can upgrade independently.

## Considered Options

1. Code-first (handlers define the API).
2. gRPC.
3. OpenAPI 3.1 first, generated server and client.

## Decision Outcome

Chosen option: "OpenAPI 3.1 first".

- [api/openapi/v1.yaml](../../api/openapi/v1.yaml) is the source. `make generate` produces the server in `internal/httpapi/api.gen.go` and the client in `clients/typescript`. Nobody edits generated files.
- Base path `/api/v1`. Endpoints: `GET /templates`, `GET|POST /sessions`, `GET|DELETE /sessions/{name}`, `GET /sessions/{name}/events` (SSE), `GET|POST /workspaces`, `DELETE /workspaces/{name}`, `GET /me/overview` and `GET /me/overview/events`.
- No `performance` and no `setConfigValue` in v1. Admin changes go through Helm and the CRDs. The admin API of [0021](0021-admin-api-and-public-status.md) is read-only and was added later (U16); this decision otherwise stands.
- Auth: OIDC bearer token of the deployment's realm, verified with go-oidc (JWKS cached). The owner comes from the verified token, never from the request.
- Errors are RFC 9457 Problem Details with a stable `code` such as `capacity-exhausted`, `session-limit-reached`, `unknown-key`, `rate-limited`. New codes are additive.
- Launch (G14): idempotent, same session for a repeated request. At most `sessionsPerUser` sessions (default 3); beyond it `session-limit-reached` with the list of running sessions. Rate limit per user (`rateLimit.launchesPerMinute`).
- SSE: the start progress and overview changes come from informer events (G15), so clients do not poll. The overview (`/me/overview`) merges workspaces without a running session as not running and all sessions as running. Display data are the non-secret `display.title` and `display.source` of the specs.
- Artemis token (G16): it travels only in the URL fragment (`#artemisToken=...`). The landing page reads it first, stores it in `sessionStorage`, removes the fragment with `history.replaceState`, then starts the Keycloak login, and deletes the entry after the launch. `?artemisToken` is still read during the migration and logged as deprecated. `gitUri` and `appDef` stay query parameters. The service never sees the fragment.
- Versioning: within v1 only additive changes (new endpoints, optional fields, new enum values and error codes that clients must tolerate). A breaking change needs v2 in parallel operation and an ADR. Deprecation uses `Deprecation` and `Sunset` headers. The spec is versioned semantically, and CI runs an `oasdiff` breaking-change check (U4).
- Requests and responses are validated against the spec in contract tests ([0015](0015-test-strategy.md)). Metrics use the `operationId` ([docs/contracts/metrics.md](../contracts/metrics.md)).
- The skill [add-api-endpoint](../../.claude/skills/add-api-endpoint/SKILL.md) holds the steps.

### Consequences

- Good: Client and server cannot drift. The landing page gets typed calls and SSE.
- Good: Stable codes replace HTTP 555.
- Bad: Every API change starts with a spec edit and a regeneration.
- Bad: Enum values added to responses can break strict clients. The client treats unknown values as opaque.
- Follow-up: Publish the TypeScript client as an npm package with the release ([release skill](../../.claude/skills/release/SKILL.md)).

## Pros and Cons of the Options

### Code-first

- Good, because fast to start.
- Bad, because the contract is implicit and client generation is weak.

### gRPC

- Good, because strong typing.
- Bad, because browsers need a proxy, and SSE-like streaming with the landing page is simpler over HTTP.

### OpenAPI first

- Good, because contract, stubs, client and validation share one file.
- Bad, because generator limits shape the handlers.
