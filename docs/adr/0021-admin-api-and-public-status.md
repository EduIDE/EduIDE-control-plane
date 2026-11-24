# 0021. Read-only admin API and public status

- Status: Accepted
- Date: 2026-11-19
- Decisions: U16, U17 (numbers from [decisions.md](decisions.md))
- Contracts: [api/openapi/v1.yaml](../../api/openapi/v1.yaml), [internal/config/config.go](../../internal/config/config.go), [docs/contracts/metrics.md](../contracts/metrics.md)

## Context and Problem Statement

Operators need to see the state of a deployment: sessions, pools, workspaces, problems, templates and limits.
Until now they used `kubectl` and Grafana.
[0010](0010-rest-api-spec-first.md) ruled out an admin API in v1.
The admin panel of the landing page needs one, read-only, and the landing page needs a small public health summary.
Both must stay inside the privacy rules: users only as pseudonymous hash (G17), launch data only as key names (G18).

## Decision Drivers

- Read-only, no new binary, no new stored state.
- Access control that an identity administrator can manage without a deployment.
- Additive within `v1` ([0010](0010-rest-api-spec-first.md)).
- Works without Prometheus; Prometheus only adds figures.
- No new cluster-wide rights.

## Considered Options

1. A separate admin binary with its own API.
2. Admin routes in the service, access by a Keycloak role in the token.
3. Admin routes in the service, access by an arbitrary claim (for example `groups`) or a list of user names.

## Decision Outcome

Chosen option: "Admin routes in the service, access by a Keycloak role".

### U16: admin API

