# 0023. Git accounts and the Git proxy

- Status: Accepted
- Date: 2026-11-24
- Decisions: U18 (numbers from [decisions.md](decisions.md))
- Contracts: [api/openapi/v1.yaml](../../api/openapi/v1.yaml) (tag `git`), [internal/config/config.go](../../internal/config/config.go) (`GitAccounts`), [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go), Planned: [api/agent/v1.yaml](../../api/agent/v1.yaml) (`/v1/git/hosts`), a new binary `git-proxy`, NetworkPolicies

## Context and Problem Statement

Students start sessions on private repositories of GitHub and GitLab hosts.
Today the Git credentials come from the launch (Artemis VCS token) or the student types them in the session.
A student should connect a Git account once and then clone and push in every session, however the session started: start link, README badge, browser extension or `git clone` in the terminal.
Code in a session (build scripts, a terminal) must not be able to steal the token.

## Decision Drivers

- The token must never enter the session pod, not even briefly ([0012](0012-user-data-secrets-allowlist.md), G18).
- One connection per student, not per repository or course.
- No new cluster-wide rights, no external key management service.
- The feature is optional: no provider in the config means no change in behaviour.
- Each installation registers its own OAuth applications, so no university specific value lives in the repository.

## Considered Options

1. Put a token in the session (launch data or Git credential helper).
2. A Git proxy in the cluster that adds the token, with the token in the control plane.
3. SSH keys or deploy keys per repository.

## Decision Outcome

Chosen option: "a Git proxy with the token in the control plane".

### Accounts and providers (built)

- An account belongs to a user, not to a repository or an organisation.
- Providers are listed in the deployment config (`gitAccounts.providers`) and are the feature toggle: no provider, no feature, no new objects in the chart.
  Each entry has `name`, `type` (`github` or `gitlab`), `host`, `displayName`, `clientID`, `clientSecretFile` and `inactivityExpiry` (default 2160 h).
  Names and hosts are unique, the host is a bare host name. Users cannot enter hosts.
- GitHub is an OAuth App with scope `repo`. Its tokens do not expire and have no refresh token. `type: github` with another host covers GitHub Enterprise Server.
- GitLab is an OAuth application per instance with the scopes `read_repository`, `write_repository` and `read_user`. Access tokens expire after two hours, refresh tokens rotate on every use.
- The operator of each installation registers its own applications, owned by an organisation or group, one per installation and provider.
- The client secrets and the encryption key reach the service as files (a projected volume in the service pod only), like the user salt. The config names the paths, never the secrets.
- Provider hosts count as allowed hosts for `HTTPSURL` keys such as `GIT_URI`. `allowedHosts` stays for hosts without login.

### Token store (built)

- One Secret per user and provider in the deployment namespace, named `git-account-<first 32 hex of sha256(owner "\n" provider)>`.
  Labels: `app.kubernetes.io/managed-by=eduide`, `eduide.github.io/component=git-account`, `eduide.github.io/git-provider`, `eduide.github.io/owner-hash`.
- The Secret holds one AES-256-GCM encrypted JSON blob: access token, refresh token, expiry, login, numeric id, display name, no-reply e-mail and connect time.
  The authenticated data is the Secret name, so a blob moved to another Secret does not open.
- The key comes from a separate Secret (generated once by the chart, kept on upgrade). The service derives the encryption key and the HMAC key of the connect state from it with SHA-256 and a label each.
- Only the service reads and refreshes tokens. The annotation `eduide.github.io/git-last-used` records a use, written at most once per 24 hours.
- A refresh runs under a lock per account. A replica takes the lock by an optimistic update of the Secret (resourceVersion) before it calls the provider; the others wait, read again and use the new token. A lock older than 30 s is taken over.
  This keeps two replicas from presenting the same GitLab refresh token.
- The service Role needs `list`, `update` and `patch` on Secrets for this. The hashed names make a narrower rule impossible; the chart adds the verbs only with providers.

### Connect flow and REST API (built)

- `GET /git/providers` lists the providers with the state of the caller. `POST /git/providers/{name}/connect` answers with the authorize URL. `GET /git/callback` ends the flow. `DELETE /git/providers/{name}` disconnects.
- The authorize URL uses PKCE (S256). The `state` is signed (HMAC) and holds owner, provider, a nonce, an expiry of 10 minutes and an optional `returnTo`.
  The connect call also sets a short-lived cookie (`HttpOnly`, `Secure`, `SameSite=Lax`, path limited to the callback) with the nonce. The callback accepts a flow only when state and cookie match.
  This stops login CSRF: without the cookie an attacker could hand a victim a callback URL and attach the victim's Git account to the attacker's user.
- The PKCE verifier is a keyed hash of the nonce. It needs no storage and never appears in a URL.
- The callback has no bearer token (the browser arrives from the host). Every outcome is a redirect to the landing page with `git=connected&provider=<name>` or `git=error&code=<code>`. Provider error text is never shown, and code, state and tokens are never logged.
- `returnTo` is a relative path on the landing page.
- The service stores the no-reply commit address of the account (GitHub `<id>+<login>@users.noreply.github.com`, GitLab `<id>-<login>@users.noreply.<host>`). A later package decides when to use it.

