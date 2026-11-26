# eduide-control-plane

The EduIDE control plane for one deployment (one namespace): operator, service, authz, the access policy for sessions, network isolation and optional monitoring.
Install the `eduide-control-plane-crds` chart first (once per cluster).
The Gateway, the Envoy Gateway installation, the OIDC provider and the client secret are prerequisites and not part of this chart.

```sh
helm install eduide oci://ghcr.io/eduide/eduide-control-plane/charts/eduide-control-plane \
  --version 0.2.5 -n eduide -f my-values.yaml
```

## What it renders

| Object | Purpose |
|---|---|
| Deployments `operator` (1, leader election), `service` (2), `authz` (2) | Images `<image.registry>/<name>:<tag>`, tag defaults to the chart appVersion |
| PodDisruptionBudgets for `service` and `authz` | `minAvailable: 1` |
| ServiceAccounts, namespaced Roles and RoleBindings | Operator rules equal `config/rbac/role.yaml` |
| ConfigMap `config.yaml` | Rendered from `config` (shape of `internal/config`) |
| Secret `<release>-user-salt` | Generated once with `lookup`, kept on upgrade |
| Secret `<release>-git-accounts-key` | Only with `config.gitAccounts.providers`: key of the Git account token store, generated once and kept on upgrade (ADR 0023) |
| Deployment `git-proxy` (2), PodDisruptionBudget (`maxUnavailable: 1`), ClusterIP Service | Only with `config.gitAccounts.providers` (ADR 0023). No HTTPRoute: only the agents of session pods reach it. The Service `service` gets the internal port 8083, which no route uses |
| IDETemplate and IDEPool | From `templates` |
| SecurityPolicy | OIDC login plus ext_authz (gRPC, `failOpen: false`) on all HTTPRoutes labelled `eduide.github.io/route: session` |
| HTTPRoutes | `/api/` on `config.hosts.service`; a catch-all `/` with label `eduide.github.io/route=session` on the instance and webview host (unknown ids and the OIDC callback, ADR 0005) |
| NetworkPolicies | Session pods: default deny, :8080 from Envoy, :8081 from the operator (ADR 0019). With Git providers also: session pods to `git-proxy` :8080 (a pod selector rule of its own, so the deny lists of the session egress cannot remove it), `git-proxy` accepts session pods and the metrics scrape, its egress is DNS, the service :8083 and HTTPS to the internet without the private ranges, and the service :8083 accepts `git-proxy` only |
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
The client secret Secret holds the key `client-secret` and the secret of the separate, confidential gateway client `config.oidc.gatewayClientID`.
Never make the landing page client confidential (its logins fail), and never list the gateway client in `config.oidc.clientIDs`. An installation with one shared client migrates with [Migrate from one shared client](../../docs/operations/install.md#migrate-from-one-shared-client).
`values.schema.json` rejects an install without them.
Examples use `example.org`, see `ci/test-values.yaml`.

## Values

| Key | Default | Description |
|---|---|---|
| `image.registry`, `image.tag`, `image.pullPolicy` | `ghcr.io/eduide/eduide-control-plane`, appVersion | Image of all binaries. A mutable tag such as `pr-<N>` needs `image.pullPolicy: Always`. The operator passes the policy on to the `agent` container of session pods (`config.agent.imagePullPolicy`, set only when it is not `IfNotPresent`), and a change replaces idle instances. IDE images follow `imagePullPolicy` of the template (default: Kubernetes decides from the tag), so use immutable tags or digests there, or set it |
| `imagePullSecrets` | `[]` | |
| `config` | see values.yaml | Content of `config.yaml`; `agent.image` defaults to the chart's agent image, `telemetry.userSaltFile` is set by the chart |
| `oidc.clientSecretName` | required | Existing Secret with `client-secret`, the secret of the separate confidential gateway client |
| `operator.*`, `service.*`, `authz.*` | replicas 1/2/2 | `logLevel` (debug, info, warn), `replicas`, `resources`, `nodeSelector`, `tolerations`, `affinity` |
| `templates` | `[]` | List of `{name, spec, pool}`; `spec` is the IDETemplate spec, `pool` (optional) is `{minIdle, maxInstances}`; a `targetIdle` here is not rendered, it belongs to the pool scaler (scale subresource), and Helm would otherwise conflict with it on every upgrade |
| `networkPolicy.enabled` | `true` | Needs a CNI that enforces policies |
| `networkPolicy.envoy.namespaceSelector`, `.podSelector` | `envoy-gateway-system`, `app.kubernetes.io/name: envoy` | Pods allowed to reach sessions, service and authz |
| `networkPolicy.monitoringNamespace` | `monitoring` | Namespace allowed to scrape the metrics ports |
| `networkPolicy.sessionEgress` | `[]` | Egress rules for session pods; when set, it replaces the default |
| `networkPolicy.sessionEgressDeny`, `.sessionEgressExtraDeny` | RFC 1918, link-local, 100.64.0.0/10, `fc00::/7`, `fe80::/10`; none | Default egress is DNS plus the internet without these ranges. Add the pod CIDR and the node network of the cluster to `sessionEgressExtraDeny` |
| `networkPolicy.gitProxyEgressExtra` | `[]` | Extra egress rules (a `NetworkPolicyEgressRule` list) of git-proxy, for a Git provider host on a private address, for example a self-hosted GitLab in the cluster network. Session pods get no such path. The e2e test cluster uses it for its fake provider |
| `networkPolicy.dnsNamespaceSelector` | `kube-system` | Namespace session pods resolve names in |
| `userSalt.secretName` | generated | Use an own Secret with key `salt` instead |
| `config.gitAccounts.providers` | `[]` | Git hosts users can connect an account for. Register each OAuth application with the redirect URI `https://<config.hosts.service>/git/callback` (exactly this URL). It is a landing page route, not an API path: the landing page must be served on `hosts.service` and the HTTPRoute for `/api/` must stay the only route that goes to the service there: `{name, type (github or gitlab), host, displayName, clientID, clientSecretRef {name, key}, caConfigMapRef {name, key} (optional PEM CA of a self-hosted host with a private CA, mounted into the service and the git-proxy), inactivityExpiry (default 2160h)}`. Empty means the feature is off and the chart renders nothing for it. The chart mounts the referenced Secret keys and the key Secret into the service pod only and renders the file paths into `config.yaml`. With providers the service Role also gets `list`, `update` and `patch` on Secrets: the token Secrets have hashed names, so the rule cannot be narrower. The chart also renders the git-proxy, its policies and the URLs the operator and the git-proxy need (`gitAccounts.proxyURL`, `gitAccounts.internalURL`); do not set them yourself. The Git settings of the agent are part of the pod hash, so the pool replaces idle pods after a change; without providers the hash is unchanged |
| `gitProxy.replicas`, `.logLevel`, `.resources`, `.nodeSelector`, `.tolerations`, `.affinity` | 2, `info`, 16Mi request, 64Mi limit | The git-proxy streams, so memory stays small however large a clone is. The worst case is about 0.6 MiB per instance that runs 16 authorised requests at once (`docs/operations/git-proxy.md`). It runs only with Git providers |
| `config.gitAccounts.approval` | `{mode: write, maxPerSession: 20}` | Approval of further repositories per session (ADR 0023). `mode`: `off` (no approvals, the credential is used for the session repository only), `read` (a user can approve repositories for reading) or `write` (for reading, or reading and pushing). `maxPerSession` bounds the approvals of one session. Approvals live in a Secret owned by the session; a change takes effect within 5 seconds |
| `gitAccounts.encryptionKey.secretName` | generated | Use an own Secret with key `key` (at least 32 bytes) instead. **GitOps setups (Argo CD, Flux, `helm template`) must set this**: the chart generates the key with `lookup`, which is empty when the chart is only rendered, so every sync would draw a new key and drop all connected accounts. A changed key makes stored accounts unreadable: the service treats them as not connected and deletes them on the next use, without a revoke at the host. Users connect again |
| `poolScaler.enabled`, `.mode`, `.logLevel`, `.config`, `.env`, `.resources`, `.image` | disabled, `shadow` | Config is rendered into `/etc/pool-scaler/config.yaml`, see `pool-scaler/examples/config.yaml` |
| `monitoring.podMonitor`, `.rules`, `.dashboard` | disabled | `enabled` and extra `labels`; `podMonitor.interval` defaults to 10s (the usage export needs it to resolve 20 s windows) (dashboard default label `grafana_dashboard: "1"`). Alerts carry `eduide_namespace` and select metrics by the release namespace (the PodMonitor sets the `namespace` label); the dashboards ("EduIDE control plane" and "EduIDE usage", one ConfigMap) have a namespace variable. `monitoring.rules.extraLabels` (map, default empty) is added to every alert, e.g. `{namespace: eduide-system}` for an AlertmanagerConfig that routes on that namespace. `monitoring.dashboard.namespace` (default: release namespace) places the dashboard ConfigMap where the Grafana sidecar looks. `monitoring.rules` also renders the `eduide:usage:*` recording rules that the usage export and the "EduIDE usage" board read ([usage-export](../../docs/operations/usage-export.md)). `monitoring.rules.certificates` (default true) adds certificate expiry and readiness alerts for the release namespace |
| `usageArchive.enabled`, `.prometheusURL`, `.schedule`, `.days`, `.persistence`, `.image`, `.resources` | disabled; `17 3 * * *`, 85 days, 2Gi | Daily CronJob that keeps the `eduide:usage:*` samples (one OpenMetrics file per day) and the weekly export on a PersistentVolumeClaim that `helm uninstall` keeps; `prometheusURL` and `monitoring.rules.enabled` are required when enabled ([usage-export](../../docs/operations/usage-export.md#usage-archive)) |

## Tests

`charts/test.sh` lints and renders the charts and checks: SecurityPolicy fail closed, NetworkPolicies present, no Ingress, resources and securityContext on every container, operator Role equals the generated RBAC, no Role grants `pods/exec` or wildcards, default session egress, the Git account rendering (off without providers, secrets only in the service pod), the git-proxy (no HTTPRoute, ClusterIP, policies present, no Secret volume, nothing rendered without providers), the fallback route, alerts and dashboard per namespace, memory requests below 150 MiB.
