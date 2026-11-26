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
- The admin of each installation registers its own applications, owned by an organisation or group, one per installation and provider. **The redirect URI is exactly `https://<hosts.service>/git/callback`.** It is a route of the landing page, not of the API (see the connect flow).
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
- "Switch account" on the gate connects first. Saving the new account replaces and revokes the old one, so a failed or cancelled connect keeps the working account. The page tells the user to sign out at the host to really switch (the host remembers its login and would offer the same account again).
- Connect, callback, preflight and the approval endpoints are rate limited per user (the launch rate, separate buckets).
- "Disconnect" and the sweep delete with the resourceVersion that was read and revoke only after the delete succeeded. A reconnect or a refresh in between fails the delete, and the disconnect reads again, so a new account is never deleted unrevoked.
- A reconnect replaces the record, keeps no lock annotation (it fails with `rate-limited` while a refresh runs) and revokes the replaced token after the update succeeded. GitHub tokens are revoked one by one (`DELETE /applications/{client_id}/token`), not by grant, so tokens of the user's other devices stay.
- The service stores the no-reply commit address of the account: GitHub `<id>+<login>@users.noreply.github.com` for github.com, GitLab `<id>-<login>@users.noreply.<host>`. For a GitHub Enterprise Server host the format is not known, so the service stores none and the launch falls back to the identity provider values.

#### Preflight

`POST /git/preflight` with `{gitUri}` answers `{result, provider?}`. It sends one `GET <repo>/info/refs?service=git-upload-pack` to the provider host with a 3 second timeout, with the user's credential if an account exists, never follows a redirect, and discards the body. Only configured provider hosts on port 443 are asked, over HTTPS, with the URL rules of the launch key `GIT_URI` (no credentials) and the repository path rule above. The request URL is built from the configured host and the canonical path, never from the request URL, so the user's token goes to no other path shape than a Git repository. The URL and the credential are never logged.

| Result | Meaning |
|---|---|
| `ok` | Reachable, with the credential or anonymously |
| `connect` | Provider host, no account (or the token no longer works) and not readable anonymously |
| `no-access` | Account connected, the host says 403 or 404 |
| `not-a-provider` | The host is no provider, nothing was checked |
| `unknown` | Timeout, 5xx, redirect or any other answer: the launch must not be blocked |

#### Commit identity at launch

For a launch with a `GIT_URI` on a provider host where the caller has an account with a stored no-reply address, the service sets `GIT_USER` to the display name (the login when empty) and `GIT_MAIL` to the no-reply address, but only for keys the template takes from the token (`source: UserName` or `UserEmail`). Any other launch keeps its data. The provider's values are not trusted: the name loses control characters, line and paragraph separators and angle brackets, collapses blanks and is cut at 100 characters (or the `maxLength` of the key when smaller); a name that is empty after that falls back to the login, then to the identity provider's value. The no-reply address must be one plain address without blanks or control characters. Scorpio writes both values with `git config --global --add user.name` and `user.email` as separate arguments (no shell), so a newline could not inject another setting, but the commit author must still be clean. The launch key hashes the data as sent by the user, so a changed display name does not start a second session.

#### Public status

`GET /status` adds `gitProviders: [{name, type, host, displayName}]` when providers are configured (absent otherwise). It serves clients without a login (the landing page reads `GET /git/providers` after its login, because it needs the connection state). This is the only public Git field; the allowlist test of [0021](0021-admin-api-and-public-status.md) lists it.

### Retention (built)