### Retention (built)

- "Disconnect" revokes at the host (best effort, a failure is logged without the token) and deletes the Secret.
- An account unused for the `inactivityExpiry` of its provider is revoked and deleted. Every service replica sweeps hourly with jitter; the sweep is idempotent.
- A refresh that fails with `invalid_grant` means the user revoked the grant at the host. The service deletes the account on that use.

### Git proxy and agent (Planned)

- A separate `git-proxy` Deployment (same Go module, new binary) is reachable only inside the cluster. Session traffic never passes the service.
- The IDE container holds no token and no instance token. Git in the pod follows `url.<agent>/git/<host>/.insteadOf https://<host>/` to the loopback listener of the agent. The agent forwards `/git/` requests to the `git-proxy` Service with the instance name and instance token. The agent stays the only data path of the pod ([0006](0006-agent-sidecar.md)).
- Per request the `git-proxy` asks an internal listener of the service whether the request is allowed and which token to use, with a short cache per instance and host. All policy lives in the service: instance token, session `Ready`, owner, provider host, kind and push target.
- Every Git request to a provider host goes through the proxy. Without a connected account it passes through anonymously, so public repositories behave as before.
- Push rule: fetch and clone work for every repository the user can read. A push is allowed only to the repository the session started with (`GIT_URI`, without credentials). Sessions without a provider repository cannot push through the proxy.
- Proxy surface: Git smart HTTP only (`info/refs`, `git-upload-pack`, `git-receive-pack`), HTTPS only, only configured provider hosts. No LFS, no platform API. LFS requests get a clear error.
- NetworkPolicy: session pods may reach `git-proxy`. `git-proxy` may reach the internal port of the service and the internet. The internal port accepts only `git-proxy`. These rules follow with the chart for the proxy ([0019](0019-network-isolation.md)).
- Logging: a line per push and per denied request (user hash, session, provider, repository, kind, result). Reads only count in metrics. Never tokens or bodies.

### Landing page and launch (Planned)

- The landing page shows the provider buttons, connects and disconnects, and resumes a pending launch after the connect step.
- The service checks the repository before the launch (preflight). The preflight never blocks a launch when the host does not answer.
- For a repository on a provider with a connected account, the launch sets `GIT_USER` and `GIT_MAIL` from the account (name and no-reply address). Otherwise it uses the identity provider values as today.
- `eduide-session` writes the `insteadOf` entries before it hands the launch data to the IDE.

### Threat model

| Threat | Control |
|---|---|
| Code in the pod steals the Git token | The token never enters the pod. The IDE container has no credential for the proxy either. |
| Code in the pod pushes to other repositories of the student | Push rule: only the session repository. Residual risk: it can push to the session repository and read every repository the student can read while the session runs. |
| Stolen token store (etcd backup, `kubectl get secret`) | Values are encrypted with a key in a separate Secret. The inactivity expiry keeps the set small. |
| Compromised `git-proxy` | It sees access tokens in transit. GitLab tokens live two hours. GitHub OAuth App tokens do not expire: this is the accepted cost of a user bound token, limited by NetworkPolicy, no ingress and the revoke on disconnect. |
| Login CSRF in the connect flow | Signed state bound to a browser cookie, PKCE. |
| Session pod calls the internal port of the service | Separate listener, NetworkPolicy allows only `git-proxy`, instance token required. |
| Proxy as an open relay or for SSRF | Only provider hosts from the config, only three Git paths, HTTPS only. |
| Launch data leak | Tokens are not launch data and are not in CR specs, logs, traces, metrics or status (G18 holds). |

### Consequences

- Good: A student connects once. The token never reaches a session pod. The feature costs nothing while no provider is configured.
- Good: The state and the flow cookie need no server side storage, so every service replica can end a flow.
- Bad: GitHub OAuth App tokens do not expire, so a stolen token is valid until the user revokes it or the account expires.
- Bad: The service Role can list and update every Secret of its namespace while the feature is on.
- Bad: The operator caches Secrets with the label `managed-by=eduide`, so it also holds the encrypted blobs in memory. It has no key.
- Bad: A change of the commit identity needs a new session.
- Follow-up: Planned parts above (Git proxy, agent, preflight, landing page, `eduide-session`, NetworkPolicies, operator guide).
- Follow-up: A switch per template to turn Git accounts off (exams), approval of further push targets (forks), Git LFS through the proxy.

## Pros and Cons of the Options

### Token in the session

- Good, because simple and works with every Git client.
- Bad, because code in the session can steal it, which is the threat this decision removes.

### Git proxy with the token in the control plane

- Good, because the token stays out of the pod and the push rule is enforceable.
- Bad, because a new binary and a new network path need operating.

### SSH keys or deploy keys per repository

- Good, because the scope is narrow.
- Bad, because the user or the lecturer must create a key per repository, which defeats "connect once".
