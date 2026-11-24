# 0023. Git accounts and the Git proxy

- Status: Accepted
- Date: 2026-11-24
- Decisions: U18 (numbers from [decisions.md](decisions.md))
- Contracts: [api/openapi/v1.yaml](../../api/openapi/v1.yaml) (tag `git`), [internal/config/config.go](../../internal/config/config.go) (`GitAccounts`), [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go), [api/agent/v1.yaml](../../api/agent/v1.yaml) (`/v1/git/hosts`), [internal/gitapi](../../internal/gitapi/gitapi.go) (internal authorize contract), the binary `git-proxy`, NetworkPolicies

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
  Each entry has `name`, `type` (`github` or `gitlab`), `host`, `displayName`, `clientID`, `clientSecretFile` and `caFile` (optional) and `inactivityExpiry` (default 2160 h).
  Names and hosts are unique, the host is a bare host name. Users cannot enter hosts.
- GitHub is an OAuth App with scope `repo`. Its tokens do not expire and have no refresh token. `type: github` with another host covers GitHub Enterprise Server.
- GitLab is an OAuth application per instance with the scopes `read_repository`, `write_repository` and `read_user`. Access tokens expire after two hours, refresh tokens rotate on every use.
- The operator of each installation registers its own applications, owned by an organisation or group, one per installation and provider. **The redirect URI is exactly `https://<hosts.service>/git/callback`.** It is a route of the landing page, not of the API (see the connect flow).
- A self-hosted instance with a private CA sets `caFile` (a PEM bundle, added to the system roots). The service uses it for OAuth, the account lookup and the preflight, and the git-proxy for its upstream connection. The chart mounts the bundle from a ConfigMap (`caConfigMapRef`) into the service and the git-proxy.
- The client secrets and the encryption key reach the service as files (a projected volume in the service pod only), like the user salt. The config names the paths, never the secrets.
- Provider hosts count as allowed hosts for the key `GIT_URI` only, not for other `HTTPSURL` keys such as `ARTEMIS_URL`: those name servers that receive requests with credentials. `allowedHosts` stays for hosts without login.

### Token store (built)

- One Secret per user and provider in the deployment namespace, named `git-account-<first 32 hex of sha256(owner "\n" provider)>`.
  Labels: `eduide.github.io/component=git-account`, `eduide.github.io/git-provider`, `eduide.github.io/owner-hash`.
- The Secret holds one AES-256-GCM encrypted JSON blob: access token, refresh token, expiry, login, numeric id, display name, no-reply e-mail and connect time.
  The authenticated data is the Secret name, so a blob moved to another Secret does not open.
- The Secrets carry no `app.kubernetes.io/managed-by` label: the operator's Secret cache selects on it, and the operator has no use for these Secrets. The sweep selects them by their component label.
- The key comes from a separate Secret (generated once by the chart, kept on upgrade). The key file is read byte for byte, only a trailing newline is dropped. A changed key makes the stored accounts unreadable: the service treats them as not connected and deletes them on the next use without a revoke at the host. GitOps setups must set `gitAccounts.encryptionKey.secretName`, because the chart generates the key with `lookup`, which is empty when the chart is only rendered. The service derives the encryption key and the HMAC key of the connect state from it with SHA-256 and a label each.
- Only the service reads and refreshes tokens. The annotation `eduide.github.io/git-last-used` records a use, written at most once per 24 hours.
- A refresh runs under a lock per account. A replica takes the lock by an optimistic update of the Secret (resourceVersion) before it calls the provider; the others wait, read again and use the new token. A lock older than 30 s is taken over.
  This keeps two replicas from presenting the same GitLab refresh token.
- The service Role needs `list`, `update` and `patch` on Secrets for this. The hashed names make a narrower rule impossible; the chart adds the verbs only with providers.

### Connect flow and REST API (built)

All endpoints need the bearer token. Nothing about Git accounts is public except the provider list in `GET /status` (below).