- "Disconnect" deletes the Secret and then revokes at the host (best effort, a failure is logged without the token).
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
- The IDE container holds no token and no instance token. Git in the pod follows `url.<agent>/git/<host>/.insteadOf https://<host>/` to the loopback listener of the agent (`127.0.0.1:8082`). The agent forwards `ANY /git/{host}/{rest...}` to the git-proxy Service as `/{host}/{rest}`, removes `Authorization`, `Cookie` and proxy auth headers of the client and adds the instance name (`X-EduIDE-Instance`) and the instance token (`Authorization: Bearer`). It streams in both directions, with no body size limit and no deadline for the whole request (the git-proxy cuts only a request that moves no byte for 5 minutes, see below). The agent stays the only data path of the pod ([0006](0006-agent-sidecar.md)).
- Every Git request to a provider host goes through the proxy. Without a connected account it passes anonymously, so public repositories behave as before and connecting works in a running session.
- The agent serves `/git/` only while the instance is claimed (an idle instance answers 403) and only when the operator set `AGENT_GIT_PROXY_URL`, `AGENT_GIT_HOSTS` and `AGENT_GIT_LANDING_URL` from the config (`gitAccounts.providers`, `gitAccounts.proxyURL`, `hosts.service`). Without them `/git/` answers 404. `specHash` of the pod includes these values, so the pool replaces idle pods after a change of the Git settings. A deployment without providers keeps the hash it had before the feature, so an upgrade replaces no idle pod.
- `GET /v1/git/hosts` on the local listener returns the provider hosts and the landing page URL for the extension; the list is empty when the feature is off.
- The git-proxy listens on `:8080` (Git) and `:9090` (`/metrics`, `/healthz`, `/readyz`). It accepts only `GET /{host}/{repo...}/info/refs?service=git-upload-pack|git-receive-pack`, `POST .../git-upload-pack` and `POST .../git-receive-pack` for provider hosts. Git LFS paths (`/info/lfs/`) answer 501 with the JSON message "Git LFS is not supported through EduIDE.". Everything else is 404. Requests without instance name and bearer token get 401 and no upstream request.
- A repository path passes one rule (`gitapi.CanonicalRepo`, shared by the git-proxy, the approval API and the preflight): 2 to 10 segments of letters, digits and `._~-`, no segment `.`, `..` or `.git` (any case; so no `.git` directory segment), `.git` only as the suffix of the last segment, and no name that still ends in `.git` after one suffix is cut. A leading dot alone is a valid name (`org/.github`, `user/.dotfiles`). The result has no `.git` suffix and is idempotent, so nothing normalises it twice. Authorize, approvals and the push rule use this form. The upstream path is exactly `/<canonical repository>.git` plus the fixed suffix (`/info/refs`, `/git-upload-pack` or `/git-receive-pack`), built from these parts and never from the request path. The `.git` is not optional: GitLab answers `<repo>/info/refs` with a 301 and `<repo>/git-upload-pack` with a 422, while GitHub serves both forms (measured against public repositories of both hosts), and the proxy never follows a redirect. The preflight probes the same path. The authorize call carries the same repository, so the repository that is authorised is the repository that is fetched.
- Order of checks in the git-proxy, part of the threat model: client rate limit, path and headers, authorize (the token check comes before any work), the per-instance slot, and only then the peek of a push body (a 4 KiB buffer, 2 seconds). A pod that sends a push with a made-up token gets an answer at once and costs no buffer and no wait; the body is never read. Only an authorised request holds memory and a connection for the peek.
- Limits against a flood. The IDE container can reach the git-proxy directly with the instance name and token it does not have, but a pod can send anything. The git-proxy retries an authorize call once after a 429 of the service (200 ms plus jitter up to 200 ms), so a burst over many repositories (40 Git dependencies, a submodule update over 30 repositories) does not fail on the limit of one instance. It allows 100 requests per second per client IP (burst 400, far above a clone, a push or a submodule update; the map holds 4096 clients and drops buckets that refilled), answers 429 above that, runs at most 16 requests per instance at the same time (an ERR line says so) and cancels a request that moves no byte in either direction for 5 minutes. The deadline counts idle time, not total time: a clone over a slow link that keeps moving finishes (checked by the streaming test with a throttled provider). One phase is not idle time: after the request body ended and before the first byte of the answer, the provider works and sends nothing (it unpacks a push, it builds a pack). That phase has its own limit of 30 minutes, which only frees the slot of a provider that never answers. A stalled upload, a stalled download and a provider that stays silent for 30 minutes are cut; a slow transfer that keeps moving and a provider that takes a long time to answer are not. An ERR line carries at most 1000 bytes of message (a pkt-line length has four hex digits). The client IP is the source address the pod network preserves for a ClusterIP Service; a network that rewrites it to a node address makes all pods of a node share one bucket, which the high limit is sized for.
- It forwards over HTTPS with default certificate verification, to provider hosts only. It never follows a redirect: a 3xx of the provider becomes an error message. It passes `Git-Protocol`, `Content-Type`, `Content-Encoding`, `Accept-Encoding` and the status, and drops hop-by-hop headers, `Set-Cookie`, `WWW-Authenticate` and `Location`, so the Git client in the pod is never asked for credentials.
- Errors reach the user as a Git message. A denial, a failure of the authorize call and an upstream 401, 403 or 404 become a valid smart HTTP answer with an `ERR` pkt-line, so `git` prints `fatal: remote error: <message>`. This works for protocol v0 and v2 (checked with a real `git` in `internal/gitproxy`). An error on a push (`POST git-receive-pack`) takes the form the client can read: git reads the result of a push through the side-band channels, where a plain `ERR` line ends in `bad band`. The git-proxy reads the first command of the push, which holds the capabilities, without consuming it. It skips the leading `shallow <oid>` lines that a push from a shallow clone sends first. With `side-band` or `side-band-64k` the text goes out on channel 3, which git prints as the remote error. Without side-band the answer has a non-200 status, and git shows only the status (and, misleadingly, "Everything up-to-date"). A first command that does not fit in the 4 KiB buffer (many shallow lines) takes the status form. The probe of a large push carries no command (`0000`) and ignores the answer, so it keeps the `ERR` line. The peek runs only after the request is authorised and holds a slot of its instance (see "Order of checks" below), so a denial that happens before it does not know the client and answers with an `ERR` line. Measured with git 2.54 on a push with side-band: the `ERR` line prints `bad band #69` and fails the push cleanly, while a non-200 status prints "RPC failed", "unexpected disconnect" and "Everything up-to-date". Neither shows the message, so the `ERR` line is the choice. A push is normally denied at `info/refs`, where git shows the message.
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
- `401` for a missing or wrong token or an unknown instance, `400` for a malformed request, `429` over the limit of 20 calls per second (burst 100) per instance, or when too many instances without a session ask for a token lookup (20 per second, burst 100), `500` when the Kubernetes API fails.
- The token is checked before the limiter is charged, so only a caller with the instance token draws on the budget of its instance: a wrong token for a known instance name (the name is in the IDE URL) cannot empty the bucket of that session. The service caches the SHA-256 of the token Secret of an instance for 30 seconds (5 seconds for a missing Secret), so wrong tokens cost no API call, and instances that have no session draw on a shared budget for lookups that real instances do not use. The limiter map, the token cache and the git-proxy cache are bounded.
- The service compares the token in constant time with the token Secret of the instance, finds the `IDESession` whose `status.instance` is the instance, requires phase `Ready` and no deletion timestamp, and takes the owner from the session. An account is used only through `TokenForHost`, which counts as a use (written at most once per 24 hours).
- The git-proxy caches an answer with a credential for at most 5 seconds per (instance, token hash, host, repository, kind). Denials and anonymous answers are cached for 3 seconds. So a user who connects an account, approves a repository or removes an approval waits 5 seconds at most, and a disconnected account or a removed approval works in a running session for at most 5 seconds. The cost is one authorize call per repository and 5 seconds.

