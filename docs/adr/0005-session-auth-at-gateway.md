# 0005. Session auth at the gateway: SecurityPolicy (OIDC) per deployment and ext_authz, no oauth2-proxy

- Status: Accepted
- Date: 2026-11-18
- Decisions: G2, G3, G4, G5, U10
- Contracts: [internal/config/config.go](../../internal/config/config.go) (`OIDC`, `Hosts`), [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go), [docs/contracts/metrics.md](../contracts/metrics.md) (`eduide_authz_*`)

## Context and Problem Statement

Today every session pod carries an oauth2-proxy and a ConfigMap with the allowed e-mail address.
That costs a container and a ConfigMap per session, and the file in the pod changes only when the kubelet syncs the volume, up to about a minute after a claim.
We want authentication and the decision "may this user open this session" at the gateway, with no change to the gateway at claim time.

## Decision Drivers

- Nothing reaches an IDE without a valid login and a matching owner.
- Fail closed, also when authz is down.
- No change to the shared EnvoyProxy of the cluster.
- Warm start below 2 s: no per-claim gateway or file propagation.

## Considered Options

1. oauth2-proxy per pod with an e-mail file written by the agent (fallback).
2. SecurityPolicy with OIDC, plus an ext_authz service that trusts Envoy-set headers.
3. SecurityPolicy with OIDC, plus an ext_authz service that verifies the ID token cookie itself.

## Decision Outcome

Chosen option: "SecurityPolicy with OIDC plus ext_authz that verifies the ID token cookie itself".
Option 1 stays the fallback if the phase 0 spike fails.

### Finding of 2026-11-18

Verified in the Envoy Gateway v1.8.3 source (`internal/xds/translator/httpfilters.go`): the HTTP filter order puts ext_authz (position 5) before oauth2 (8) and jwt_authn (9).
The shared EnvoyProxy of the stud cluster (`mergeGateways`) sets no `filterOrder`, and we may not change it.
Consequence: when ext_authz runs, the OIDC filter has not yet validated or set anything, so authz cannot trust headers derived from the token.

### Design

One SecurityPolicy per deployment selects the session HTTPRoutes by the label `eduide.github.io/route=session` and configures:

- `oidc` (the oauth2 filter) against the deployment's Keycloak realm with the gateway client (`oidc.gatewayClientID`), `cookieNames.idToken: eduide-id` (`oidc.idTokenCookie`), `cookieDomain` covering the instance host and the webview host (G3), and `refreshToken`.
- `extAuth` over gRPC to the `authz` Deployment ([0002](0002-go-single-module-four-binaries.md), G5) with `failOpen: false`.

Because ext_authz runs first, `authz` verifies the ID token cookie itself with go-oidc.
The JWKS is cached, issuer comes from `oidc.issuerURL`, audience is the gateway client.
The decision, per request:

| Cookies and token | Answer |
|---|---|
| (a) No auth cookie at all: no `eduide-id`, no `BearerToken*`, no `OauthHMAC*` | allow; the following oauth2 filter forces the login, so nothing reaches the IDE without a valid login |
| (b) ID token present but invalid or expired | 302 to the same URL and expire the auth cookies, so oauth2 starts a fresh login, silent through Keycloak SSO |
| (c) Valid token | allow only if the first path segment is the instance id of a session in phase `Ready` whose `spec.owner` equals the configured user claim (`oidc.userClaim`); otherwise 403 |

- Unknown, free, foreign and released instances get 403.
- The instance id is random and unguessable (label `eduide.github.io/instance`, `status.instance` of the IDESession), so answer (a) leaks nothing about sessions.
- `authz` reads sessions from an own informer only, with no API call per request. WebSockets are checked at the upgrade.
- Results are counted in `eduide_authz_decisions_total` as `allow`, `deny` or `login`.
- On release, the session leaves `Ready` and authz denies at once ([0009](0009-claim-protocol.md)).
- The NetworkPolicy and the agent token stay as the second line ([0019](0019-network-isolation.md), [0006](0006-agent-sidecar.md)).
- On the test cluster, the gateway client is the existing confidential Keycloak client, its secret is copied by pipe into the namespace, and the callback is on the instance host (U10).

### Consequences

- Good: No sidecar proxy and no per-session ConfigMap. Claim and release change no gateway state.
- Good: authz does not depend on filter order for correctness: a request without a valid token never passes (c), and one without any cookie is stopped by oauth2.
- Bad: authz parses and verifies a JWT per request (cached keys, cheap). Expired tokens cost one extra redirect.
- Bad: Correctness relies on oauth2 running after ext_authz for case (a). The e2e suite tests a request without cookies against a session route and expects a redirect, never IDE content.
- Follow-up: If a cluster allows an own EnvoyProxy with `filterOrder`, authz can trust Envoy-set headers and drop the token check.
- Follow-up: Observe GEP-1494 (external auth in the Gateway API standard). ext_authz is the standard protocol, so authz stays portable.
- Follow-up: The phase 0 spike measures cookie size, refresh, logout, WebSockets and latency per request.

## Pros and Cons of the Options

### oauth2-proxy per pod

- Good, because it is proven.
- Bad, because of a container and ConfigMap per session and slow file propagation.

### Trust Envoy-set headers

- Good, because authz stays tiny.
- Bad, because it does not work with the given filter order.

### authz verifies the cookie

- Good, because it works without changing the shared EnvoyProxy.
- Bad, because it duplicates token verification that oauth2 also does.