- `GET /git/providers` lists the providers with the state of the caller. `POST /git/providers/{name}/connect` (body `{returnTo?}`) answers `{authorizeUrl}`. `POST /git/callback` (body `{code, state}` or `{error, state}`) ends the flow and answers `{provider, login, returnTo}`. `DELETE /git/providers/{name}` disconnects.
- **No cookie.** The Git host redirects the browser to `https://<hosts.service>/git/callback`. The landing page server answers every path without a file extension with the single-page app (`landing-page/server`, SPA fallback), and the chart routes only `/api/` on `hosts.service` to the service, so this path reaches the landing page and not the API. The page reads `code` and `state` (GitHub and GitLab append them as query parameters) and posts them with its own bearer token. The landing page must be served on `hosts.service` for this.
- The state is sealed with the AEAD of the token store (AAD `state`): it holds owner, provider, a nonce, an expiry of 10 minutes and an optional `returnTo`. Nothing readable about the user reaches the Git host, browser history or access logs. The callback requires that the state owner equals the bearer owner. A state planted in another browser (login CSRF) therefore fails: the victim's bearer does not match the attacker's state, and the attacker cannot attach anything to the victim.
- The authorize URL uses PKCE (S256). The PKCE verifier is a keyed hash of the nonce. It needs no storage and never appears in a URL.
- Two flows at the same time (two providers, two tabs) both succeed, because no per-browser state exists. A state is not single use: single use would need a shared store written per flow, which is not cheap with several replicas. The expiry of 10 minutes, the single-use code of the provider and the owner check bound a replay.
- Failures are problems with a fixed code: `git-invalid-state` (also for a state of another user), `git-state-expired`, `git-denied` (`error=access_denied`), `git-provider-error`. Provider error text, code, state and tokens are never returned or logged. `returnTo` is a relative path on the landing page; the callback answers it, `/` when none was given.
- Connect, callback, preflight and the approval endpoints are rate limited per user (the launch rate, separate buckets).
- A reconnect replaces the record, keeps no lock annotation (it fails with `rate-limited` while a refresh runs) and revokes the replaced token after the update succeeded. GitHub tokens are revoked one by one (`DELETE /applications/{client_id}/token`), not by grant, so tokens of the user's other devices stay.
- The service stores the no-reply commit address of the account: GitHub `<id>+<login>@users.noreply.github.com` for github.com, GitLab `<id>-<login>@users.noreply.<host>`. For a GitHub Enterprise Server host the format is not known, so the service stores none and the launch falls back to the identity provider values.

#### Preflight

`POST /git/preflight` with `{gitUri}` answers `{result, provider?}`. It sends one `GET <repo>/info/refs?service=git-upload-pack` to the provider host with a 3 second timeout, with the user's credential if an account exists, never follows a redirect, and discards the body. Only configured provider hosts on port 443 are asked, over HTTPS, with the URL rules of the launch key `GIT_URI` (no credentials). The URL and the credential are never logged.

| Result | Meaning |
|---|---|
| `ok` | Reachable, with the credential or anonymously |
| `connect` | Provider host, no account (or the token no longer works) and not readable anonymously |
| `no-access` | Account connected, the host says 403 or 404 |
| `not-a-provider` | The host is no provider, nothing was checked |
| `unknown` | Timeout, 5xx, redirect or any other answer: the launch must not be blocked |

#### Commit identity at launch

For a launch with a `GIT_URI` on a provider host where the caller has an account with a stored no-reply address, the service sets `GIT_USER` to the display name (the login when empty) and `GIT_MAIL` to the no-reply address, but only for keys the template takes from the token (`source: UserName` or `UserEmail`). Any other launch keeps its data. The launch key hashes the data as sent by the user, so a changed display name does not start a second session.

#### Public status

`GET /status` adds `gitProviders: [{name, type, host, displayName}]` when providers are configured (absent otherwise). The landing page draws its connect buttons from it before the login. This is the only public Git field; the allowlist test of [0021](0021-admin-api-and-public-status.md) lists it.

### Retention (built)

- "Disconnect" revokes at the host (best effort, a failure is logged without the token) and deletes the Secret.
- An account unused for the `inactivityExpiry` of its provider is revoked and deleted. Every service replica sweeps hourly with jitter; the sweep is idempotent.
- A refresh that fails with `invalid_grant` means the user revoked the grant at the host. The service deletes the account on that use, but only if the Secret still holds the refresh token it sent and its own lock (delete with a resourceVersion precondition). Otherwise another replica or a reconnect changed it, and the caller reads the account again.
- A refresh runs under a context detached from the request, with its own deadline: the provider rotates the refresh token on use, so the new one must reach the Secret even when the request ends. The store retries every error and logs loudly if it still fails. It leaves a lock that is not ours alone.
- The sweep deletes with a precondition first and revokes only after the delete succeeded, so an account used meanwhile stays.

### Git proxy and agent (built)

```
ide container --http 127.0.0.1:8082/git/<host>/<repo>/...--> agent --(instance name + instance token)--> git-proxy --(user token)--> https://<host>/...
                                                                                                           |
                                                                                                           +--> service internal listener :8083: authorize
```

