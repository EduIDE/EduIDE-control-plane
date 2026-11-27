# Runbook: git-proxy

Symptom: `git clone` or `git push` in a session fails, hangs or prints `fatal: remote error: ...`.
Design: [ADR 0023](../adr/0023-git-accounts-and-git-proxy.md).

## What it does

With `config.gitAccounts.providers` the chart renders two `git-proxy` pods and a ClusterIP Service.
The IDE container reaches a provider host through the agent (`127.0.0.1:8082/git/<host>/...`), which adds the instance name and the instance token.
The git-proxy asks the service (`:8083`, `POST /internal/v1/git/authorize`) what is allowed, then streams the request to `https://<host>/...`.
The user's token is used for the session repository (its `GIT_URI`) and for repositories the user approved for the session.
Other repositories are read anonymously, and a push to them is refused.
Nothing of a clone or push is buffered, so a transfer of any size uses little memory.
The proxy reads at most 4 KiB of a push body (2 seconds at most) to find out how git wants to read an error, and only for a request that the service authorised and that holds a slot of its instance.
A request that is not authorised, or finds no free slot, reads no body and allocates no peek buffer.
A request with a body (a POST) never reuses its connection: the answer carries `Connection: close`, and the git-proxy expires the read deadline of the connection when it returns, so a client that stalls its body is cut off (`net/http` would otherwise discard the unread body with no deadline; `TestStalledBodyHoldsNothingOnAnyPath`, with a real server).
The cost is one new in-cluster TCP connection per POST; a request without a body (the `info/refs` GET) keeps its connection.
A provider that answers before the upload ended, with a client that stalls, is cut by the idle limit of 5 minutes plus 5 seconds of grace.
Until then it holds one of the 16 slots of its own session, so only that session waits.

### Memory

A request in flight costs memory until it ends, however little it moves. `TestMemoryOfStalledPushes` holds 320 authorised pushes (20 instances at their cap of 16) at a provider that never answers and reports heap plus stack of the process per request:

| Connection to the provider | Per request |
|---|---|
| HTTP/1.1 (what the git-proxy uses) | about 130 KiB (heap 105, stack 24) |
| HTTP/2 (the default of Go) | about 525 KiB with a provider that offers 1 MiB frames |

The HTTP/2 client of Go keeps a write buffer of the maximum frame size the provider offers for every request in flight, up to 512 KiB. GitLab.com offers 16 MiB, GitHub about 68 KiB (read from their SETTINGS frame). The git-proxy therefore speaks HTTP/1.1 to providers; one transfer is one stream, so HTTP/2 gains nothing here.

Plan with `limit = 20 MiB + 0.26 MiB x concurrent requests per replica`: 0.13 MiB live per request, doubled because the garbage collector lets the heap grow by that much before it collects. A session runs about 4 requests at once while it pushes (probe, `info/refs`, push, a parallel fetch), and a Service spreads connections evenly over the replicas.
The chart assumes a deadline burst of 100 sessions pushing at once: 400 requests, 200 per replica, about 72 MiB. It sets 256Mi, which is 3.5 times that and holds about 900 requests per replica. One instance at its cap of 16 costs 2 MiB.
Raise `gitProxy.resources.limits.memory` when a pod is killed for memory (`OOMKilled`) or `container_memory_working_set_bytes` stays above 70 percent of the limit during a burst. Raise `gitProxy.replicas` instead when you do not want a bigger pod: the replicas share the load, so doubling them halves the memory per pod.

## What users see on a push

- A denial at `info/refs` (the normal case) shows the message as `remote error`.
- A refusal after the push body began (the provider refuses or stalls) shows the message too, also for a push from a shallow clone (`--depth`).
- A refusal that comes before the proxy looks at the push (the instance has no free slot, the service is unreachable, or the service denies the POST only) cannot know what git reads. git prints `bad band` and the push fails; the log of the git-proxy has the reason.
- A client without side-band gets the message as an HTTP status; git shows only the status and "Everything up-to-date".

## Metrics