- Only `GET` under `/api/v1/admin/*`, tag `admin` in the spec. Additive in `v1`.
- **Access: a Keycloak role in the access token.** A realm role (`realm_access.roles`) or a client role (`resource_access.<client>.roles`). Deployment config `oidc.admin: {rolePath, role}`. `rolePath` has exactly these two shapes; `config.Parse` rejects everything else (for example `groups` or nested realm paths). The client id is everything between `resource_access.` and `.roles`, so Keycloak client ids with dots work; the claims are read by name, not split at dots. A role list that is not a JSON array grants nothing. The `azp` rule of the user API stays: a token another client of the realm obtained is rejected even if it carries the role. Without `oidc.admin` every admin route answers 404 (the routes do not exist, also for admins). With it, a valid token without the role gets 403 `forbidden`. The check runs on every request against the verified token; the landing page hides the menu entry from users without the role as a convenience only.
- Why a role and not a claim: roles are what Keycloak administrators assign to users and groups, they appear in the token by default (realm roles) or with one mapper (client roles), and they have an owner and an audit trail in Keycloak. Arbitrary claims such as `groups` depend on mapper settings of each realm. Group membership can still grant the role: map a Keycloak group to the role. How to create and map the role: [install.md](../operations/install.md), "Admin role".
- Own middleware: role check and a per-user rate limit (`admin.rateLimitPerMinute`, default 60 per minute over all admin routes). No new binary.
- Endpoints: `/admin/status` (+ `/events`), `/admin/pools`, `/admin/sessions` (+ `/events`, `/{name}`), `/admin/workspaces`, `/admin/problems`, `/admin/templates`, `/admin/config`.
- Lists are ordered by name and paged with an opaque `cursor` and `limit` (default 100, at most 500).
- Privacy: users only as `ownerHash` (G17). The admin `ownerHash` is the HMAC of the owner name with the deployment salt (`telemetry.userSaltFile`, required with `oidc.admin`), the same value as the user id in spans and logs. The unkeyed `owner-hash` label is not used: user names such as TUM ids are few enough to reverse a plain SHA-256 by trying them all. The owner's user name is replaced by `***` in titles, because repository names often end in it. Launch data only as the key names of the data Secret (G18), read only in the detail of one session; values are never copied into a response. Titles come from `display.title` or the derived title of `display.source`, never the source itself. No instance id, no URL of a session.
- Sources: the cache of the service (sessions, workspaces, pools, templates, effective config without secrets), Kubernetes events (Warning events on sessions for the problem list of the last hour, scaler decisions on pools), and optional Prometheus (`admin.prometheusURL`). Without Prometheus the fields that need it are absent.
- Status warnings: `pool-empty` (a target above 0 and no idle ready instance for at least 1 minute, counted from the first sight of this service replica), `capacity-pressure` (pool condition), `scaler-silent` (only with Prometheus and `admin.scalerInterval`: the scaler's heartbeat `pool_scaler_last_decision_timestamp_seconds` of this namespace is older than twice the interval, or absent because the scaler target is down or not scraped; never from missing events, since a steady pool writes none; a failed query warns about nothing).
- Workspaces: only requested bytes (spec) and an optional storage quota of the namespace; used bytes per volume and a node list would need cluster-wide rights and are left out.
- Prometheus queries select `eduide_` and `pool_scaler_` series by `namespace` of this deployment (the PodMonitor sets it), so other deployments in the cluster do not mix in; only CPU requests and the node count are cluster-wide. User input never reaches PromQL: `since` is an enum, filters apply in the service.
- Prometheus client: plain HTTP, 5 s timeout, 1 MiB response cap, no redirects, no cookies or authorization header, no URL in errors, answers cached for 15 s. The URL is admin config only; user endpoints never return it.
- SSE only for status and sessions: initial state first, at most one event per 5 s, `: ping` every 15 s, subscriptions removed when the client leaves. Everything else is loaded on open and every 30 s by the client.
- Operator: new counter `eduide_session_failures_total{template,code}` when a session fails, and the label `persistence` (`none`, `volume`) on `eduide_session_startup_seconds`, so that start p95 can be shown per template and persistence ([metrics contract](../contracts/metrics.md)).
- Pool scaler: writes a Kubernetes event on the `IDEPool` for every change of `targetIdle` (`ScaledUp`, `ScaledDown`) and when a guardrail withholds a planned change (`Held`, once until the message changes). The message holds strategy and reason. The reporting controller is `pool-scaler`.
- RBAC: service Role gets `list` and `watch` on `events.k8s.io` events and `get` and `list` on `resourcequotas` of its namespace (`list` because the quota name is not known); the pool-scaler Role gets `create` on `events.k8s.io` events.

### U17: public status

- `GET /api/v1/status`, tag `public`, no token, `security: []`. Answer cached for 15 s in the service and rebuilt by one request at a time (no stampede when it expires), `Cache-Control: public, max-age=15`, rate limited per client IP (100 per minute: enough for a campus NAT address with many open status pages, the answer is cached; the client IP is the last `X-Forwarded-For` entry, which Envoy adds, else the peer address).
- Body: `overall` (`ok`, `slow`, `down`: the worst part), `updatedAt`, `parts` (`signIn`, `starting`, `workspaces`) and per template `name`, `displayName`, `logoUrl`, `startEstimate` (`none` and `volume` with `seconds`) and `warm`. Since [0023](0023-git-accounts-and-git-proxy.md): `gitProviders` (`name`, `type`, `host`, `displayName` per configured Git provider), absent without providers.
- `starting`: the service records the outcome of every start (Ready with duration, or Failed) in memory. `slow` when the p95 of the starts of the last 15 minutes is above 60 s; `down` when more than half of the starts of the last 5 minutes failed, with at least 4 starts (else `ok`). No Prometheus.
- `signIn`: `down` when the OIDC discovery document of the issuer is not reachable. A background check runs every 30 s with a 3 s timeout, so a request never waits for the issuer. Until the first check the value is `ok`.
- `workspaces`: `down` when an `IDEWorkspace` is Pending for more than 2 minutes.
- `warm`: the pool of the template has `idleReady` above 0.
- The answer contains no user, hash, session or workspace name, host, node, capacity limit, pool target or error detail. `TestPublicStatusAllowlist` checks the field paths of a real answer against an allowlist; adding a field needs a decision.

### Consequences

- Good: operators see the state in one panel, and the access is managed in Keycloak.
- Good: all changes are additive; without `oidc.admin` nothing new is reachable except the public status.
- Good: the public status shows the state without a login and leaks nothing.
- Bad: `emptySince`, start outcomes and start estimates are per replica and start empty after a restart.
- Bad: `scaler-silent` needs Prometheus and the PodMonitor; without them there is no warning.
- Bad: the service reads events, so Kubernetes retention (about one hour) limits the problem list and scaler decisions.
- Bad: the persistence label on the startup histogram adds series (two per template and strategy).
- Follow-ups: the admin panel and the public status in the landing page (`/admin`, lazy loaded).

## Pros and Cons of the Options

### A separate admin binary

- Bad, because it adds a deployment, an image and a second auth path.

### Admin routes in the service, access by a Keycloak role

- Good, because administrators manage roles in Keycloak and the role is in the token already.
- Bad, because the role claim must reach the access token (client role needs a mapper, see install).

### Access by an arbitrary claim or a user list

- Bad, because claims differ per realm and a user list in the config needs a deployment for every change.
