# 0004. Gateway API only, routes for pool instances created up front

- Status: Accepted
- Date: 2026-11-18
- Decisions: G2, G3, G4
- Contracts: [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go) (`LabelRoute`, `LabelInstance`), [internal/config/config.go](../../internal/config/config.go) (`Hosts`, `GatewayRef`)

## Context and Problem Statement

Sessions need a URL that reaches the IDE pod.
The old stack supports Ingress and creates the route only when a cold session is created, so the Gateway programs a new route on the critical path.
A warm start should change nothing at the gateway.

## Decision Drivers

- Warm start below 2 s p95: no gateway reconfiguration at claim time.
- One mechanism instead of several routing back ends.
- Stay free of wildcard DNS and wildcard certificates (G3).

## Considered Options

1. Ingress and Gateway API.
2. Gateway API only, route per session created at claim time.
3. Gateway API only, service and HTTPRoute created together with the pool pod.

## Decision Outcome

Chosen option: "Gateway API only, route created with the pod".

- No Ingress support. The Gateway is Envoy Gateway; the deployment names its Gateway in `gateway` of the config.
- URL scheme is path based on one instance host per deployment: `https://<instance host>/<instance id>/` (G2).
  No subdomain per session.
- The instance id is random and unguessable, stored in the label `eduide.github.io/instance` and in `status.instance` of the session.
- A pool pod gets its Service and HTTPRoute when it is created, under its instance id.
  A claim only changes labels and the session status. The gateway configuration does not change.
  Until the claim, authz denies every request to the route ([0005](0005-session-auth-at-gateway.md)).
- Cold sessions create pod, Service and HTTPRoute at the same time, so the gateway programs the route while the container starts.
- Session HTTPRoutes carry the label `eduide.github.io/route=session`, which the deployment's SecurityPolicy selects.
- The HTTPRoute sets `X-EduIDE-Instance-Token` with a `RequestHeaderModifier`, which overwrites client values (G4, [0006](0006-agent-sidecar.md)).
- Webviews and the mini browser use the fixed host `webview.<instance host>` instead of `<uuid>.webview.<host>` (G3).
  This needs one more DNS name and certificate name, no wildcard. The workbench stays on a separate origin from the webviews.
  The images set `THEIA_WEBVIEW_EXTERNAL_ENDPOINT` and `THEIA_MINI_BROWSER_HOST_PATTERN` accordingly.

### Consequences

- Good: The claim path touches no gateway state.
- Good: No wildcard certificate or DNS entry is needed.
- Bad: More HTTPRoutes exist (one per idle pool instance). The route count is measured in the scale test; the fallback is one route per pool with header matching ([0015](0015-test-strategy.md)).
- Bad: The instance token sits in the HTTPRoute spec, readable by admins and the operator (see [0006](0006-agent-sidecar.md)).
- Follow-ups: A phase 0 spike checks Markdown preview, extension webviews, HTML preview and service workers under the shared webview origin.

## Pros and Cons of the Options

### Ingress and Gateway API

- Good, because it works on any cluster.
- Bad, because two code paths, and no cluster of ours needs Ingress.

### Route at claim time

- Good, because fewer routes exist.
- Bad, because route programming latency lands in the warm start.

### Route with the pod

- Good, because claim is label-only.
- Bad, because the route count grows with the pool size.
