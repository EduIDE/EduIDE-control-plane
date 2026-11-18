# Install

Install order: CRDs once per cluster, then the control plane once per deployment.
Use a local kind cluster first. Never run these commands against a cluster you did not choose on purpose; check `kubectl config current-context`.

## Prerequisites

| Item | Requirement |
|---|---|
| Kubernetes | A version with CEL validation in CRDs and Gateway API support |
| Envoy Gateway | v1.8 or newer, with a `Gateway` and a listener for the instance host and the webview host |
| CNI | Enforces NetworkPolicies (checked below) |
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
| `hosts.service` | `eduide.example.org` |
| `hosts.instance` | `instance.eduide.example.org` |
| `hosts.webview` | `webview.instance.eduide.example.org` (default: `webview.` plus the instance host) |
| `gateway.name`, `gateway.namespace`, `gateway.sectionName` | `shared-gateway`, `envoy-gateway-system` |
| `oidc.issuerURL` | `https://keycloak.example.org/realms/eduide` |
| `oidc.clientIDs` | `[eduide]` (clients whose tokens the service accepts) |
| `oidc.gatewayClientID` | `eduide-gateway` |
| `agent.image` | `ghcr.io/eduide/eduide-control-plane/agent:<X.Y.Z>` |

Optional: `sessionsPerUser`, `allowedHosts` (Git and Artemis hosts users may inject), `workspaces.*` (storage class, size, retention, grace), `placement.*` (node selector, tolerations, affinity, node pool label, prepull), `capacity.exhaustedAfter`, `telemetry.otlpEndpoint`, `rateLimit.launchesPerMinute`.
Templates and pools are values of the chart too (they render `IDETemplate` and `IDEPool`).

Hosts, realms and node labels belong in your values, never in the chart defaults.

## 3. Keycloak

Create two clients in the realm of the deployment.

| Client | Type | Settings |
|---|---|---|
| Landing page (for example `eduide`) | public, standard flow | Valid redirect URI `https://<service host>/*`, web origin `https://<service host>`. Its id goes into the landing page config and into `oidc.clientIDs`. |
| Gateway (for example `eduide-gateway`) | **confidential** | Valid redirect URI `https://<instance host>/oauth2/callback`. Its id is `oidc.gatewayClientID` and the audience `authz` checks. |

The tokens must carry the claim named in `oidc.userClaim` (default `preferred_username`), plus `name` and `email`.
The gateway client secret is a Secret in the deployment namespace that the SecurityPolicy references. Create it from a file or a pipe; never print it and never put it in `values.yaml` in Git:

```bash
kubectl -n eduide-<deployment> create secret generic <gateway-secret-name> \
  --from-file=client-secret=<path-to-file>
```

The Secret name and key are chart values; check `values.yaml`.
The ID token cookie (`eduide-id` by default) is set for the instance host. The cookie domain covers the webview host.

## 4. Secrets overview

| Secret | Created by | Content |
|---|---|---|
| Gateway client secret | operator of the deployment (you) | Keycloak client secret |
| `ide-<instance>-token` | operator | Instance token of a pod, 32 random bytes |
| `<session>-data` | service | Launch data of one session, owned by the session |
| Telemetry salt file (if `telemetry.userSaltFile` is set) | you | Salt for pseudonymous user ids in traces |

## 5. Verify

```bash
kubectl -n eduide-<deployment> get pods
kubectl -n eduide-<deployment> get idetemplate,idepool
kubectl -n eduide-<deployment> get networkpolicy
```

- Operator, service (2) and authz (2) are `Ready`.
- Pools show `idleReady` equal to `minIdle` after a minute.
- Open `https://<service host>/`, launch a session, and check the status: `kubectl -n eduide-<deployment> get idesession`.
- NetworkPolicy smoke test: start a foreign pod in the namespace and confirm it cannot reach an IDE pod on :8080 or :8081. If it can, the CNI does not enforce policies. Do not release the deployment ([ADR 0019](../adr/0019-network-isolation.md)).
- An unauthenticated request to `https://<instance host>/<anything>/` redirects to Keycloak and never returns IDE content.

## Upgrade and rollback

Upgrade: CRD chart first, then the control plane chart of each deployment. Do not upgrade during an exam period.
Rollback: `helm rollback` the control plane chart. The CRD chart stays; the change is additive.
