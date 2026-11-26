# Changelog

All notable changes to the EduIDE control plane. One tag `vX.Y.Z` versions the images, both charts, the OpenAPI spec and the TypeScript client (see `.claude/skills/release/SKILL.md`).

## [Unreleased]

### Added

- Git accounts ([ADR 0023](docs/adr/0023-git-accounts-and-git-proxy.md)): users connect a GitHub or GitLab account through the new endpoints `GET /git/providers`, `POST /git/providers/{name}/connect`, `POST /git/callback` and `DELETE /git/providers/{name}` (OpenAPI 1.2.0). The flow needs no cookie: the Git host redirects to the landing page route `https://<hosts.service>/git/callback`, and the landing page posts `code` and `state` with the user's token. The token stays in the control plane, in one encrypted Secret per user and provider. The feature is off without `gitAccounts.providers` in the config
- Config `gitAccounts` (providers, `inactivityExpiry`, `encryptionKeyFile`, `proxyURL`, `internalURL`, `approval.mode` and `maxPerSession`, and `providers[].caFile` for a self-hosted Git host with a private CA); chart values `config.gitAccounts`, `gitAccounts.encryptionKey` (a generated key Secret), `config.gitAccounts.approval`, `caConfigMapRef`, `gitProxy.*` and `networkPolicy.gitProxyEgressExtra` (default empty; git-proxy egress to a provider inside the cluster, used by the e2e), and the Secret rights of the service Role that the feature needs
- Provider hosts count as allowed hosts for the key `GIT_URI` only, not for other `HTTPSURL` keys such as `ARTEMIS_URL`
- `git-proxy` (new binary and image `git-proxy`, chart Deployment with 2 replicas, PodDisruptionBudget with `maxUnavailable: 1` and ClusterIP Service, only with `config.gitAccounts.providers`): streams Git smart HTTP from session pods to the provider hosts. The token of the user stays in the control plane and is used for the repository the session was started with and the repositories the user approved; other repositories are read anonymously, and a push to any other repository is refused. Errors reach the user as `fatal: remote error: ...` messages, on a push through the side-band channel that git reads. The upstream path is the authorised repository with `.git` plus the fixed Git suffix (GitLab refuses a path without `.git`). A request is cut after 5 minutes without a moved byte, so a slow transfer that keeps moving is never cut, and the wait for the provider after the upload has its own limit of 30 minutes. It limits requests per client IP (100 per second, burst 400) and per instance (16 at a time), caps the error message at 1000 bytes, retries an authorize call once after a 429, and reuses an allowed answer with a credential for at most 5 seconds, so a disconnect or a removed approval takes effect at once. NetworkPolicies `session-to-git-proxy`, `git-proxy` and `service-internal`. Runbook: `docs/operations/git-proxy.md`
- One repository path rule (`gitapi.CanonicalRepo`) for the git-proxy, the approval API, the preflight and the landing page: 2 to 10 segments, none of them `.`, `..` or `.git`, `.git` only as the final suffix. Names with a leading dot such as `org/.github` are valid
- Service: internal listener `:8083` with `POST /internal/v1/git/authorize` (contract in `internal/gitapi`, ADR 0023), served only with providers and reachable only from the git-proxy. It checks the instance token before it charges the per-instance limiter (20 calls per second, burst 100) and caches the token hash, so wrong tokens cannot lock out a session or cost API calls; its limiter and caches are bounded
- Agent: `/git/<host>/...` on the loopback listener (only while claimed) and `GET /v1/git/hosts` (`api/agent/v1.yaml`); the operator passes `AGENT_GIT_PROXY_URL`, `AGENT_GIT_HOSTS` and `AGENT_GIT_LANDING_URL` to new session pods. The Git settings are part of the pod hash, so the pool replaces idle pods after a change of the Git accounts config; a deployment without providers keeps its hash, so an upgrade replaces no idle pod
- Metrics `eduide_gitproxy_requests_total`, `eduide_gitproxy_bytes_total`, `eduide_gitproxy_upstream_seconds`
- `POST /git/preflight` (does a launch with this repository need a step first; it sends the token of the user to Git repository paths only), the commit identity of a launch on a provider repository (`GIT_USER` and `GIT_MAIL` from the account; the service removes control characters and angle brackets, caps the length and uses the no-reply address only when it is a plain address), and `gitProviders` in the public `GET /status` ([ADR 0023](docs/adr/0023-git-accounts-and-git-proxy.md), [ADR 0021](docs/adr/0021-admin-api-and-public-status.md))
- Repository approval: a user approves further repositories for one session outside the pod (`GET`, `PUT` and `DELETE /sessions/{name}/git/repositories`). The approval mode is `off`, `read` or `write` (default `write`), at most 20 repositories per session by default. git-proxy messages carry an approval link. Approvals live in a Secret owned by the session
- Account lifecycle: an hourly sweep revokes and deletes Git accounts that no session used for `inactivityExpiry`; a reconnect revokes the replaced token; GitHub tokens are revoked one by one (`DELETE /applications/{client_id}/token`, a 422 counts as already revoked); no no-reply address is guessed for GitHub Enterprise Server hosts; a token refresh survives the end of the request, and an `invalid_grant` deletes the account only if the Secret still holds the refresh token that was sent
- Landing page: a "Git accounts" section (connect, disconnect with a confirmation; disconnect deletes with the version it read and revokes after the delete, so a reconnect in between is kept; "Connect another account" connects first and does not disconnect the working account), the route `/git/callback`, a repository check before a launch with a `gitUri` (`connect` and `no-access` stop the launch with a clear message, any failing request launches as before), a template picker for a link with `gitUri` and no usable `appDef`, and the repository approval page for the git-proxy link (`/?session=...&approveRepo=...&access=...`; nothing is approved without a click on "Allow", a link with a percent escape is refused, and the page shows the session's repository next to the title). English and German. The committed API client is synced with the Git endpoints
- `eduide-session` 0.2.0: writes the `url.<agent>/git/<host>/.insteadOf` entries from `GET /v1/git/hosts` before it hands launch data to other extensions, and removes entries of hosts that left the list. The IDE image must ship this version for the feature to reach real IDEs
- E2E: a fake Git provider that behaves like GitLab, including the redirect of a path without `.git` (`test/fake-git`), a Git capable fake IDE, the test `TestGitAccounts` and the Playwright spec `git-accounts.spec.ts` in the Git phase (`docs/development/testing.md`). The tests check that the approval call of a pod and a call with a made-up token answer 401, that the gateway's login cookies are HttpOnly and never reach the IDE, and that the fake accepts only its registered redirect URI
- Docs: developer page `docs/development/git-accounts.md`; runbook notes for rotating a client secret, removing a provider and a provider on a private address (`docs/operations/git-proxy.md`)

### Changed

- Docs: the installation guide requires two Keycloak clients with exact redirect URIs and a confidential gateway client, and ADR 0023 documents why (an open risk for deployments that use one client for both). The Keycloak guidance warns never to switch "Client authentication" on for the shared landing page client (it breaks every landing page login) and adds the procedure "Migrate from one shared client" (not yet tested on a real installation). Changing `gatewayClientID` ends every open IDE login once: the users sign in again, running sessions are not affected

## [0.2.5] - 2026-11-25

### Fixed

- Operator: the late start phases from the IDE milestones (`browser_connect` to `ide_usable` in `eduide_session_phase_seconds`, usage metric M4) are recorded without tracing too; before, installations without an OTLP endpoint had no M4 data
- Usage archive docs: the copy command leaves out `lost+found`

### Compatibility

- CRD and API version `v1alpha1` / `v1`, unchanged; no chart value changes.

## [0.2.4] - 2026-11-24

### Added

- Usage archive: `usageArchive.enabled` in the control plane chart adds a daily CronJob and a kept PersistentVolumeClaim; `usage-export --archive` writes every `eduide:usage:*` sample of a day as gzipped OpenMetrics (loadable with `promtool`) and catches up missed days, so the usage data outlives the Prometheus retention
- Alert `EduIDEUsageArchiveStale` when the usage archive has not succeeded for 36 hours
- Image `usage-export`

### Compatibility

- CRD and API version `v1alpha1` / `v1`, unchanged. The CRD chart has no changes; upgrade it first as always, then the control plane chart per deployment. `usageArchive` is off by default.

## [0.2.3] - 2026-11-21

### Added

- `usage-export`: command that writes aggregated, anonymous usage data (weekly CSV, context, README; schema version 2) from Prometheus, with a small-cell rule and an identifier check (`docs/operations/usage-export.md`)
- Recording rules `eduide:usage:*` in the control plane chart, rendered with `monitoring.rules.enabled`; `usage-export` and the board read only these series
- Grafana board "EduIDE usage" with one row per usage metric M1 to M7, shipped in the dashboard ConfigMap (`monitoring.dashboard.enabled`)
- Usage rules for restarts and OOM kills of IDE pods (`eduide:usage:ide_restarts_5m`, `eduide:usage:ide_oom_kills_5m`) and installation-wide launch peaks and running sessions

### Changed

- Config `agent.imagePullPolicy` (optional): the operator sets it on the agent container of session pods. The chart fills it from `image.pullPolicy` when that is not `IfNotPresent`, so a test deployment with a mutable `pr-<N>` tag and `Always` no longer keeps an old agent on cached nodes. Without it nothing changes; when it is set or changed, idle instances are replaced
- CI runs the promtool unit tests of the recording rules

### Fixed

- Usage rules: a kube-state-metrics restart no longer counts every IDE container restart again; running sessions record 0 for templates without Ready sessions, so weekly quantiles include quiet minutes; the memory and CPU split no longer fails while two kube-state-metrics replicas disagree on the state of an instance

## [0.2.2] - 2026-11-20

### Added

- Usage metrics: label `start_mode` (`eager`, `lazy`, `lazy_fallback`, `none`) on `eduide_session_startup_seconds`, label `template` on `eduide_session_phase_seconds`, finer startup buckets, and the new metrics `eduide_pool_min_idle`, `eduide_pool_max_instances`, `eduide_session_lifetime_seconds` and `eduide_template_resources`, `eduide_template_timeout_seconds`

### Fixed

- The landing page names an environment started with a project template after it ("Web", "Node.js"), so several saved workspaces of one template are told apart in "Your environments"
- Launch traces are complete in Jaeger: the service starts `launch` as the root span and keeps the landing page `traceparent` as a link (it was an unexported parent, so most traces showed as incomplete and were listed by trace id); ADR 0022, OpenAPI 1.1.1

## [0.2.1] - 2026-11-20

### Fixed

- A saved workspace launched without a repository is per starter template: picking "Web" no longer reopens the "Node.js" workspace with its files and ignores the choice

## [0.2.0] - 2026-11-20

The control plane of EduIDE 3.0.0: fixes from the audit of 2026-11-19, listed templates, and the repository is no longer marked experimental.

### Added

- `workspaces.perUser` (default 20) limits the workspaces of one user; beyond it the API answers the new problem code `workspace-limit-reached` (409)
- `IDETemplate.ResolvedTimeouts()`: one place that resolves the session timeouts for operator, service and admin API
- `IDETemplate.spec.listed` (default `true`) and `Template.listed` in the REST API: the landing page shows start tiles only for listed templates. Unlisted templates still launch by name through the API and Artemis links, and their sessions keep display name and logo in "Your environments"

### Changed

- `sessionsPerUser` holds for parallel launches, also across service replicas
- `DELETE /workspaces/{name}` shares the launch budget and runs one at a time per user
- At most 16 event streams per user; a stream ends when its client stops reading for 30 s
- A session whose pod is unschedulable for a reason other than room fails with `start-failed` and no longer puts its node pool under pressure
- The admin API shows the resolved timeouts of a template, not only the fields it sets
- Pool scaler: only the claim results `claimed` and `empty` count as demand; a Prometheus answer without series is unknown demand, not zero; the default `prometheus.timeout` is 10 s
- Chart: Deployments with several replicas spread over nodes; the pool scaler Role no longer grants `patch` on `idepools`
- CI: actions pinned by commit SHA, least-privilege workflow permissions, release gate on tags, image scan before push, nightly vulnerability scans, aggregate job `ci-ok`; a new value in a response enum only warns in `oasdiff`

### Fixed

- A relaunch right after a stop no longer fails on the terminating pod of the stopped session
- A Ready session fails when its pod failed (eviction, node shutdown) instead of staying Ready with a dead URL
- A workspace no longer reports its volume as lost while the claim cache is behind
- A template that sets only `timeouts.idle` still gets its idle warning; a zero `idle` or `max` is the default
- An imported claim that another workspace controls is not shared
- A user at the workspace limit gets an existing workspace back also from a replica whose cache has not seen it yet, instead of `workspace-limit-reached`
- The admin template list survives a template deleted between two reads

### Security

- The agent client of the operator never follows redirects
- The agent bounds headers and connections on all three listeners and strips the configured ID token cookie (`oidc.idTokenCookie`)
- The service bounds request body reads, idle connections and header size; requests without a valid token write at most 20 log lines per minute; without a salt, logs carry no user id
- The landing page moves the deprecated `?artemisToken=` and a `?gitUri=` with credentials out of the URL before the login

### Compatibility

- Additive: the CRD field `IDETemplate.spec.listed` and the API field `Template.listed` (OpenAPI 1.1.0). Upgrade order: CRD chart first, then the control plane chart per deployment.

## [0.1.0] - 2026-11-20

First release of the Go control plane that replaces EduIDE-Cloud (Java). Still marked experimental in this repository.

### Added

- `operator`, `service`, `authz` and `agent` with the CRDs `IDETemplate`, `IDEPool`, `IDEWorkspace` and `IDESession` (`eduide.github.io/v1alpha1`) and the REST API `/api/v1` (OpenAPI 3.1)
- Session auth at Envoy Gateway (SecurityPolicy with OIDC, ext_authz through `authz`), the agent as the only door into the session pod
- Prewarmed pools with claim protocol, capacity pressure per node pool, persistence `none` and `volume`, workspace deletion after last use
- Landing page, pool scaler, the `eduide-session` extension and the charts `eduide-control-plane-crds`, `eduide-control-plane` and `eduide-landing-page`
- Artemis launches: token and repository URL with credentials in the URL fragment (`allowCredentials` for `GIT_URI`)

### Compatibility

- CRD and API version `v1alpha1` / `v1`. Upgrade order: CRD chart first, then the control plane chart per deployment.