- A separate `git-proxy` Deployment (same Go module, `cmd/git-proxy`, own image) is reachable only inside the cluster, through a ClusterIP Service, with no HTTPRoute. Session traffic never passes the service. It holds no state except a cache of authorize answers, so any number of replicas can run.
- The IDE container holds no token and no instance token. Git in the pod follows `url.<agent>/git/<host>/.insteadOf https://<host>/` to the loopback listener of the agent (`127.0.0.1:8082`). The agent forwards `ANY /git/{host}/{rest...}` to the git-proxy Service as `/{host}/{rest}`, removes `Authorization`, `Cookie` and proxy auth headers of the client and adds the instance name (`X-EduIDE-Instance`) and the instance token (`Authorization: Bearer`). It streams in both directions, with no body size limit and no deadline for the whole request. The agent stays the only data path of the pod ([0006](0006-agent-sidecar.md)).
- Every Git request to a provider host goes through the proxy. Without a connected account it passes anonymously, so public repositories behave as before and connecting works in a running session.
- The agent serves `/git/` only while the instance is claimed (an idle warm instance answers 403) and only when the operator set `AGENT_GIT_PROXY_URL`, `AGENT_GIT_HOSTS` and `AGENT_GIT_LANDING_URL` from the config (`gitAccounts.providers`, `gitAccounts.proxyURL`, `hosts.service`). Without them `/git/` answers 404. Idle pods that were built before a config change keep their old values until the pool replaces them.
- `GET /v1/git/hosts` on the local listener returns the provider hosts and the landing page URL for the extension; the list is empty when the feature is off.
- The git-proxy listens on `:8080` (Git) and `:9090` (`/metrics`, `/healthz`, `/readyz`). It accepts only `GET /{host}/{repo...}/info/refs?service=git-upload-pack|git-receive-pack`, `POST .../git-upload-pack` and `POST .../git-receive-pack` for provider hosts. Git LFS paths (`/info/lfs/`) answer 501 with the JSON message "Git LFS is not supported through EduIDE.". Everything else is 404. Requests without instance name and bearer token get 401 and no upstream request.
- It forwards over HTTPS with default certificate verification, to provider hosts only. It never follows a redirect: a 3xx of the provider becomes an error message. It passes `Git-Protocol`, `Content-Type`, `Content-Encoding`, `Accept-Encoding` and the status, and drops hop-by-hop headers, `Set-Cookie`, `WWW-Authenticate` and `Location`, so the Git client in the pod is never asked for credentials.
- Errors reach the user as a Git message. A denial, a failure of the authorize call and an upstream 401, 403 or 404 become a valid smart HTTP answer with an `ERR` pkt-line, so `git` prints `fatal: remote error: <message>`. This works for protocol v0 and v2 (checked with a real `git` in `internal/gitproxy`).
- Logging: one line per push and per denied request (salted user id, session, provider, repository, kind, result, reason). Reads that succeed are only counted. Never tokens or bodies. Metrics are in [metrics.md](../contracts/metrics.md).

#### The credential rule

The user's token is used only for the repository the session was started with, and for repositories the user approved for this session (next section). The reason: a crafted start link to an attacker's repository runs the attacker's code in the victim's session (for example a Gradle import in the language server). If the session could use the token for every repository, that code could clone all private repositories of the victim.

| Request | Answer |
|---|---|
| `read` or `push` on the session repository (the `GIT_URI` of the launch data, compared without credentials, host case-insensitive, without a trailing `.git` or slash) | Allowed with the credential of the account. Without an account, `read` is anonymous and `push` is denied with reason `connect`. |
| `read` on an approved repository (access `read` or `write`) | Allowed with the credential (reason `ok`). |
| `push` on an approved repository with access `write` | Allowed with the credential. |
| `read` on another repository of a provider host | Allowed, anonymous, never with the credential (reason `anonymous-other`). Public repositories keep working. |
| `push` on another repository (or an approved one with access `read`), or in a session without a provider repository | Denied, reason `push-rule`. |

One function (`mayUseCredential` in `internal/gitpolicy`) answers "may this session use the credential for this repository for this kind of request". An approval counts only as far as the mode allows: lowering the mode takes effect on approvals that exist.
When an anonymous read of another repository fails at the provider (401, 403, 404), the git-proxy says "Connect your <displayName> account to open this repository: <landing page URL>" if the user has no account. With an account it says "EduIDE uses your <displayName> account only for the repository this session was started with (<session repository or none>). Start a session from <host>/<repo> to open it." when approval is off, and the approval message below when it is on.

#### Repository approval

