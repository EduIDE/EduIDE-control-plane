# 0006. Agent sidecar as the only data path into the pod

- Status: Accepted
- Date: 2026-11-18
- Decisions: G4, G7, G18, U8
- Contracts: [api/agent/v1.yaml](../../api/agent/v1.yaml), [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go) (`InstanceTokenHeader`, `InstanceTokenKey`)

## Context and Problem Statement

The old stack injects user data by polling: the operator tries every 2 s, up to 900 times, to reach the Data Bridge in the IDE.
The IDE must also be isolated from other pods now that oauth2-proxy leaves the pod ([0005](0005-session-auth-at-gateway.md)).
We need one entry into the pod for claims, activity and traffic.

## Decision Drivers

- Warm claim in milliseconds, no polling.
- User data never on disk, in logs or in the CR (G18).
- IDE images stay unchanged and keep listening on loopback (G4).
- Small footprint, target below 10 MiB per agent.

## Considered Options

1. Operator talks directly to the IDE (current Data Bridge).
2. Agent sidecar with a push on claim and a local long-poll for the extension.
3. Data in env vars or a mounted Secret.

## Decision Outcome

Chosen option: "agent sidecar", the only entry into the pod. Protocol: [api/agent/v1.yaml](../../api/agent/v1.yaml).

Listeners:

| Listener | Address | Caller | Auth |
|---|---|---|---|
| proxy | `0.0.0.0:8080` | Envoy | requires `X-EduIDE-Instance-Token`, strips it, forwards HTTP and WebSocket to the IDE on `127.0.0.1:<port>` |
| control | `0.0.0.0:8081` | operator | bearer instance token; `/healthz` and `/readyz` without auth for the kubelet |
| local | `127.0.0.1:8082` | `eduide-session` extension | none, loopback only; long-poll and heartbeat |

- The instance token is created with the pod, from the Secret mounted at `/var/run/eduide/token`. The claim does not change it (G4). Comparisons are constant-time.
- The HTTPRoute sets the header, overwriting client values ([0004](0004-gateway-api-only.md)). Residual risk: the token is readable in the HTTPRoute spec by admins and the operator.
- `POST /v1/claim` is pushed once per claim with the launch data from the session Secret. It is idempotent for the same session and answers 409 for another one. The agent keeps the data in memory only.
- `GET /v1/status` returns claimed, ready, last activity and open WebSocket connections. The operator polls it throttled ([0016](0016-activity-detection.md)).
- `POST /v1/notify` carries timeout warnings to the extension.
- The extension `eduide-session` replaces the data bridge and the monitor extension (G7). It only calls out to `127.0.0.1:8082` and opens no server. The commands `dataBridge.getEnv` and `dataBridge.getEnvState` stay unchanged.
- There is no compatibility mode for old IDE images. The images change in an experimental pull request in the image repository (U8).
- `/readyz` is ready when the IDE answers on its loopback port. An idle ready pod counts as `idleReady`.
- Later: the `snapshot` mode adds fields to the claim request ([0007](0007-persistence-modes.md)).

### Consequences

- Good: One push per claim, the extension reads the data within milliseconds after the plugin host starts.
- Good: Isolation and the data path share one component with one token check.
- Bad: One more container per pod (offset by dropping oauth2-proxy). It is a single point of failure per pod; the pod is not ready without it.
- Bad: All traffic passes the agent proxy. It must handle WebSockets and large uploads correctly; proxy tests cover that.
- Follow-up: Fuzzing for payload parsing and the 16 KiB claim limit ([0015](0015-test-strategy.md)). Data rules are in [0012](0012-user-data-secrets-allowlist.md).

## Amendment 2026-11-25: pull policy of the agent

The config field `agent.imagePullPolicy` (additive, optional) sets `imagePullPolicy` on the agent container, so a mutable tag with `image.pullPolicy: Always` reaches session pods too. Unset keeps the old pod and spec hash.

## Pros and Cons of the Options

### Direct to the IDE

- Good, because no extra component.
- Bad, because polling and no isolation.

### Agent sidecar

- Good, because push, isolation and activity in one place.
- Bad, because of the extra container.

### Env vars or mounted Secret

- Good, because simple.
- Bad, because a warm pod cannot receive data after start, and env values leak into pod specs.
