# e2e harness

`make e2e` builds a local kind cluster that looks like a deployment, installs the control plane with a fake IDE, and runs the full matrix (ADR 0015).
`make e2e-down` deletes the cluster.

## Safety rules

The default kube context of a developer machine can be a real cluster. The harness never uses it.

- Every script exports `KUBECONFIG=<repo>/.e2e/kubeconfig` first and creates the kind cluster with `--kubeconfig` on that file.
- `kubectl` and `helm` are shell functions in `safe.sh`. They check before every call that the current context is `kind-$E2E_CLUSTER` and that the server is `https://127.0.0.1:...`, and exit otherwise.
- The Go tests read `KUBECONFIG` from the environment and refuse to run when it is unset, names more than one file, or the API server is not on `127.0.0.1`.
- kind binds the gateway to `127.0.0.1` only. kind is installed with `go install` into `<repo>/bin`, nothing globally.
- Never run `kubectl` or `helm` in this repository without `export KUBECONFIG=$PWD/.e2e/kubeconfig`.

## Architecture

```
host (127.0.0.1:18443)  --kind extraPortMapping-->  node port 30443
                                                      |
                                            Service gateway-entry (envoy-gateway-system)
                                                      |
        Envoy pods of Gateway "eduide" (namespace eduide-e2e), HTTPS listeners:
        ide.e2e.test   instance.ide.e2e.test   webview.instance.ide.e2e.test   keycloak.e2e.test   smoke.e2e.test
              |                  |                         |                          |                  |
     landing page, API     session routes            session routes                Keycloak         fake IDE behind
     (chart)               (operator, SecurityPolicy with OIDC and ext_authz, chart)                   OIDC (smoke)
```

| Part | Choice |
|---|---|
| Cluster | kind v0.33, one control plane and two workers. The workers carry the node pool label `e2e.eduide.test/node-pool` with the values `pool-a` and `pool-b` (`config.placement.nodePoolLabel`). |
| CNI | Calico v3.33.1 (kind runs with `disableDefaultCNI`), because kindnet does not enforce NetworkPolicies. |
| Gateway | Envoy Gateway v1.8.3 from the OCI Helm chart. The chart installs the Gateway API CRDs too. GatewayClass `eduide-e2e`, Gateway `eduide` in the namespace `eduide-e2e`. |
| TLS | HTTPS with a generated CA (`tools/certgen`, files in `.e2e/certs`). The OIDC filter sets secure cookies and the redirects need `https`. One certificate covers all five hosts. |
| DNS | The names `*.e2e.test` do not exist. In the cluster, CoreDNS rewrites them to the Service `gateway-entry`, so service, authz, the Envoy Gateway controller and Envoy reach Keycloak at the same issuer URL `https://keycloak.e2e.test/realms/eduide` as the tests. On the host, the test client dials `127.0.0.1:18443` for every `*.e2e.test` name. |
| CA trust | The CA is a ConfigMap `e2e-ca`. It is mounted into the Envoy Gateway controller (Helm values `eg-values.yaml`), into the Envoy proxies (`EnvoyProxy` patch in `manifests/gateway.yaml`, over `/etc/ssl/certs/ca-certificates.crt`), and into the `service` and `authz` deployments (`run.sh` patches them after the chart install and sets `SSL_CERT_FILE`). Playwright ignores certificate errors. |
| Keycloak | `quay.io/keycloak/keycloak:26.4`, dev mode, realm `eduide` imported from `realm.json`. Confidential client `eduide-gateway` (secret in `e2e-gateway-client`, key `client-secret`), public client `eduide-landing` with direct access grants for tests. Users `alice` and `bob`, password equal to the name. All test values, the cluster is throwaway and local. |
| Fake IDE | `test/fake-ide`, loaded into the cluster as `ghcr.io/eduide/eduide-control-plane/fake-ide:e2e` (alpine base with `git`, so `sh` exists for the prepull init container). It serves `/`, a WebSocket echo `/ws`, `/headers` (header names only), `/data-state` (key names and a hash, never values), `POST /heartbeat` and `POST /git-test` (fixed Git operations, exit codes and scrubbed stderr only) and `POST /try-approve` (a PUT without a token to the service and the agent Git path, status codes only). It long-polls the agent like the extension and writes the Git `insteadOf` entries from `GET /v1/git/hosts`. |
| Fake Git provider | `test/fake-git`, loaded as `ghcr.io/eduide/eduide-control-plane/fake-git:e2e` (alpine with `git`), deployed by `manifests/fake-git.yaml` as Service `fake-git` on port 443. It has a CA of its own (`.e2e/certs-git`). See "Git accounts run" below. |
| Charts | `charts/eduide-control-plane-crds`, `charts/eduide-control-plane` with `values.yaml`, `charts/eduide-landing-page` with `values-landing.yaml`. Images come from `make images TAG=e2e` plus the fake IDE, loaded with `kind load`. |