| Metric | Meaning |
|---|---|
| `eduide_gitproxy_requests_total{provider,kind,result}` | `result`: `ok`, `denied` (the service said no), `upstream-denied` (provider answered 401, 403, 404 or a redirect), `upstream-error` (provider unreachable or 5xx), `unavailable` (the service did not answer) |
| `eduide_gitproxy_bytes_total{provider,direction}` | `in` is client to provider (pushes), `out` is provider to client |
| `eduide_gitproxy_upstream_seconds{provider,kind}` | Duration of a forwarded request |

A rise of `unavailable` means the service internal port is not reachable.
A rise of `upstream-error` for one provider means that host is down or not reachable from the cluster.

## Logs

```bash
kubectl -n <ns> logs deploy/<release>-eduide-control-plane-git-proxy --since=15m
```

One JSON line per push and per request that did not succeed, with `user` (salted id), `session`, `provider`, `repository`, `kind`, `result`, `reason` and `status`.
Reads that succeed are only counted.
`reason` is `push-rule`, `connect`, `not-ready`, `unknown-host`, `unauthorized`, `anonymous-other` or `ok`.
Tokens and bodies are never logged.

## Failure modes

| Symptom in the session | Cause | Check and fix |
|---|---|---|
| `fatal: unable to access 'http://127.0.0.1:8082/...': Failed to connect` | The agent listener is down, or the `insteadOf` entry is wrong | `kubectl logs <pod> -c agent`. Check `git config --global --get-regexp insteadof` in the session |
| 404 on `/git/...` | The pod has no Git settings: it was claimed before the pool replaced it after a config change, or `gitAccounts.proxyURL` is empty | `kubectl get pod <pod> -o yaml` shows `AGENT_GIT_PROXY_URL` in the agent container. The pool replaces idle pods after a change of the Git settings (they are part of the pod hash), so wait for the roll or start a new session. A deployment without providers keeps its hash, so an upgrade replaces nothing |
| 403 on `/git/...` | The instance is not claimed | Expected for idle instances |
| 502 `git proxy unavailable` | git-proxy is down, or a NetworkPolicy blocks session pods | `kubectl -n <ns> get pods -l app.kubernetes.io/component=git-proxy`. Check the policy `session-to-git-proxy` and that the CNI enforces it. Agent log: `git proxy unreachable` |
| `remote error: EduIDE cannot check your access right now` | The service internal port is not reachable, or the service returns errors (git-proxy log: `git authorize unavailable`) | Check the policy `service-internal`, the service `internal` port (`kubectl -n <ns> get svc <release>-eduide-control-plane-service`), and the service logs. Both service replicas serve it |
| `remote error: <displayName> is not reachable right now` | The provider host is down or egress is blocked | Check the policy `git-proxy` egress (DNS, HTTPS, deny list) and the provider status page |
| `remote error: Connect your <displayName> account ...` | No account, or the account was revoked at the host | The user connects on the landing page. A new connection takes effect within 3 seconds, a disconnect within 5 |
| `remote error: EduIDE uses your <displayName> account only for the repository this session was started with` | A private repository other than the session repository, with approvals off (`gitAccounts.approval.mode: off`) | Start a session from that repository, or turn approvals on. This is the credential rule, not a fault |
| `remote error: This session may not use <host>/<repo> yet. Approve it here: <link>` | A repository the user has not approved for this session | The user opens the link on the landing page and confirms. A push needs access `write`. The change takes effect within 5 seconds |
| `remote error: EduIDE only pushes to the repository ...` | A push to a repository other than the session repository, or from a session without one | Start a session from the repository to push |
| `remote error: Too many Git requests of this session run at the same time` | More than 16 requests of one instance in flight | Wait and retry; one Git command uses a few. Tools that fetch many repositories in parallel should run fewer at a time |
| HTTP 429 from the git-proxy (`RPC failed; HTTP 429`) | More than 100 requests per second from one pod IP (burst 400) | Not reached by Git commands. A script that loops over Git requests, or a network that rewrites pod addresses so many pods share one source address (check the source IP in the git-proxy request) |
| `remote error: The transfer stalled and EduIDE cancelled it` | No byte moved for 5 minutes in either direction, or the provider did not answer within 30 minutes after the upload | A dead link or a provider that hangs. Retry. A transfer that keeps moving is never cut, and neither is a provider that takes a while to process a push (the wait between the end of the upload and the first byte of the answer has its own limit of 30 minutes) |
| `remote error: EduIDE cannot check your access right now` after a burst over many repositories | The service answered 429 twice: more than 100 authorize calls at once for one instance (20 per second, burst 100; the git-proxy retries once) | Run fewer parallel fetches (`git submodule update --jobs 4`). Repeated: check the git-proxy log for `git authorize unavailable` and the service for a client that loops |
| `remote error: EduIDE could not verify this session` | The service rejected the instance token: the session ended, or the token Secret is gone | Start a new session |
| `remote error: ... not found, or your <displayName> account has no access` | The provider answered 404, 403 or 401 for the account | The repository does not exist or the account lacks access. Revoked tokens are detected on the next use and the account is deleted |
| Git LFS prints `Git LFS is not supported through EduIDE.` | LFS is not proxied | Limitation. Do not use LFS in sessions |

