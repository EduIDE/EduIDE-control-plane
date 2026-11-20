# Data model

API group `eduide.github.io`, version `v1alpha1`. The Go types in [api/v1alpha1](../../api/v1alpha1) are the source; the CRD YAML in [config/crd](../../config/crd) is generated from them.
Change rules: [ADR 0011](../adr/0011-crd-versioning-cel.md). Object model diagram: [object-model.svg](diagrams/object-model.svg).

## CRDs

| CRD | Purpose | Key fields |
|---|---|---|
| `IDETemplate` | Kind of IDE: image, resources, port, env, allowlist, persistence, timeouts, placement | `spec.displayName`, `spec.logoURL`, `spec.listed`, `spec.image`, `spec.port`, `spec.resources`, `spec.env`, `spec.injectableKeys`, `spec.persistence` (`allowed`, `default`, `size`, `mountPath`), `spec.timeouts` (`idle`, `warning`, `max`), `spec.retention`, `spec.placement`, `spec.bandwidth`; status `conditions` (`Ready`, `ImageMetadata`), `image` (`digest`, `version`, `variants`, `includes`, `lastChecked`) |
| `IDEPool` | Prewarmed instances of one template (optional) | `spec.templateRef`, `spec.minIdle` (floor, Helm), `spec.targetIdle` (pool-scaler), `spec.maxInstances`; status `replicas`, `idleReady`, `idleStarting`, `claimed`, `selector`; scale subresource maps to `targetIdle` |
| `IDEWorkspace` | Persistent user files | `spec.owner`, `spec.templateRef`, `spec.persistence` (`mode`, `size`, `storageClassName`, `existingClaimName`), `spec.retention`, `spec.display`; status `phase` (`Pending`, `Ready`, `Expired`), `claimName`, `lastUsed`, `expiresAt`, `expiredAt` |
| `IDESession` | One running IDE | `spec.owner`, `spec.templateRef`, `spec.workspaceRef`, `spec.dataSecretRef`, `spec.display`; status `phase`, `startStrategy`, `instance`, `podName`, `url`, `errorCode`, `message`, `timestamps`, `lastActivity` |

Notes:

- `IDEPool` is separate from the template: Helm owns the template, the pool scaler owns `targetIdle`. No field has two owners.
- `listed` (default `true`) controls only the start tiles of the landing page. `GET /api/v1/templates` returns every template with `listed`; unlisted ones still launch by name (API, Artemis `?appDef=`) and show their display name and logo in the overview.
- `injectableKeys` is the allowlist for launch data. There is no `fromSecrets` and no `fromConfigMaps`. `allowCredentials` (only with `format: HTTPSURL`) accepts user info such as an Artemis VCS token in the URL; the value then reaches only the launch data Secret ([ADR 0012](../adr/0012-user-data-secrets-allowlist.md)).
- `persistence.mode` is `none` or `volume`. **Planned:** `snapshot` (optional phase 9). The enum allows it additively.
- A session with a `workspaceRef` always starts cold. The startup strategy shows it as `cold`.
- `IDETemplate.status.image` comes from the OCI annotation `io.eduide.template` of the image manifest, read by the operator once per digest and at most every 10 minutes per tag ([0020](../adr/0020-landing-page-data-in-the-api.md)). Anonymous pulls only. The condition `ImageMetadata` says why the block is empty (`AnnotationMissing`, `AnnotationInvalid`, `RegistryError`) and never affects `Ready`.
- The service computes `stopsAt`, `stopReason` and `deletesAt` from `status.lastActivity`, the template timeouts and `status.expiresAt` plus `workspaces.grace`; they are not stored.
- Status of every CRD carries `observedGeneration` and `conditions`.
- Launch data is never part of a CR spec or status.

### Session phases

`Pending`, `Starting`, `Ready`, `Failed`.
Error codes in `status.errorCode`: `capacity-exhausted`, `template-not-found`, `workspace-not-found`, `workspace-lost`, `workspace-in-use` (unused since U14), `start-failed`.
A session that has an instance keeps running when its template is deleted; it ends by the default timeouts. Only a session without an instance fails with `template-not-found`.
Duration fields (`timeouts.*`, `retention`) accept Go durations such as `90m` or `48h`; the CRD rejects `2d`. `timeouts.max` must not be shorter than `timeouts.idle`. The operator and the service apply the resolved values (`ResolvedTimeouts` in `api/v1alpha1/helpers.go`): a missing or zero `idle` or `max` is the default, and a `warning` that does not come before the idle end is moved to five sixths of `idle`.
Timestamps in `status.timestamps`: `claimed`, `podReady`, `agentReady`, `ready` (see [start-path.md](start-path.md)).
Start strategies: `warm-ready`, `warm-starting`, `cold`.

## Labels and annotations

Source: [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go). Prefix `eduide.github.io/`.