## Templates in `values.yaml`

| Template | Purpose |
|---|---|
| `plain` | no pool: matrix cells none and volume without pool, gateway auth, idempotency, session limit, NetworkPolicy |
| `warm` | pool of 2: matrix cells none and volume with pool, data path |
| `short` | idle 30s, warning 15s: idle timeout |
| `cap-a`, `cap-b` | pool of 2 in node pool a and b: capacity pressure |
| `cap-a-big` | requests 1000 CPU in node pool a, never schedulable: capacity pressure |

`exhaustedAfter` is 20s in the test values, the rate limit is high, `sessionsPerUser` is 3.

## Tests

Go tests with the build tag `e2e` in this directory, run in the order of the files, one at a time. Every test deletes its sessions.

| File | Covers |
|---|---|
| `infra_test.go` | Keycloak through the gateway, tokens, gateway OIDC on the smoke route. Needs no control plane (`E2E_INFRA_ONLY=1`). |
| `matrix_test.go` | the four cells of ADR 0007 (cold, warm-ready measured, volume cold with and without pool), idempotent launch, session limit |
| `data_test.go` | launch data reaches the fake IDE (key names and hash), the token is in no pod log, pod, IDESession or API answer |
| `auth_test.go` | owner 200, other user 403, anonymous redirect to Keycloak, free pool instance 403, after release 403, token header stripped, WebSocket; NetworkPolicy negative test with a probe pod and a positive control |
| `capacity_test.go` | oldest idle pod of the same node pool deleted, other pool untouched, session fails with `capacity-exhausted` |
| `lifecycle_test.go` | SSE events, idle timeout |
| `git_test.go` | Git accounts (ADR 0023), own phase with `values-git.yaml`: connect flow, preflight, foreign state, clone, commit, push and the credential rule in a session, repository approval (link in the refusal, read then write, negative cases, the pod cannot approve), refresh, disconnect, no token in logs and containers, NetworkPolicy. See below. |
| `browser/` | Playwright: Artemis launch without login (G16), the token reaches the IDE. `git-accounts.spec.ts` runs in the Git phase: connect through the page, a start link to the private repository that asks to connect and then launches, the approval page |

## Git accounts run

The default install has no Git providers, so the suite above runs unchanged.
After it, `run.sh` runs `helm upgrade` with `values-git.yaml` on top of `values.yaml` and runs the tests `TestGit*`.
The values add the provider `fake` (type `gitlab`, host `fake-git.eduide-e2e.svc`, CA from the ConfigMap `fake-git-ca`) and `networkPolicy.gitProxyEgressExtra`, a rule that lets git-proxy reach the pods of the fake on port 8443. Session pods get no such rule, and a test proves they cannot reach the fake.
The next `make e2e` installs the chart without providers again.

