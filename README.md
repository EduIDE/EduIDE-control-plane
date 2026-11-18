# EduIDE control plane

> **Experimental.** This is a rewrite in progress. Do not run it in production.
> The production control plane is still [EduIDE-Cloud](https://github.com/EduIDE/EduIDE-Cloud).

Kubernetes control plane for [EduIDE](https://github.com/EduIDE): it starts browser IDEs for students, prewarms them in pools, keeps their files in workspaces and guards access at the gateway.

| Binary | Role |
|---|---|
| `operator` | Reconciles `IDETemplate`, `IDEPool`, `IDESession`, `IDEWorkspace`; owns pods, services, routes, volumes |
| `service` | REST API `/api/v1` with OIDC and Server-Sent Events for the landing page |
| `authz` | ext_authz for Envoy Gateway: only the owner of a session reaches its IDE |
| `agent` | Sidecar in every session pod: instance-token proxy, launch data, activity |
| `pool-scaler` | Sets `targetIdle` of pools via `/scale` |
| `landing-page` | Launch page and overview of sessions and workspaces |

Start here:
- [AGENTS.md](AGENTS.md): layout, commands, rules
- [docs/adr](docs/adr/README.md): architecture decisions
- [docs/architecture](docs/architecture/): overview and start path
- Contracts: [CRDs](api/v1alpha1), [REST API](api/openapi/v1.yaml), [agent protocol](api/agent/v1.yaml), [metrics](docs/contracts/metrics.md)

```bash
make generate   # code from the contracts
make test       # unit and envtest
make e2e        # kind cluster, full matrix
```

Images: `ghcr.io/eduide/eduide-control-plane/{operator,service,authz,agent,pool-scaler,landing-page}`.
Charts: `oci://ghcr.io/eduide/eduide-control-plane/charts/{eduide-control-plane-crds,eduide-control-plane,eduide-landing-page}`.

License: EPL-2.0.