A student who needs a second private repository, a fork to push to, or a push target in a session without a repository approves it **outside the pod**. The proxy refuses and names a link. The student opens it on the landing page, sees session and repository and clicks, signed in with the EduIDE login. Code in the pod cannot do this: it has no bearer token, and the internal authorize endpoint only decides, it never approves. The service never approves on a GET.

- Config `gitAccounts.approval.mode`: `off` (no approvals, the rule as before), `read` (approve for reading only) or `write` (for reading, or reading and pushing); default `write`. `maxPerSession` (default 20) bounds the approvals of one session.
- API (bearer, session owner only, the session must exist and not be deleting, the host must be a provider, the caller needs an account on it, the mode must allow the access): `GET /sessions/{name}/git/repositories` returns mode, limit, the session repository and the approvals. `PUT` (body `{repository: "https://<host>/<path>", access: "read"|"write"}`) approves, is idempotent and can raise or lower the access. `DELETE` (body `{repository}`) removes. The repository URL follows the rules of `GIT_URI` (https, no credentials, no query, fragment or port). Problem codes: `git-approval-disabled`, `git-access-not-allowed`, `git-approval-limit`, `git-not-a-provider`, `git-not-connected`, `invalid-input`, `not-found`.
- Link in the message of the git-proxy: `https://<hosts.service>/?session=<session name>&approveRepo=<url-encoded https URL of the repository>&access=<read|write>`. `access` is `read` for a refused read and `write` for a refused push. The link holds no secret. The message reads "This session may not use <host>/<repo> yet. Approve it here: <link>" (for a push "may not push to"). With mode `off`, with a push in mode `read`, or without a connected account, the old message without a link stays.
- Storage: one Secret per session, `<session>-git-repos`, owned by the session (it is deleted with it), with the label `eduide.github.io/component=git-approvals` and no `managed-by` label. [0012](0012-user-data-secrets-allowlist.md) puts user data into Secrets owned by the session, and the service Role already has the Secret verbs the Git accounts need, so no new right is added. An annotation or a field on the `IDESession` would put user data into the CR (not allowed by 0012) and would need `patch` on sessions. The Secret holds only repository, access and time, no credential. Writes are optimistic (resourceVersion); a conflict re-reads and retries, so several replicas lose no approval.
- Logging: one audit line per approval and per removal (salted user id, session, provider, repository, access). Nothing else logs repositories of approvals.
- The authorize answer carries `approvalMode` (only when the user has an account on the host and approving could help) and the session name, so the git-proxy builds the message. The service builds the message of a refused push.

#### Internal contract: authorize

Defined in [internal/gitapi](../../internal/gitapi/gitapi.go), shared by service and git-proxy. It is not part of `api/openapi/v1.yaml`. The service serves it on its own listener `:8083`, only when providers are configured. The gateway never routes to it, and a NetworkPolicy lets only the git-proxy reach it.

`POST /internal/v1/git/authorize` with the instance token as `Authorization: Bearer`.

```json
{"instance": "<instance id>", "host": "github.com", "repository": "owner/repo", "kind": "read"}
```

`kind` is `read` or `push`. The answer is `200` with:

```json
{"allow": true, "reason": "ok", "message": "", "user": "<salted id>", "session": "<name>",
 "connected": true, "sessionRepository": "github.com/owner/repo",
 "upstream": {"username": "x-access-token", "password": "<token>"}}
```

- `approvalMode` is `read` or `write` when an approval could help (see above). `reason` is one of `ok`, `anonymous`, `anonymous-other`, `connect`, `push-rule`, `not-ready`, `unknown-host`. The git-proxy adds `unauthorized` itself when the service answers 401.
- `upstream` is present only with `ok`: basic auth `x-access-token:<token>` for GitHub, `oauth2:<token>` for GitLab. The git-proxy never logs it and never hands it to a client.
- `message` carries the text for a denial that only the service can build (`connect`, `push-rule`, `not-ready`, `unknown-host`). `connected` and `sessionRepository` let the git-proxy build the message after an anonymous failure.
- `401` for a missing or wrong token or an unknown instance, `400` for a malformed request, `429` over the limit of 10 calls per second (burst 30) per instance, `500` when the Kubernetes API fails.
- The service compares the token in constant time with the token Secret of the instance, finds the `IDESession` whose `status.instance` is the instance, requires phase `Ready` and no deletion timestamp, and takes the owner from the session. An account is used only through `TokenForHost`, which counts as a use (written at most once per 24 hours).
- The git-proxy caches an answer with a credential for at most 5 seconds per (instance, token hash, host, repository, kind). Denials and anonymous answers are cached for 3 seconds. So a user who connects an account, approves a repository or removes an approval waits 5 seconds at most, and a disconnected account or a removed approval works in a running session for at most 5 seconds. The cost is one authorize call per repository and 5 seconds.