The tests play the landing page: they call connect, open the authorize URL at the fake through `kubectl port-forward` (certificate checked against the fake CA), read code and state from the `Location` header and post them to `/git/callback`.
The port-forward and the Git steps in the session use the kind kubeconfig only: the helper checks once that the context is `kind-*` and the server is on `127.0.0.1`.
Codes, tokens and state never appear in the test output; failures name the number of the value only.

```sh
E2E_RUN=TestGitAccounts make e2e     # only this phase (the base install still runs first)
```

Without `E2E_GIT_CA_FILE` (set by `run.sh`) the tests skip, so `go test -tags e2e ./test/e2e/` against a cluster without the feature stays green.
After the Go tests the phase runs `browser/git-accounts.spec.ts`. The browser cannot resolve the fake's cluster address, so `run.sh` starts `kubectl port-forward svc/fake-git` on `E2E_FAKE_GIT_PORT` (default 18444) and the spec answers the authorize step through it, the way the fake would redirect a browser.
The approval modes `read` and `off` are not part of this run (default mode `write`).
The refresh test waits about a minute: the fake's access tokens live 75 seconds and the service refreshes a minute before expiry.

## Run

```sh
make e2e                       # everything
E2E_INFRA_ONLY=1 make e2e      # only the cluster and the infrastructure smoke test
E2E_RUN=TestMatrix make e2e    # a go test -run expression (TestGit* selects the Git accounts phase only)
E2E_SKIP_BROWSER=1 make e2e    # without Playwright (it installs Chromium on first use)
E2E_SKIP_IMAGES=1 make e2e     # reuse the images already loaded
make e2e-down                  # needs kind on PATH: export PATH=$PWD/bin:$PATH (run.sh installs it there)
```

`run.sh` is idempotent: it reuses the cluster, re-applies manifests and does `helm upgrade --install`.
Variables: `E2E_CLUSTER` (eduide-e2e), `E2E_PORT` (18443), `TAG` (e2e).
Needs Docker (OrbStack or Docker Desktop), Go, Helm, kubectl, jq, Node.js 22 for the browser test.

To run only the Go tests against a running cluster:

```sh
export KUBECONFIG=$PWD/.e2e/kubeconfig
E2E_CA_FILE=$PWD/.e2e/certs/ca.crt go test -tags e2e ./test/e2e/ -run TestMatrix -v
```

## Debug

```sh
export KUBECONFIG=$PWD/.e2e/kubeconfig
kubectl -n eduide-e2e get idesessions,idepools,pods,httproutes
kubectl -n eduide-e2e logs deploy/<operator deployment> | tail -100
kubectl -n envoy-gateway-system logs deploy/envoy-gateway          # OIDC discovery errors show here
kubectl -n eduide-e2e get securitypolicy -o yaml                   # status shows accepted or not
kubectl -n envoy-gateway-system get pods -l app.kubernetes.io/name=envoy
curl --resolve ide.e2e.test:18443:127.0.0.1 --cacert .e2e/certs/ca.crt https://ide.e2e.test:18443/
```

- Login loops or a 403 for the owner: check the SecurityPolicy status, the `eduide-id` cookie name against `config.oidc.idTokenCookie`, and the authz logs.
- Keycloak not reachable from a pod: check that the `rewrite` line is in `kubectl -n kube-system get cm coredns -o yaml` and that `gateway-entry` has endpoints.
- The NetworkPolicy test fails with open ports: Calico is not enforcing (ADR 0019). Check `kubectl -n kube-system get pods -l k8s-app=calico-node`.
- The cluster stays up after a failed run. Fix, then run `make e2e` again.

## Known limits

- The fake IDE answers on one port and does not run user code. The nightly test with the real IDE image is separate (ADR 0015).
- The idle warning check in `TestIdleTimeout` only logs when the warning is missed, because the polling interval of the operator can skip it.
- The Playwright test assumes the landing page reads `appDef` as the template name and `gitUri` as `GIT_URI`.
