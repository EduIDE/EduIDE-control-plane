# Install

Install order: CRDs once per cluster, then the control plane once per deployment.
Use a local kind cluster first. Never run these commands against a cluster you did not choose on purpose; check `kubectl config current-context`.

## Prerequisites

| Item | Requirement |
|---|---|
| Kubernetes | A version with CEL validation in CRDs and Gateway API support |
| Envoy Gateway | v1.8 or newer, with a `Gateway` and a listener for the instance host and the webview host. The listeners of the three hosts allow routes only from the deployment namespace (`allowedRoutes.namespaces`): a route of another namespace on the instance host would receive the login cookies of the users |
| CNI | Enforces NetworkPolicies (checked below). Session pods may reach the internet but not the private ranges of `networkPolicy.sessionEgressDeny`. If nodes, the API server or the pod network have public addresses (also IPv6), add those ranges to `networkPolicy.sessionEgressExtraDeny` |
| Keycloak | One realm per deployment, see below |
| TLS | Certificates for `hosts.service`, `hosts.instance`, `hosts.webview` (one name each, no wildcard) |
| Storage | A StorageClass for `volume` workspaces (optional if you use only `none`) |

## 1. CRDs (once per cluster)

The CRD chart goes first and is additive only. Upgrade it before any control plane chart.

```bash
helm upgrade --install eduide-crds \
  oci://ghcr.io/eduide/eduide-control-plane/charts/eduide-control-plane-crds \
  --version <X.Y.Z>
```

Helm does not upgrade CRDs in a chart's `crds/` directory. The CRD chart ships them as templates so `helm upgrade` updates them.
Several deployments in one cluster share these CRDs. Use the newest chart version in the cluster; older operators ignore unknown fields.

## 2. Control plane (once per deployment)

```bash
helm upgrade --install eduide-control-plane \
  oci://ghcr.io/eduide/eduide-control-plane/charts/eduide-control-plane \
  --version <X.Y.Z> --namespace eduide-<deployment> --create-namespace \
  -f values.yaml
```

The chart installs operator, service and authz, PodDisruptionBudgets, RBAC, the SecurityPolicy, NetworkPolicies, the `/oauth2/` route and the ConfigMap `config.yaml`.
The chart's `values.yaml` lists every value with its default. The values below map to [internal/config/config.go](../../internal/config/config.go) and are required:

| Value (config key) | Example |
|---|---|
| `deployment` | `course-a` |
| `hosts.service` | `eduide.example.org` (must differ from the instance and webview host: the session routes catch every path there) |
| `hosts.instance` | `instance.eduide.example.org` |
| `hosts.webview` | `webview.instance.eduide.example.org` (default: `webview.` plus the instance host) |
| `gateway.name`, `gateway.namespace`, `gateway.sectionName` | `shared-gateway`, `envoy-gateway-system` |
| `oidc.issuerURL` | `https://keycloak.example.org/realms/eduide` |
| `oidc.clientIDs` | `[eduide]` (clients whose tokens the service accepts) |
| `oidc.gatewayClientID` | `eduide-gateway` |
| `agent.image` | `ghcr.io/eduide/eduide-control-plane/agent:<X.Y.Z>` |

Optional: `sessionsPerUser`, `allowedHosts` (Git and Artemis hosts users may inject), `workspaces.*` (storage class, size, retention, grace, `perUser`), `placement.*` (node selector, tolerations, affinity, node pool label, prepull), `capacity.exhaustedAfter`, `telemetry.otlpEndpoint`, `rateLimit.launchesPerMinute`.
Templates and pools are values of the chart too (they render `IDETemplate` and `IDEPool`).

Hosts, realms and node labels belong in your values, never in the chart defaults.

## 3. Keycloak