#### Network

- Session pods may reach the `git-proxy` Service. This is a NetworkPolicy of its own with a pod selector. A NetworkPolicy only adds allowed paths and an `except` list of an ipBlock narrows that rule alone, so the rule holds on clusters that deny the pod CIDR in `sessionEgressDeny` or `sessionEgressExtraDeny`.
- `git-proxy` accepts ingress only from session pods and from the monitoring namespace (metrics). Its egress is DNS, the internal port of the service and HTTPS to the internet without the private and link-local ranges (the deny list of session pods).
- The internal port of the service accepts only `git-proxy` ([0019](0019-network-isolation.md)).
- The git-proxy pod has no service account token and mounts no Secret.

### Landing page and extension (Planned)

- The landing page shows the provider buttons (from `gitProviders` in `GET /status`), connects and disconnects, handles the redirect route `/git/callback`, calls the preflight before a launch, offers a template picker for a link that has `gitUri` but a missing or unknown `appDef`, and shows the approval page for the link above. `GET /templates` already carries what the picker needs: the templates with `listed` and `injectableKeys` containing `GIT_URI`. Nothing in the API changes for it.
- `eduide-session` reads `GET /v1/git/hosts` and writes the `insteadOf` entries before it hands the launch data to the IDE. The e2e fake IDE does the same.

### Tests

Unit tests cover each part. The kind e2e (`TestGitAccounts`, [testing guide](../development/testing.md)) deploys a fake Git provider (OAuth application endpoints compatible with the GitLab client with PKCE check and refresh rotation, and Git smart HTTP for private and public repositories) and plays the landing page: connect, authorize, callback, preflight, a callback of another user, clone, commit and push of the session repository, a second private repository (refused, then approved), push elsewhere, public clone, disconnect, an idle instance, NetworkPolicies, no token in the IDE container, in logs or in the launch data Secret, and the token refresh. The existing suite runs without providers and stays unchanged.

### Threat model

| Threat | Control |
|---|---|
| Code in the pod steals the Git token | The token never enters the pod. The IDE container has no credential for the proxy either. |
| Code in the pod pushes to other repositories of the student | Push rule: only the session repository. Residual risk: it can push to the session repository and read every repository the student can read while the session runs. |
| Stolen token store (etcd backup, `kubectl get secret`) | Values are encrypted with a key in a separate Secret. The inactivity expiry keeps the set small. |
| Compromised `git-proxy` | It sees access tokens in transit. GitLab tokens live two hours. GitHub OAuth App tokens do not expire: this is the accepted cost of a user bound token, limited by NetworkPolicy, no ingress and the revoke on disconnect. |
| Login CSRF in the connect flow | Sealed state naming the user who started the flow, required to equal the bearer user of the callback. No cookie, so a sibling subdomain cannot plant one. PKCE. |
| A student clicks through an approval request they did not make | The approval page (landing page) names session and repository and needs an explicit click with the EduIDE login. Approvals are per session, end with it, are limited in number and audited. The mode can limit them to reading or turn them off. |
| Code in the pod approves a repository for itself | Approval needs the bearer token of the user. The pod has none, and the internal authorize endpoint never approves. |
| Code in the pod reads the user's other private repositories | The credential is used for the session repository only; other repositories are read anonymously. |
| Session pod calls the internal port of the service | Separate listener, NetworkPolicy allows only `git-proxy`, instance token required. |
| Proxy as an open relay or for SSRF | Only provider hosts from the config, only three Git paths, HTTPS only, no redirects followed, egress without private ranges. |
| Launch data leak | Tokens are not launch data and are not in CR specs, logs, traces, metrics or status (G18 holds). |

### Consequences

- Good: A student connects once. The token never reaches a session pod. The feature costs nothing while no provider is configured.
- Good: The sealed state and the derived PKCE verifier need no server side storage, so every service replica can end a flow.
- Bad: GitHub OAuth App tokens do not expire, so a stolen token is valid until the user revokes it or the account expires.
- Bad: The service Role can list and update every Secret of its namespace while the feature is on.
- Bad: A change of the commit identity needs a new session.
- Bad: A private repository other than the session repository needs an approval on the landing page (or a session started from it). With mode `off` it cannot be read at all.
- Bad: A state is not single use (see the connect flow).
- Follow-up: The landing page and `eduide-session` parts above.
- Follow-up: A switch per template to turn Git accounts off (exams), Git LFS through the proxy.

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
