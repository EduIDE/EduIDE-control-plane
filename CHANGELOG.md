# Changelog

All notable changes to the EduIDE control plane. One tag `vX.Y.Z` versions the images, both charts, the OpenAPI spec and the TypeScript client (see `.claude/skills/release/SKILL.md`).

## [Unreleased]

### Added

- In-IDE feedback, off by default ([ADR 0023](docs/adr/0023-in-ide-feedback-path.md)): `config.feedback` (`enabled`, `receiverURL`, `keySecret`) in the control plane chart; the render fails when enabled without receiver or key Secret
- `POST /api/v1/feedback` (API 1.2.0), authenticated by instance token and `X-EduIDE-Instance`, not by OIDC; 404 while off; 5 per hour per session owner; redaction of tokens, email addresses and matriculation numbers; forwards without user identity to the central receiver. New problem code `upstream-unavailable` (502)
- The operator sets `AGENT_FEEDBACK_URL` on the agent when feedback is on; `LocalSession.feedback` and the loopback `POST /v1/feedback` of the agent
- `eduide-session` extension: context key `eduide.feedbackEnabled` and command `eduide.feedback.send`

### Compatibility

- Additive. The CRDs do not change. Feedback is off by default; pods created before the switch is turned on (including warm pool pods) do not get it until recreated.

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
