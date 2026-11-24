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
  Each entry has `name`, `type` (`github` or `gitlab`), `host`, `displayName`, `clientID`, `clientSecretFile` and `inactivityExpiry` (default 2160 h).
  Names and hosts are unique, the host is a bare host name. Users cannot enter hosts.
- GitHub is an OAuth App with scope `repo`. Its tokens do not expire and have no refresh token. `type: github` with another host covers GitHub Enterprise Server.
- GitLab is an OAuth application per instance with the scopes `read_repository`, `write_repository` and `read_user`. Access tokens expire after two hours, refresh tokens rotate on every use.
- The operator of each installation registers its own applications, owned by an organisation or group, one per installation and provider.
- The client secrets and the encryption key reach the service as files (a projected volume in the service pod only), like the user salt. The config names the paths, never the secrets.
- Provider hosts count as allowed hosts for the key `GIT_URI` only, not for other `HTTPSURL` keys such as `ARTEMIS_URL`: those name servers that receive requests with credentials. `allowedHosts` stays for hosts without login.

### Token store (built)

- One Secret per user and provider in the deployment namespace, named `git-account-<first 32 hex of sha256(owner "\n" provider)>`.
  Labels: `eduide.github.io/component=git-account`, `eduide.github.io/git-provider`, `eduide.github.io/owner-hash`.
- The Secret holds one AES-256-GCM encrypted JSON blob: access token, refresh token, expiry, login, numeric id, display name, no-reply e-mail and connect time.
  The authenticated data is the Secret name, so a blob moved to another Secret does not open.
- The Secrets carry no `app.kubernetes.io/managed-by` label: the operator's Secret cache selects on it, and the operator has no use for these Secrets. The sweep selects them by their component label.
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

The user's token is used only for the repository the session was started with. The reason: a crafted start link to an attacker's repository runs the attacker's code in the victim's session (for example a Gradle import in the language server). If the session could use the token for every repository, that code could clone all private repositories of the victim.

| Request | Answer |
|---|---|
| `read` or `push` on the session repository (the `GIT_URI` of the launch data, compared without credentials, host case-insensitive, without a trailing `.git` or slash) | Allowed with the credential of the account. Without an account, `read` is anonymous and `push` is denied with reason `connect`. |
| `read` on another repository of a provider host | Allowed, anonymous, never with the credential (reason `anonymous-other`). Public repositories keep working. |
| `push` on another repository, or in a session without a provider repository | Denied, reason `push-rule`. |

One function (`mayUseCredential` in `internal/gitpolicy`) answers "may this session use the credential for this repository". A later list of repositories the user approved outside the pod only changes that function.
When an anonymous read of another repository fails at the provider (401, 403, 404), the git-proxy says "Connect your <displayName> account to open this repository: <landing page URL>" if the user has no account, and "EduIDE uses your <displayName> account only for the repository this session was started with (<session repository or none>). Start a session from <host>/<repo> to open it." if the user has one.

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

- `reason` is one of `ok`, `anonymous`, `anonymous-other`, `connect`, `push-rule`, `not-ready`, `unknown-host`. The git-proxy adds `unauthorized` itself when the service answers 401.
- `upstream` is present only with `ok`: basic auth `x-access-token:<token>` for GitHub, `oauth2:<token>` for GitLab. The git-proxy never logs it and never hands it to a client.
- `message` carries the text for a denial that only the service can build (`connect`, `push-rule`, `not-ready`, `unknown-host`). `connected` and `sessionRepository` let the git-proxy build the message after an anonymous failure.
- `401` for a missing or wrong token or an unknown instance, `400` for a malformed request, `429` over the limit of 10 calls per second (burst 30) per instance, `500` when the Kubernetes API fails.
- The service compares the token in constant time with the token Secret of the instance, finds the `IDESession` whose `status.instance` is the instance, requires phase `Ready` and no deletion timestamp, and takes the owner from the session. An account is used only through `TokenForHost`, which counts as a use (written at most once per 24 hours).
- The git-proxy caches an answer with a credential for at most 30 seconds per (instance, token hash, host, repository, kind). Denials and anonymous answers are cached for 3 seconds, so a user who connects an account waits a few seconds at most.

#### Network

- Session pods may reach the `git-proxy` Service. This is a NetworkPolicy of its own with a pod selector. A NetworkPolicy only adds allowed paths and an `except` list of an ipBlock narrows that rule alone, so the rule holds on clusters that deny the pod CIDR in `sessionEgressDeny` or `sessionEgressExtraDeny`.
- `git-proxy` accepts ingress only from session pods and from the monitoring namespace (metrics). Its egress is DNS, the internal port of the service and HTTPS to the internet without the private and link-local ranges (the deny list of session pods).
- The internal port of the service accepts only `git-proxy` ([0019](0019-network-isolation.md)).
- The git-proxy pod has no service account token and mounts no Secret.

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
| Code in the pod reads the user's other private repositories | The credential is used for the session repository only; other repositories are read anonymously. |
| Session pod calls the internal port of the service | Separate listener, NetworkPolicy allows only `git-proxy`, instance token required. |
| Proxy as an open relay or for SSRF | Only provider hosts from the config, only three Git paths, HTTPS only, no redirects followed, egress without private ranges. |
| Launch data leak | Tokens are not launch data and are not in CR specs, logs, traces, metrics or status (G18 holds). |

### Consequences

- Good: A student connects once. The token never reaches a session pod. The feature costs nothing while no provider is configured.
- Good: The state and the flow cookie need no server side storage, so every service replica can end a flow.
- Bad: GitHub OAuth App tokens do not expire, so a stolen token is valid until the user revokes it or the account expires.
- Bad: The service Role can list and update every Secret of its namespace while the feature is on.
- Bad: A change of the commit identity needs a new session.
- Bad: Private repositories other than the session repository cannot be read through the proxy. A student must start a session from the repository to use the account on it.
- Follow-up: Planned parts above (preflight, landing page, `eduide-session`, operator guide).
- Follow-up: A switch per template to turn Git accounts off (exams), approval of further repositories the credential may be used for (forks, other repositories), Git LFS through the proxy.

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
