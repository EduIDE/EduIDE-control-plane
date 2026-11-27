# Git accounts for developers

How the pieces of [ADR 0023](../adr/0023-git-accounts-and-git-proxy.md) fit, where each rule lives, and how to run the feature on your machine.

## The pieces

```
landing page ---- /api/v1 (bearer) ----> service ---- Kubernetes API (token Secrets, approval Secrets)
     |                                     ^  |
     | OAuth redirect                      |  +--- provider host (OAuth, account, preflight)
     v                                     | authorize :8083
Git host                      git-proxy ---+
                                 ^   |
IDE ---- 127.0.0.1:8082/git ---> agent   +--- provider host (Git smart HTTP, user token)
```

| Piece | Code | Does |
|---|---|---|
| Accounts and tokens | `internal/gitaccount` | Provider clients, encrypted token Secrets, refresh with a lock, sweep, sealed connect state |
| REST API | `internal/httpapi/git*.go` | Connect, callback, preflight, disconnect, approvals. `returnTo` rule in `validReturnTo` |
| Credential rule | `internal/gitpolicy` | `mayUseCredential`: the one function that decides whether a session may use the credential for a repository |
| Git proxy | `cmd/git-proxy`, `internal/gitproxy` | Streams Git smart HTTP, asks the service per request, turns denials into Git messages |
| Agent | `internal/agent/git.go` | `/git/<host>/...` on the loopback listener, `GET /v1/git/hosts` |
| IDE extension | `extensions/eduide-session/src/git.ts` | Writes the `insteadOf` entries before it hands out launch data |
| Landing page | `landing-page/src/git.ts`, `landing-page/src/components/Git*.tsx`, `RepoPicker.tsx`, `App.tsx` | Accounts section, `/git/callback`, preflight, picker, approval page |
| Fake provider | `test/fake-git` | OAuth and Git smart HTTP for the e2e. It behaves like GitLab: a path without `.git` is a 301 (GET) or a 422 (POST) |

## Where each rule lives

| Rule | Place |
|---|---|
| The token never enters the pod | Only the service reads tokens (`gitaccount`); the git-proxy gets one per request from the authorize answer; the agent strips client credentials |
| Credential only for the session repository and approved repositories | `gitpolicy.mayUseCredential`, called by the authorize handler |
| `returnTo` is a path on the landing page | `validReturnTo` in the service, `safeReturnTo` in `landing-page/src/git.ts` |
| `code` and `state` leave the address bar and are used once | `captureGitCallback` and `takeGitCallback` in `landing-page/src/git.ts` |
| A preflight never blocks a launch | `preflight` in `landing-page/src/git.ts` returns `undefined` on any failure, `startChecked` in `App.tsx` then launches |
| Nothing is approved without a click | `GitApproval.tsx` calls `PUT` only from the "Allow" handler; `parseApproval` validates the link |
| Provider hosts count as allowed hosts for `GIT_URI` only | `internal/httpapi/validate.go` |
| What a repository path may look like (git-proxy, approval API, preflight) | `gitapi.CanonicalRepo`, the only place; the landing page mirrors it in `canonicalRepository` (`landing-page/src/git.ts`). The git-proxy and the preflight build the upstream path `/<repository>.git/<suffix>` from its result (GitLab needs the `.git`) |
| Limits of the git-proxy (per IP, per instance, idle and processing limit, ERR length) | `internal/gitproxy/limits.go`, used by `Handler.ServeHTTP` |
| How an error reaches git on a push (side-band channel 3, status, `ERR`; leading `shallow` lines; the 4 KiB peek only after authorize and the slot) | `internal/gitproxy/pusherror.go`, order in `Handler.ServeHTTP`; tests `TestUnauthorisedPushNeverPeeks`, `TestStalledUnauthorisedPushesAllocateNothing` and `TestPushErrorsReachTheUser` |
| A client that sends headers and stalls its body holds no connection after the answer (the handler expires the read deadline when it returns; the agent route does the same when its answer starts; the internal listener has a `ReadTimeout`) | `Handler.ServeHTTP`, `gitHandler` in `internal/agent/git.go`, `internalServer` in `cmd/service/main.go`; tests with a real server and stalled raw connections: `TestStalledBodyHoldsNothingOnAnyPath`, `TestStalledBodyOnTheGitRouteHoldsNothing`, `TestInternalServerReleasesStalledBodies`; keep-alive `TestKeepAliveSurvivesTheReadDeadline` |
| Memory per request of the git-proxy (HTTP/1.1 to the provider, chart limit) | `Transport` in `internal/gitproxy/proxy.go`; measured by `TestMemoryOfStalledPushes`, figures in `docs/operations/git-proxy.md` |
| Limiter and token cache of the authorize endpoint | `internal/gitpolicy/gitpolicy.go` (`tokenOK` before `allow`) |
| Commit identity from the provider's values | `identityName`, `validIdentityMail` in `internal/httpapi/git.go` |

## Run it locally

The simplest way is the e2e cluster, which has a fake provider:

```sh
E2E_RUN=TestGitAccounts make e2e     # builds the cluster, installs values-git.yaml, runs the Go tests and the Playwright spec
```

The cluster stays up after the run. Use the isolated kubeconfig for every command (`export KUBECONFIG=$PWD/.e2e/kubeconfig`, check that the context is `kind-*`).
The landing page is at `https://ide.e2e.test:18443` in a browser that maps `*.e2e.test` to `127.0.0.1` (the Playwright config shows how). The users are `alice` and `bob`, password equal to the name.
The browser cannot resolve the fake at `fake-git.eduide-e2e.svc`. Forward it and answer the authorize step yourself, as `git-accounts.spec.ts` does:

```sh
kubectl -n eduide-e2e port-forward svc/fake-git 18444:443
```

For landing page work alone, `npm run dev` in `landing-page/` needs a service with providers. The unit tests (`npm test`) mock the API and cover every state of the page.

## Add a provider type

1. Add the type to `internal/config` (`GitProvider.Type`, the validation, the OpenAPI enum `GitProvider.type`).
2. Implement the `Provider` interface (`internal/gitaccount/provider.go`) in a new file next to `github.go` and `gitlab.go`: authorize URL with PKCE, code exchange, refresh, revoke and the account lookup, which also sets `Account.NoReplyEmail`. Add the default endpoints to `DefaultEndpoints` and select the type in `NewProvider`.
3. Set the upstream basic auth user in `gitpolicy.UpstreamUser`.
4. Test the provider against a fake (`internal/gitaccount` tests, and `test/fake-git` if the e2e must cover it).
5. Update ADR 0023, `docs/operations/git-proxy.md` and the chart values documentation.
The landing page needs no change: it shows the `displayName` the service sends.