### Policy not enforced

If a session can push to or read private repositories it should not, check in this order:

1. `kubectl -n <ns> get networkpolicy`: `session-to-git-proxy`, `git-proxy` and `service-internal` exist. Without enforcement by the CNI, session pods could reach the service internal port directly. The instance token is still required there, but the rule is the second line of defence.
2. The service log for `git authorize` errors. The decision lives in the service only (`internal/gitpolicy`), never in the git-proxy.
3. The `GIT_URI` of the session launch data (the Secret `<session>-data`; list its key names only with `kubectl get secret <name> -o json | jq '.data | keys'`, never print the values). The push rule compares it with the pushed repository without credentials, host case-insensitive, without a trailing `.git`.
4. Provider hosts in the config: a host that is no provider is refused (`unknown-host`).

## Scaling and rollout

Replicas are stateless; the Deployment has a PodDisruptionBudget (`maxUnavailable: 1`, so a node drain also works with `gitProxy.replicas: 1`, with a short break of Git while the pod moves).
A pod finishes running transfers for up to 60 seconds on shutdown (`terminationGracePeriodSeconds: 70`).
A longer clone or push during a rollout can break and the user retries.

## Repository approvals

Users approve further repositories per session on the landing page (ADR 0023, API `PUT /sessions/{name}/git/repositories`).
The approvals of a session are in the Secret `<session>-git-repos`, owned by the session, so they go when the session goes.
Read them as an admin (repository names and access, no credentials):

```bash
kubectl -n <ns> get secret <session>-git-repos -o jsonpath='{.data.approvals}' | base64 -d | jq
```

Every approval and every removal writes one service log line (`git repository approved`, `git repository approval removed`) with the salted user id, session, provider and repository.
A change, a disconnect or a removed approval takes effect within 5 seconds, because the git-proxy reuses an allowed answer for at most that long.

Turn the feature off with `config.gitAccounts.approval.mode: off` (the credential is then used for the session repository only; stored approvals stop counting at once).
Use `read` to allow approvals for reading only, which makes a push to another repository impossible.
`config.gitAccounts.approval.maxPerSession` bounds the approvals of one session.

## Provider changes

- **Rotate a client secret.** Update the Secret that `clientSecretRef` names, then restart the service: `kubectl -n <ns> rollout restart deploy/<service>`. The service reads the client secret files and the encryption key once at start and never again. A wrong secret shows as `git-provider-error` on connect and as a failed refresh. The git-proxy holds no client secret.
- **Remove a provider.** Stored accounts of a provider that is no longer in the config stay: the sweep skips them. Delete them with `kubectl -n <ns> delete secret -l eduide.github.io/component=git-account,eduide.github.io/git-provider=<name>`, and delete the OAuth application at the host, which revokes the tokens. Without that, the tokens stay valid at the host.
- **Provider on a private address** (a self-managed GitLab in the cluster network). The service calls the provider for OAuth, the account lookup and the preflight. The chart sets no egress NetworkPolicy for the service, so only the cluster network limits it. The git-proxy excludes private ranges by default: add a rule with `networkPolicy.gitProxyEgressExtra`. Session pods never get such a rule.
- **The Artemis host is also a provider.** Launches keep working. Git does not rewrite a URL with credentials (`https://user:token@host/...`), so the clone with the VCS token goes to the host directly. Requests without credentials for that host go through the proxy ([ADR 0023](../adr/0023-git-accounts-and-git-proxy.md), operation notes).
