# Development setup

## Tools

| Tool | Version | Use |
|---|---|---|
| Go | 1.27.4 | All binaries, tools run through `go tool` (controller-gen, oapi-codegen, setup-envtest) |
| Node.js and npm | current LTS | TypeScript client, extension `eduide-session`, landing page |
| Docker | any | Images, kind |
| kind, kubectl, helm | current | Local cluster, charts, e2e |
| golangci-lint | current | `make lint` |
| Python 3 and the `drawio` CLI | optional | Regenerate [diagrams](../architecture/diagrams/README.md) |

## First build

```bash
git clone https://github.com/EduIDE/EduIDE-control-plane.git
cd EduIDE-control-plane
make generate
make build
make test
```

Layout and rules: [AGENTS.md](../../AGENTS.md).

## Generated code

Never edit `*.gen.go`, `zz_generated*.go`, `config/` or `clients/typescript/src/schema.gen.ts` by hand.
Change the source (`api/v1alpha1`, `api/openapi/v1.yaml`, `api/agent/v1.yaml`) and run `make generate`.
CI runs `make verify-generate` and fails on a diff.

## Local cluster safety

The default kube context on your machine can be a real cluster.
Every command that talks to a local cluster runs with an isolated kubeconfig:

```bash
export KUBECONFIG=$(pwd)/.e2e/kubeconfig
kubectl config current-context
kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}'
```

The context must be `kind-eduide-e2e` and the server `https://127.0.0.1:<port>`. If not, stop.
Set the variable in every script and shell, not only for one command. In fish use `set -x KUBECONFIG (pwd)/.e2e/kubeconfig`.
On OrbStack, containers bind to `127.0.0.1` only; use that address instead of `localhost`.

## Conventions

- Terms: Template, Pool, Instance, Session, Workspace, Claim, Release, launch data.
- New logic comes with tests (see [testing.md](testing.md)).
- Docs: English, active voice, present tense, short sentences, hyphens instead of dashes.
- API or CRD changes: skills `add-api-endpoint` and `add-crd-field` in [.claude/skills](../../.claude/skills). Decisions: `write-adr`.
- No university-specific hosts, realms or labels in the repo; use `example.org`.
- Ask before committing or pushing.
