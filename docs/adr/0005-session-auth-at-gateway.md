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
The JWKS is cached and refetched at most once per 30 s per replica, so tokens with forged key ids cannot flood Keycloak; a rotated key is accepted within 30 s.
Issuer comes from `oidc.issuerURL`, audience is the gateway client.
The first path segment must be the same before and after unescaping and dot removal, because Envoy routes on the literal path.
The decision, per request:

| Cookies and token | Answer |
|---|---|
| (a) No auth cookie at all: no `eduide-id`, no `AccessToken*`, `OauthHMAC*`, `OauthExpires*`, `IdToken*` or `RefreshToken*` (Envoy Gateway names them `<Name>-<suffix>`) | allow; the following oauth2 filter forces the login, so nothing reaches the IDE without a valid login |
| (b) ID token present but invalid or expired | 302 to the same URL and expire the auth cookies, so oauth2 starts a fresh login, silent through Keycloak SSO |
| (c) Valid token | allow only if the first path segment is the instance id of a session in phase `Ready` whose `spec.owner` equals the configured user claim (`oidc.userClaim`); otherwise 403 |

- Unknown, free, foreign and released instances get 403 for a logged-in user and the login redirect for an anonymous one.
  An id without a session route does not break this: the chart adds a catch-all HTTPRoute (`PathPrefix /`, label `eduide.github.io/route=session`) on the instance and the webview host. Session routes have longer prefixes and win. Every other path passes the SecurityPolicy and authz first and only then reaches the service, which answers 404.
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
## Amendment 2026-11-18: token cookies in plain text

Envoy Gateway encrypts the access and ID token cookies by default with a key only Envoy knows.
authz then sees no JWT and every request ends in a fresh login (found in the kind e2e run).
The SecurityPolicy therefore sets `oidc.disableTokenEncryption: true`.
The cookies stay `Secure` and `HttpOnly` and are scoped to the instance host.
The agent strips all gateway login cookies (`eduide-id`, `IdToken*`, `AccessToken*`, `RefreshToken*`, `OauthHMAC*`, `OauthExpires*`, `OauthNonce*`, `CodeVerifier*`) before a request reaches the IDE, so code in the IDE never sees the tokens.

## Amendment 2026-11-19: expired ID token with a refresh token, no redirect for XHR

Case (b) cleared the cookies of every request with an expired ID token.
Because ext_authz runs before oauth2, the oauth2 filter never got to refresh the tokens with `refreshToken: true`.
A page navigation survived this through a silent Keycloak round trip, but XHR and WebSocket reconnects of an open IDE cannot follow a redirect to Keycloak (CORS), so the IDE lost its connection once the ID token expired (found on test3, Keycloak ID token lifetime 5 minutes).
Worse, every redirect sets two new login flow cookies (`OauthNonce-*.<flow>`, `CodeVerifier.<flow>`, Max-Age 600); an IDE that retried every second pushed the browser past Envoy's limit of 100 request headers (`http2.too_many_headers`) and locked it out of every session on the instance host for 10 minutes.

Two changes:

- authz accepts an ID token whose signature, issuer and audience verify and which expired less than 12 hours ago, when the request carries refresh token cookies (`RefreshToken*`) and every one of them is a JWT with the same `sub` and, when both carry one, the same `sid` as the ID token.
  authz reads the refresh token payload without verifying it; Keycloak verifies it when the oauth2 filter uses it.
  The binding stops a user from pairing someone else's expired ID token with their own valid refresh token.
  The ownership check (c) is unchanged.
  The oauth2 filter that follows refreshes the tokens and sets new cookies, or restarts the login when Keycloak refuses the refresh, so an ended Keycloak session still ends access.
  Without a matching refresh token, with an opaque one, or later than 12 hours, case (b) applies as before.
- The SecurityPolicy sets `oidc.denyRedirect` for requests with `Sec-Fetch-Mode` `cors`, `no-cors`, `same-origin` or `websocket`: they get a 401 instead of a login redirect and no flow cookies.
  Page navigations (`navigate`) still log in through the redirect.
