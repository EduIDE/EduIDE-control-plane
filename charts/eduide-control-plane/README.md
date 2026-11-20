# eduide-control-plane

The EduIDE control plane for one deployment (one namespace): operator, service, authz, the access policy for sessions, network isolation and optional monitoring.
Install the `eduide-control-plane-crds` chart first (once per cluster).
The Gateway, the Envoy Gateway installation, the OIDC provider and the client secret are prerequisites and not part of this chart.

```sh
helm install eduide oci://ghcr.io/eduide/eduide-control-plane/charts/eduide-control-plane \
  --version 0.2.1 -n eduide -f my-values.yaml
```

## What it renders

| Object | Purpose |
|---|---|
| Deployments `operator` (1, leader election), `service` (2), `authz` (2) | Images `<image.registry>/<name>:<tag>`, tag defaults to the chart appVersion |
| PodDisruptionBudgets for `service` and `authz` | `minAvailable: 1` |
| ServiceAccounts, namespaced Roles and RoleBindings | Operator rules equal `config/rbac/role.yaml` |
| ConfigMap `config.yaml` | Rendered from `config` (shape of `internal/config`) |
| Secret `<release>-user-salt` | Generated once with `lookup`, kept on upgrade |
| IDETemplate and IDEPool | From `templates` |
| SecurityPolicy | OIDC login plus ext_authz (gRPC, `failOpen: false`) on all HTTPRoutes labelled `eduide.github.io/route: session` |
| HTTPRoutes | `/api/` on `config.hosts.service`; a catch-all `/` with label `eduide.github.io/route=session` on the instance and webview host (unknown ids and the OIDC callback, ADR 0005) |
| NetworkPolicies | Session pods: default deny, :8080 from Envoy, :8081 from the operator (ADR 0019) |
| Pool scaler (optional) | Deployment, config, RBAC for `idepools` and `idepools/scale`, and `create` on `events.k8s.io` events (decision events on the pool) |
| PodMonitor, PrometheusRule, dashboard (optional) | Based on `docs/contracts/metrics.md` |

Admin API (U16, optional): `config.oidc.admin` (`rolePath`, `role`) turns it on, `config.admin` sets `prometheusURL`, `links` and `rateLimitPerMinute`.
The service Role then needs `list` and `watch` on `events.k8s.io` events and `get` and `list` on `resourcequotas`; the chart always grants them.
With the pool scaler enabled the chart sets `config.admin.scalerInterval` from `poolScaler.config.interval` (default 30s).
The public status (`GET /api/v1/status`, U17) needs no values.

No Ingress is rendered.
The memory requests of operator, service and authz add up to 128 MiB.

## Values a deployment must set

`config.deployment`, `config.hosts.service`, `config.hosts.instance`, `config.gateway.name`, `config.oidc.issuerURL`, `config.oidc.clientIDs`, `config.oidc.gatewayClientID` and `oidc.clientSecretName`.
The client secret Secret holds the key `client-secret`.
`values.schema.json` rejects an install without them.
Examples use `example.org`, see `ci/test-values.yaml`.

## Values

| Key | Default | Description |
|---|---|---|
| `image.registry`, `image.tag`, `image.pullPolicy` | `ghcr.io/eduide/eduide-control-plane`, appVersion | Image of all binaries |
| `imagePullSecrets` | `[]` | |
| `config` | see values.yaml | Content of `config.yaml`; `agent.image` defaults to the chart's agent image, `telemetry.userSaltFile` is set by the chart |
| `oidc.clientSecretName` | required | Existing Secret with `client-secret` |
| `operator.*`, `service.*`, `authz.*` | replicas 1/2/2 | `logLevel` (debug, info, warn), `replicas`, `resources`, `nodeSelector`, `tolerations`, `affinity` |
| `templates` | `[]` | List of `{name, spec, pool}`; `spec` is the IDETemplate spec, `pool` (optional) is `{minIdle, maxInstances}`; a `targetIdle` here is not rendered, it belongs to the pool scaler (scale subresource), and Helm would otherwise conflict with it on every upgrade |
| `networkPolicy.enabled` | `true` | Needs a CNI that enforces policies |
| `networkPolicy.envoy.namespaceSelector`, `.podSelector` | `envoy-gateway-system`, `app.kubernetes.io/name: envoy` | Pods allowed to reach sessions, service and authz |
| `networkPolicy.monitoringNamespace` | `monitoring` | Namespace allowed to scrape the metrics ports |
| `networkPolicy.sessionEgress` | `[]` | Egress rules for session pods; when set, it replaces the default |
| `networkPolicy.sessionEgressDeny`, `.sessionEgressExtraDeny` | RFC 1918, link-local, 100.64.0.0/10, `fc00::/7`, `fe80::/10`; none | Default egress is DNS plus the internet without these ranges. Add the pod CIDR and the node network of the cluster to `sessionEgressExtraDeny` |
| `networkPolicy.dnsNamespaceSelector` | `kube-system` | Namespace session pods resolve names in |
| `userSalt.secretName` | generated | Use an own Secret with key `salt` instead |
| `poolScaler.enabled`, `.mode`, `.logLevel`, `.config`, `.env`, `.resources`, `.image` | disabled, `shadow` | Config is rendered into `/etc/pool-scaler/config.yaml`, see `pool-scaler/examples/config.yaml` |
| `monitoring.podMonitor`, `.rules`, `.dashboard` | disabled | `enabled` and extra `labels` (dashboard default label `grafana_dashboard: "1"`). Alerts carry `eduide_namespace` and select metrics by the release namespace (the PodMonitor sets the `namespace` label); the dashboard has a namespace variable. `monitoring.rules.extraLabels` (map, default empty) is added to every alert, e.g. `{namespace: eduide-system}` for an AlertmanagerConfig that routes on that namespace. `monitoring.dashboard.namespace` (default: release namespace) places the dashboard ConfigMap where the Grafana sidecar looks. `monitoring.rules` also renders the `eduide:usage:*` recording rules for the usage export ([usage-export](../../docs/operations/usage-export.md)). `monitoring.rules.certificates` (default true) adds certificate expiry and readiness alerts for the release namespace |

## Tests

`charts/test.sh` lints and renders the charts and checks: SecurityPolicy fail closed, NetworkPolicies present, no Ingress, resources and securityContext on every container, operator Role equals the generated RBAC, no Role grants `pods/exec` or wildcards, default session egress, the fallback route, alerts and dashboard per namespace, memory requests below 150 MiB.