#### Network

- Session pods may reach the `git-proxy` Service. This is a NetworkPolicy of its own with a pod selector. A NetworkPolicy only adds allowed paths and an `except` list of an ipBlock narrows that rule alone, so the rule holds on clusters that deny the pod CIDR in `sessionEgressDeny` or `sessionEgressExtraDeny`.
- `git-proxy` accepts ingress only from session pods and from the monitoring namespace (metrics). Its egress is DNS, the internal port of the service and HTTPS to the internet without the private and link-local ranges (the deny list of session pods).
- The internal port of the service accepts only `git-proxy` ([0019](0019-network-isolation.md)).
- The git-proxy pod has no service account token and mounts no Secret.

### Landing page and extension (built)

The landing page (`landing-page/`, code in `src/git.ts` and `src/components/Git*.tsx`):

- **Accounts.** The home page shows a "Git accounts" section when `GET /git/providers` lists providers: one row per provider with "Connect", or the account login with "Disconnect" (after a confirmation). Short texts say what EduIDE does with the account: it uses it only for the repository the session was started with and for repositories the student approves, the token stays on the server and never enters the IDE, pushes and refused requests are logged, and the account is removed on disconnect or after the inactivity period of the provider. The page needs the connection state, so it reads `GET /git/providers` after the login. `gitProviders` in the public `GET /status` stays for clients without a login.
- **Connect.** "Connect" calls `POST /git/providers/{name}/connect` with the current page as `returnTo` (a pending start link included) and navigates to `authorizeUrl`. The route `/git/callback` reads `code` and `state` (or `error` and `state`) before Keycloak starts (`captureGitCallbackFromWindow`, called after `captureFromWindow` in `main.tsx`), keeps them in `sessionStorage` like the Artemis token, and replaces the address with `/git/callback` at once. After the login the page takes them out of the storage (used once), posts them to `POST /git/callback` and goes to `returnTo`. Each problem code has its own message. The values are never logged and go to this one endpoint only. The page validates `returnTo` again before it navigates (one leading `/`, no `//` or `/\`, no control characters).
- **Preflight.** A launch with a `GIT_URI` without credentials first calls `POST /git/preflight`. `ok`, `not-a-provider` and `unknown` launch as before. `connect` shows "This repository needs your <name> account" (it may also not exist) with "Connect"; the connect returns to the same link, so the launch continues without the user opening the link again. "Start anyway" is not offered, because the clone would fail. `no-access` names the connected account and offers "Connect another account" or "Back" (the home page has the tiles to start without a repository). A failing request (network, 404 of an older service, rate limit) never blocks: the launch goes ahead. A URL with credentials (Artemis) is not sent to the preflight.
- **Picker.** A link with `gitUri` and a missing or unknown `appDef` shows the repository and the listed templates whose `injectableKeys` contain `GIT_URI`. The pick launches with the repository. The picker never forwards `artemisUrl` or the Artemis token: it is not an Artemis launch. A link without `gitUri` and without `appDef` does nothing, as before, and a launch never starts before the login.
- **Approval page.** The link of the git-proxy message opens the home route with `session`, `approveRepo` and `access`. The page shows the session (title or template, and when it started), the repository, the access in words ("Read" or "Read and push"), a sentence that the student should allow only a request they just made themselves, the repository the session was started with next to the title (a start link can set the title), and the buttons "Allow" and "Decline". Only "Allow" calls `PUT /sessions/{name}/git/repositories`. The parameters are attacker controlled: the session name must be a DNS label, `access` must be `read` or `write`, the repository must be an https URL without credentials, query, fragment, port or percent escape, with a path that passes the repository rule above (shown the way the service compares it: host in lower case, no `.git`, no trailing slash, look-alike hosts in punycode; a `%` would show another path than the service approves), everything renders as text, and an invalid link shows an error and makes no call. The problem codes have messages; `git-not-connected` offers "Connect". The page lists the approved repositories of the session with "Remove". An approval link never starts a launch, whatever else it carries.

`eduide-session` reads `GET /v1/git/hosts` and writes the `insteadOf` entries before it hands the launch data to the IDE (`extensions/eduide-session/src/git.ts`). The e2e fake IDE does the same. The extension reaches a real IDE only through the EduIDE image: it needs a new version of the extension in the image build.

#### Operation notes

- **A provider leaves the config.** The sweep skips accounts of providers that are not configured, so their Secrets stay (and the access tokens stay valid at the host, because the service has no client credentials to revoke them). The sweep does not delete them on purpose: during a rollout that adds a provider, an old replica does not know it yet and would delete the accounts the new replicas just created. Delete them by hand, and remove the OAuth application at the host (this revokes the tokens): `kubectl -n <ns> delete secret -l eduide.github.io/component=git-account,eduide.github.io/git-provider=<name>`. Without any provider the service builds no Git manager, so nothing sweeps.
- **The Artemis host is also a provider.** This works. The `GIT_URI` of an Artemis launch carries the VCS token as user and password. Git rewrites only URLs that start with `https://<host>/`, so the clone of a URL with credentials does not match the `insteadOf` entry and goes straight to the host with the VCS token, as before the feature (checked with `git`). The rule for the session repository compares the URL without credentials, so requests through the proxy to that repository use the connected account of the student, or are anonymous without one. A client that strips the credentials before it clones goes through the proxy, and then the student needs a connected account for a private repository. No configuration is refused. Not verified: what Scorpio does with the credentials in a real IDE.
- **A provider on a private address.** The service makes the OAuth, account and preflight calls itself. The chart gives it no egress NetworkPolicy (the policy `service` selects Ingress only), so it reaches a provider on a private address as far as the cluster network allows. `networkPolicy.gitProxyEgressExtra` covers only git-proxy, whose default egress excludes private ranges.
- **A rotated client secret.** The service reads the client secret files and the encryption key once at start. A changed Secret needs a restart of the service Deployment (`kubectl -n <ns> rollout restart deploy/<service>`). The chart does not restart it on a change of a Secret it does not own. A wrong secret fails the connect and refresh calls with `git-provider-error`. The git-proxy holds no client secret.

### Tests

Unit tests cover each part (the limiter bounds, the token-first order, the expiry of the token cache with an injected clock, a burst of 40 distinct repositories, the path rule, the upstream `.git` path, the idle and processing limits, the retry of an authorize 429, the form of a push error, the identity sanitiser, the disconnect order). The integration test in `internal/gitproxy` runs a real `git` against a provider that behaves like GitLab (a path without `.git` is a 301 or a 422): clones with and without `.git`, a name with a leading dot, and pushes that fail with the message on screen. The kind e2e (`TestGitAccounts`, [testing guide](../development/testing.md)) deploys a fake Git provider (OAuth application endpoints compatible with the GitLab client with PKCE check and refresh rotation, and Git smart HTTP for private and public repositories) and plays the landing page: connect, authorize, callback, preflight, a callback of another user, clone, commit and push of the session repository, a second private repository (refused, then approved), a PUT on the Git path of the agent (refused), and the approval call without a token or with a made-up token from outside, with the real session name (401; the pod's own call to the service is not asserted, because session pods cannot reach private ranges in the e2e and a failed connection would pass for any reason), the login cookies of the gateway (HttpOnly, never at the IDE), push elsewhere, public clone, disconnect, an idle instance, NetworkPolicies, no token in the IDE container, in logs or in the launch data Secret, and the token refresh. The Playwright spec `test/e2e/browser/git-accounts.spec.ts` runs in the same phase and drives the landing page: connect and disconnect, a start link to the private repository that asks to connect and then launches, and the approval page. The existing suite runs without providers and stays unchanged.

### Threat model

| Threat | Control |
|---|---|
| Code in the pod steals the Git token | The token never enters the pod. The IDE container has no credential for the proxy either. |
| Code in the pod pushes to other repositories of the student | Push rule: only the session repository. Residual risk: while the session runs, code in the pod can read and push the session repository and read and (with access `write`) push the repositories the student approved for it. It can read public repositories anonymously, with no credential. |
| Stolen token store (etcd backup, `kubectl get secret`) | Values are encrypted with a key in a separate Secret. The inactivity expiry keeps the set small. |
| Compromised `git-proxy` | It sees access tokens in transit. GitLab tokens live two hours. GitHub OAuth App tokens do not expire: this is the accepted cost of a user bound token, limited by NetworkPolicy, no ingress and the revoke on disconnect. |
| Login CSRF in the connect flow | Sealed state naming the user who started the flow, required to equal the bearer user of the callback. No cookie, so a sibling subdomain cannot plant one. PKCE. |
| A student clicks through an approval request they did not make | The approval page (landing page) names session and repository and needs an explicit click with the EduIDE login. Approvals are per session, end with it, are limited in number and audited. The mode can limit them to reading or turn them off. |
| Code in the pod approves a repository for itself | Approval needs the bearer token of the user. The pod has none, and the internal authorize endpoint never approves. See the next section for what keeps it that way. |
| A flood against git-proxy or the authorize endpoint | Token check before the limiter, cached token hashes, bounded maps, per-IP and per-instance limits in the git-proxy, idle deadline. |
| A crafted repository path (`.git` segment, `..`, encoded dots, double suffix) gets the credential for another path | One path rule for all components, the upstream path built from the authorised repository. |
| A provider display name with control characters reaches the Git config of the pod | Sanitised before it becomes `GIT_USER`. |
| Code in the pod reads the user's other private repositories | The credential is used for the session repository only; other repositories are read anonymously. |
| Session pod calls the internal port of the service | Separate listener, NetworkPolicy allows only `git-proxy`, instance token required. |
| Proxy as an open relay or for SSRF | Only provider hosts from the config, only three Git paths, HTTPS only, no redirects followed, egress without private ranges. |
| Launch data leak | Tokens are not launch data and are not in CR specs, logs, traces, metrics or status (G18 holds). |

#### What keeps a bearer token away from the pod and the IDE origin

The approval and every other call of the API accept a bearer token that Keycloak issued to one of the clients in `oidc.clientIDs` (`azp`, or `aud` when there is no `azp`). Code in the pod and script in the IDE's browser origin (the instance host and the webview host) must not be able to get such a token. Verified from the sources:

1. **The pod holds no token.** The pod has no service account token and receives no credential. The agent removes `Authorization`, `Cookie` and the instance token header from requests it forwards to the Git path, and removes the gateway's login cookies (`eduide-id` or the configured `oidc.idTokenCookie`, `eduide-authz`, `IdToken`, `AccessToken`, `BearerToken`, `RefreshToken`, `OauthHMAC`, `OauthExpires`, `OauthNonce`, `CodeVerifier`) from every request to the IDE (`internal/agent/proxy.go`, `stripAuthCookies`), and drops cookies of those names that the IDE tries to set (`scopeCookies`). The Envoy `oidc` filter does not forward the access token as a header (`forwardAccessToken` is not set in the chart). The e2e test `TestGatewayAuth` checks that the IDE receives no login cookie and no `Authorization` header.
2. **Script cannot read the ID token cookie.** The cookie is set by the oauth2 filter of Envoy (chart `SecurityPolicy`, `cookieNames.idToken`, `cookieDomain` the instance host, `disableTokenEncryption: true`), and the chart sets no cookie attributes, so the attributes are Envoy's own (`Secure; HttpOnly`). The e2e test `TestGatewayAuth` reads the `Set-Cookie` lines of a real login and fails if a cookie of the instance host lacks `HttpOnly` or `Secure`, so an Envoy Gateway upgrade that changes the default is caught.
3. **Script cannot run its own login for a client the API accepts.** An authorization code flow that script starts in the IDE origin (a popup or iframe to the Keycloak authorize URL with a redirect URI on the instance or webview host) ends at a URL on its own origin, so it can read the code and exchange it at the token endpoint (web origins `+` allow the CORS call). That yields a token for the client of the flow. The attack works exactly when the client is **public** (no secret to present at the token endpoint) and one of its redirect URIs is on the instance or webview host. Two separate clients close it: the **gateway client is confidential**, so a code exchange needs its secret, which only the gateway holds (a Secret in the namespace that no session pod mounts), and the **landing page client** has redirect URIs on the service host only, whose origin the IDE script cannot read. The API accepts the landing page client only. The ID token of the gateway client carries `azp` = gateway client, so the API rejects it although it is a valid token of the realm (`internal/auth`: a token whose `azp` is not in `oidc.clientIDs` is never accepted, even if `aud` lists one).

**Requirement.** The installation guide ([install.md](../operations/install.md)) requires: two clients; the gateway client confidential, standard flow only, one exact redirect URI, the callback of the instance host (the webview host needs none), no wildcard; the landing page client public with PKCE `S256` required, redirect URIs on the service host only, none on the instance or webview host; no direct access grants, implicit flow or service account on either client; web origins of each client only its own hosts; and `oidc.gatewayClientID` never listed in `oidc.clientIDs`.

**Open risk: one client for both.** A deployment that lists the same client id in `oidc.clientIDs` and `oidc.gatewayClientID` breaks points 2 and 3: the ID token cookie then is a valid API bearer, and a client that the landing page uses with PKCE must be public, so script in the IDE origin can run the flow above (while the user has a Keycloak session, which is the normal case) and call the API, including the approval, as the user. HttpOnly on the cookie does not help against that. Config validation does not reject the setting, because real deployments use it. The fix is a NEW, confidential client for the gateway; the shared client is never switched to confidential, because the landing page needs it public and its logins fail otherwise (change `oidc.gatewayClientID`, the client secret Secret and the Keycloak client; the landing page and `oidc.clientIDs` stay). Until then, the approval, the credential rule and every other API call are only as strong as the same-origin policy between the IDE origin and Keycloak, which does not hold for such a client. This is a property of the existing session access design ([session-access.md](../architecture/session-access.md)), not of the Git accounts: a token obtained that way also launches and deletes sessions.

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
- Bad, because the user or the instructor must create a key per repository, which defeats "connect once".
