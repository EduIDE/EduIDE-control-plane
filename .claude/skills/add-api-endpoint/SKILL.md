---
name: add-api-endpoint
description: Use when adding or changing a REST endpoint, request or response field, or error code of the service in api/openapi/v1.yaml. Spec first, then codegen, handler, contract test and client release.
---

# Add an API endpoint

Rules:
- The spec `api/openapi/v1.yaml` is the source. `internal/httpapi/api.gen.go` and `clients/typescript/src/schema.gen.ts` are generated and never edited by hand.
- Inside `/api/v1` only additive changes: new endpoints, optional fields, new error codes. Anything else needs an ADR and v2 ([ADR 0010](../../../docs/adr/0010-rest-api-spec-first.md)).
- Errors are RFC 9457 Problem Details with a stable `code`. Never invent HTTP status codes like 555.
- Tests are mandatory. Docs change in the same change.
- Local cluster commands use an isolated `KUBECONFIG` (see run-e2e).

Steps:
1. Edit `api/openapi/v1.yaml`: path, `operationId` (it becomes a metric label), request and response schemas, `default: {$ref: "#/components/responses/Problem"}`. Bound all strings and lists.
2. New error code: add it to the `code` enum in the spec. A code shared with a CRD status goes into `api/v1alpha1/idesession_types.go` too.
3. Run `make generate`. The server interface, the client and the schema types update.
4. Implement the handler in `internal/httpapi/` for the new interface method. Read from informer caches, write only CRs. The owner comes from the verified token, never from the request.
5. Add tests: handler unit tests, a contract test that validates request and response against the spec, a fuzz test if the endpoint parses user input.
6. If a CRD field is needed, follow add-crd-field first.
7. If you add a metric, add it to `docs/contracts/metrics.md` (the test in `internal/telemetry` checks the table).
8. Update `CHANGELOG.md` and the generated API reference. If the landing page uses the endpoint, update it through the generated client in `clients/typescript`.
9. Run `make test`, `make lint` and `make verify-generate`. CI also runs the `oasdiff` breaking-change check.
10. The client version follows the release (see release skill). Do not commit without asking.