> **Warning: never switch "Client authentication" on for a client the landing page uses.**
> The landing page is a browser app. Its client must stay **public** ("Client authentication" off). If you turn it on, every landing page login fails with `unauthorized_client`. This happened on two production installations on 2026-11-25.
> The confidential client is a **new, separate** client for the gateway. You never convert an existing shared client.
> Installations that share one client for the landing page and the gateway follow [Migrate from one shared client](#migrate-from-one-shared-client).

Create two clients in the realm of the deployment: a public client for the landing page and a new confidential client for the gateway.

| Client | Type | Settings |
|---|---|---|
| Landing page (for example `eduide`) | public, standard flow only, PKCE method `S256` required | Valid redirect URI `https://<service host>/*` and nothing else, web origin `https://<service host>`. Direct access grants, implicit flow and service accounts off. Its id goes into the landing page config and into `oidc.clientIDs`. |
| Gateway (for example `eduide-gateway`) | a **new** client, **confidential**, standard flow only | Valid redirect URI, exactly one and with no wildcard: `https://<instance host>/oauth2/callback`. The webview host needs no entry: the SecurityPolicy sets this one callback URL, so the login always returns to the instance host, and the cookie domain only shares the login with the webview host. Web origins: none, or the instance origin. Direct access grants, implicit flow and service accounts off. Its id is `oidc.gatewayClientID` and the audience `authz` checks. |

**Use two clients, and never list the gateway client in `oidc.clientIDs`.**
Code in a session and script in the IDE's browser origin (the instance and webview host) must not be able to get a token the API accepts, because the API, including the approval of Git repositories, trusts that token as the user.
With a confidential gateway client, script cannot finish a login for it (it has no secret), and the API rejects the gateway's ID token (its `azp` is not in `oidc.clientIDs`).
With one client for both roles, the client has to be public for the landing page, and script in the IDE origin can run its own authorization code flow against Keycloak and call the API as the user.
That setup is not recommended, and the fix is a second client, never a confidential shared client: Keycloak ignores the gateway's client secret for a public client, and a confidential client breaks the landing page login.
The landing page client must have no redirect URI on the instance or webview host, for the same reason.
See [session access](../architecture/session-access.md#api-tokens-and-the-ide-origin) and [ADR 0023](../adr/0023-git-accounts-and-git-proxy.md).

The tokens must carry the claim named in `oidc.userClaim` (default `preferred_username`), plus `name` and `email`.
The gateway client secret is a Secret in the deployment namespace that the SecurityPolicy references. Create it from a file or a pipe; never print it and never put it in `values.yaml` in Git:

```bash
kubectl -n eduide-<deployment> create secret generic <gateway-secret-name> \
  --from-file=client-secret=<path-to-file>
```

The Secret name and key are chart values; check `values.yaml`.
The ID token cookie (`eduide-id` by default) is set for the instance host. The cookie domain covers the webview host.

### Migrate from one shared client

Use this when `oidc.gatewayClientID` and `oidc.clientIDs` name the same client today.
The order keeps the landing page login working at every step. Step 3 ends the IDE logins that are open (see there). **This procedure is not yet tested on a real installation.** Try it on a test realm first.
Do not change the existing client's "Client authentication" at any step.

1. In Keycloak, create a **new** client (for example `eduide-gateway`). Client authentication **on**, standard flow only, direct access grants, implicit flow and service accounts off. Valid redirect URI, exactly one: `https://<instance host>/oauth2/callback`. No wildcard, and no entry for the webview host. Copy its secret from the Credentials tab.
2. Store the secret in the Secret that `oidc.clientSecretName` names, under the key `client-secret`. Create or replace it from a file or a pipe, and never print it.
3. Set `config.oidc.gatewayClientID` to the new client id. Leave `config.oidc.clientIDs` and the landing page client as they are. Deploy the chart.
   This step signs out every open IDE tab once: the ID token cookies of the gateway carry the old audience, so `authz` rejects them and redirects to the login of the new client. Every user with an open IDE tab signs in again once. Running sessions keep running; only the browser login is renewed. Choose a quiet time window, for example outside lectures and exams.
4. Verify: log in to the landing page, then log in to an IDE (a fresh browser session). Both must work. The old public client still has its instance host redirect URI, so a failed step 3 costs nothing.
5. Only now remove the instance host redirect URI and web origin from the old, public client (and a webview host entry, if your installation added one). Keep its service host redirect URI and web origin. Its "Client authentication" stays off.
6. Verify again: landing page login, IDE login.

Go back:

- Before step 5: set `config.oidc.gatewayClientID` to the old client id again, restore the old secret value in the Secret, and deploy (or `helm rollback`).
- After step 5: re-add the two redirect URIs and web origins to the old client first, then set `gatewayClientID` back and deploy.
- Delete the new client only after the rollback is deployed.

## Admin role

The read-only admin API (`/api/v1/admin/*`, [ADR 0021](../adr/0021-admin-api-and-public-status.md)) is off until you configure a Keycloak role.
Without it every admin route answers 404; with it, users without the role get 403.

1. Create the role. Realm role: Realm roles, Create role, name `eduide-admin`. Client role: Clients, the landing page client, Roles, Create role, name `eduide-admin`.
2. Assign it: Users, the user, Role mapping, Assign role. To manage it by group: create a group, give the group the role, add users to the group.
3. Make it appear in the access token. A realm role is in `realm_access.roles` by default (the `roles` client scope). A client role is in `resource_access.<client>.roles` once the client scope `roles` is assigned to the landing page client with the mapper "client roles" enabled for access tokens. Decode a token to check.
4. Set it in the chart values:

```yaml
config:
  oidc:
    admin:
      rolePath: realm_access.roles          # or resource_access.<client>.roles
      role: eduide-admin
  admin:                                    # all optional
    prometheusURL: http://prometheus-operated.monitoring:9090
    links: {grafana: https://grafana.example.org, jaeger: https://jaeger.example.org}
```

`rolePath` is exactly one of the two shapes; the service refuses to start with anything else (for example `groups`).
A client id may contain dots (`resource_access.eduide.landing.roles` names the client `eduide.landing`).
The admin API also needs `telemetry.userSaltFile` (the chart sets it): owners appear as a hash keyed with this salt, the same as the user id in spans.
With `prometheusURL` and the pool scaler, the status warns `scaler-silent` when the scaler's heartbeat metric `pool_scaler_last_decision_timestamp_seconds` is older than two intervals or missing; this needs `monitoring.podMonitor.enabled`.
With `prometheusURL` the status page also shows start counts, latency, capacity and time series; without it these parts are absent.
The service needs `list` and `watch` on events and `get` and `list` on resourcequotas; the chart grants both.
With `poolScaler.enabled` the chart also creates a read-only ClusterRole `<release>-<namespace>-pool-scaler-nodes` (`get` and `list` on nodes and pods) for the scaler's node budget; it is the only cluster-scoped object of the deployment chart.

### Monitoring and HSTS

`monitoring.rules.extraLabels` adds labels to every alert, for example `{namespace: eduide-system}` on a cluster whose AlertmanagerConfig routes on that namespace; `eduide_namespace` stays the real namespace.
`monitoring.dashboard.namespace` (default: the release namespace) places the dashboard ConfigMap where the Grafana sidecar looks.
`monitoring.rules.certificates` (default true) adds `EduIDECertificateExpiringSoon` (under 21 days) and `EduIDECertificateNotReady` (1 h) for the cert-manager certificates of the release namespace; turn it off where a cluster-wide rule already covers the namespace.

The service and the landing page send `Strict-Transport-Security: max-age=31536000; includeSubDomains`, because the Envoy OIDC redirects on the session hosts never reach the IDE route and carry no HSTS of their own.
This assumes `hosts.instance` and `hosts.webview` are subdomains of `hosts.service` (for example `instance.ide.example.org`).
When they are not, set `config.security.hstsIncludeSubdomains: false` for the service and `hstsIncludeSubdomains: false` in the landing page `config`.
The route of a session keeps its own HSTS header without `includeSubDomains`.

## 4. Secrets overview

| Secret | Created by | Content |
|---|---|---|
| Gateway client secret | operator of the deployment (you) | Secret of the new, confidential gateway client (never of the public landing page client) |
| `ide-<instance>-token` | operator | Instance token of a pod, 32 random bytes |
| `<session>-data` | service | Launch data of one session, owned by the session |
| Git account key (`gitAccounts.encryptionKey.secretName`, else generated by the chart) | you or the chart | Only with Git providers. Encrypts the stored Git tokens and signs the connect state. Losing it makes users connect again |
| User salt (`userSalt.secretName`, else generated by the chart) | you or the chart | Salt for pseudonymous user ids in traces and the admin API. authz also derives the key of its session cookie `eduide-authz` from it, so treat it as an authentication secret: whoever knows it and holds a login of their own can pass authz as another user |

## Git accounts (optional)

The service can connect GitHub and GitLab accounts of users ([ADR 0023](../adr/0023-git-accounts-and-git-proxy.md)).
The feature is off while `config.gitAccounts.providers` is empty, and the chart renders nothing for it then.
Each provider needs an OAuth application at the Git host and the values described in the [chart README](../../charts/eduide-control-plane/README.md).
With providers the chart also renders the `git-proxy` (2 replicas, ClusterIP Service, no route), the internal port 8083 of the service and the NetworkPolicies for them. See the [git-proxy runbook](git-proxy.md).
**Planned:** a step-by-step operator guide for the OAuth applications.

## 5. Verify

```bash
kubectl -n eduide-<deployment> get pods
kubectl -n eduide-<deployment> get idetemplate,idepool
kubectl -n eduide-<deployment> get networkpolicy
```

- Operator, service (2) and authz (2) are `Ready`.
- Pools show `idleReady` equal to `minIdle` after a minute.
- Open `https://<service host>/`, launch a session, and check the status: `kubectl -n eduide-<deployment> get idesession`.
- NetworkPolicy smoke test: start a foreign pod in the namespace and confirm it cannot reach an IDE pod on :8080 or :8081. From a terminal in a session, confirm that the node address on :10250, the API server and a pod of another namespace do not answer. If it can, the CNI does not enforce policies. Do not release the deployment ([ADR 0019](../adr/0019-network-isolation.md)).
- An unauthenticated request to `https://<instance host>/<anything>/` redirects to Keycloak and never returns IDE content.

The chart generates the user salt once and keeps it on `helm upgrade`, because it looks the Secret up in the cluster. A renderer without cluster access (`helm template`, Argo CD, a client-side dry run) cannot look it up and renders a new salt every time: owner hashes then change between syncs and every `eduide-authz` cookie becomes invalid. With such a renderer, create the Secret yourself and set `userSalt.secretName`.

The operator sends launch data to the agent of a session pod over plain HTTP inside the cluster, with the instance token as bearer. The NetworkPolicies and the CNI protect that path; a CNI with transparent encryption adds confidentiality on the wire.

## Upgrade and rollback

Upgrade: CRD chart first, then the control plane chart of each deployment. Do not upgrade during an exam period.
Rollback: `helm rollback` the control plane chart. The CRD chart stays; the change is additive.
