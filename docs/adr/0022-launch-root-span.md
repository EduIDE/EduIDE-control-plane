# 0022. The launch span is the root of the launch trace

- Status: Accepted
- Date: 2026-11-20
- Refines: [0013](0013-observability.md), trace context only; the rest of 0013 stays in force
- Contracts: [api/openapi/v1.yaml](../../api/openapi/v1.yaml) (`traceparent` header of `launchSession`), [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go) (`AnnotationTraceparent`)

## Context and Problem Statement

[0013](0013-observability.md) lets the service continue the `traceparent` that the landing page sends.
The landing page exports no spans, so the parent of `launch` never reaches Jaeger.
On test3, 38 of 50 launch traces showed as incomplete, and the Jaeger search listed them by trace id instead of `eduide-service: launch`.

## Considered Options

- Keep the landing page as the parent.
- Let the landing page export its spans to the collector.
- Start `launch` as a new root span and keep the incoming context as a span link.

## Decision Outcome

Chosen: `launch` is a new root span, and an incoming `traceparent` becomes a span link.
The browser exporter would add a public OTLP endpoint and a second client to maintain for one span.
The service stores the context of `launch` in the annotation `eduide.github.io/traceparent`, so operator spans, agent milestones and `authz.check` still join the trace.

### Consequences

- Every launch trace is complete and named `launch`.
- The trace id differs from the one the landing page generated; the link keeps the connection for a client that exports spans later.
- The OpenAPI spec version goes to 1.1.1, because the meaning of the `traceparent` header changed.