| Key | Values | Set on | Read by |
|---|---|---|---|
| `app.kubernetes.io/managed-by` | `eduide` | everything the control plane creates | informers (filter) |
| `eduide.github.io/component` | `session`, `workspace` | pods, services, routes, PVCs | operator |
| `eduide.github.io/template` | template name | session pods, pools | operator, scaler |
| `eduide.github.io/pool` | pool name | pool pods (kept after claim) | operator |
| `eduide.github.io/session` | session name | claimed or cold pods | operator, authz |
| `eduide.github.io/workspace` | workspace name | PVCs, sessions | operator |
| `eduide.github.io/instance` | instance id | all objects of an instance | operator, authz |
| `eduide.github.io/state` | `idle`, `claimed` | session pods | operator (claim) |
| `eduide.github.io/owner-hash` | hash of the owner | pods, sessions, workspaces | service (owner lists) |
| `eduide.github.io/launch-key` | idempotency key | `IDESession` | service (launch is idempotent) |
| `eduide.github.io/route` | `session` | session HTTPRoutes | SecurityPolicy selector |
| `eduide.github.io/node-pool` | node pool label value | session pods | operator (capacity per node pool) |

| Annotation | Meaning |
|---|---|
| `eduide.github.io/traceparent` | W3C trace context of the launch, set by the service |
| `eduide.github.io/retain` | `"true"` on an `IDEWorkspace` cancels expiry |
| `eduide.github.io/scaler-mode`, `eduide.github.io/scaler-override` | Pool scaler control on `IDEPool` |

Other constants: header `X-EduIDE-Instance-Token`, Secret key `token`, finalizer `eduide.github.io/session` on `IDESession`.

## Naming

| Object | Name |
|---|---|
| Instance id | 20 characters, lowercase base32 without padding of random bytes (`crypto/rand`). Unguessable. First URL path segment and label `instance`. |
| Session pod | `ide-<instance>` |
| Service | `ide-<instance>`, port 8080 `http` to the agent proxy |
| HTTPRoute | `ide-<instance>` |
| Instance token Secret | `ide-<instance>-token`, key `token`, 32 random bytes hex |
| Session | `s-<10 random [a-z0-9]>`, created by the service |
| Launch data Secret | `<session>-data`, key set by the allowlist |
| Workspace | `w-<first 16 hex of sha256(owner \x00 template \x00 source)>`; `source` is `GIT_URI` or `display.source`, and without both `template:<TEMPLATE>` (the starter template), so each starter template gets its own saved workspace. Get-or-create, so the same input gives the same workspace. |
| Workspace PVC | same name as the workspace, also in `status.claimName` |

## Ownership

| Object | Owner reference |
|---|---|
| Pool pod | `IDEPool` (controller ref); on claim the controller ref moves to the `IDESession` |
| Service, HTTPRoute, token Secret | the pod |
| Launch data Secret | the `IDESession` |
| Workspace PVC | the `IDEWorkspace` |
| `IDESession` with a workspace | the `IDEWorkspace` (no controller ref), set by the service at launch, so a session launched while its workspace is deleted goes with it (U14) |

Kubernetes garbage collection removes dependents. No manual cleanup.

## Session pod

Containers: `ide` (template image, env, resources, no launch data) and `agent` (ports 8080 `proxy`, 8081 `control`; readiness `/readyz` and liveness `/healthz` on 8081; env `AGENT_IDE_PORT`, `AGENT_TOKEN_FILE=/var/run/eduide/token`, `AGENT_PATH_PREFIX=/<instance>/`, `AGENT_ID_TOKEN_COOKIE=<oidc.idTokenCookie>`). The agent moves explicit cookie paths of IDE responses below `AGENT_PATH_PREFIX` (like nginx `proxy_cookie_path`), drops their `Domain` attribute and drops cookies named like the gateway login cookies, because all instances share one host and the IDE sees itself at `/`: Theia's `theia-connection-token` with `Path=/` would otherwise be overwritten by every other IDE of the student, whose requests then fail with 403.
The workspace mounts at `persistence.mountPath` (default `/home/project`), otherwise nothing is mounted (sessions with persistence `none` write to the container filesystem).
Bandwidth annotations `kubernetes.io/ingress-bandwidth` and `kubernetes.io/egress-bandwidth` come from the template.

## Deployment config

`/etc/eduide/config.yaml` from a ConfigMap, read with `--config`. Typed in [internal/config/config.go](../../internal/config/config.go).
Required: `deployment`, `namespace` (or `POD_NAMESPACE`), `hosts.service`, `hosts.instance`, `gateway.name`, `oidc.issuerURL`, `oidc.gatewayClientID`, `oidc.clientIDs`, `agent.image`.
Defaults: `sessionsPerUser` 3, `workspaces.perUser` 20, `oidc.userClaim` `preferred_username`, `oidc.idTokenCookie` `eduide-id`, `workspaces.retention` 14 days, `workspaces.grace` 24 h, `capacity.exhaustedAfter` 45 s, `rateLimit.launchesPerMinute` 10.
Optional admin API (U16, [0021](../adr/0021-admin-api-and-public-status.md)): `oidc.admin` (`rolePath` `realm_access.roles` or `resource_access.<client>.roles`, and `role`; no default, absent means the admin API is off), `admin.prometheusURL`, `admin.links` (`grafana`, `jaeger`, `traceUrlTemplate`), `admin.rateLimitPerMinute` (default 60) and `admin.scalerInterval` (set by the chart when the pool scaler runs).
The admin API reads the objects above through the cache and shows owners only as the owner hash label value, launch data only as the key names of the data Secret.
The pool scaler writes Events on `IDEPool` (reporting controller `pool-scaler`, reasons `ScaledUp`, `ScaledDown`, `Held`); the service reads them.
Secrets come from files or environment variables, never from flags or the config file.
