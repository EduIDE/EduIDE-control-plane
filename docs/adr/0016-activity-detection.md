# 0016. Activity detection through the agent (pull by the operator)

- Status: Accepted
- Date: 2026-11-18
- Decisions: G7, G14
- Contracts: [api/agent/v1.yaml](../../api/agent/v1.yaml) (`/v1/status`, `/v1/heartbeat`, `/v1/notify`), [api/v1alpha1/idesession_types.go](../../api/v1alpha1/idesession_types.go) (`lastActivity`), [api/v1alpha1/idetemplate_types.go](../../api/v1alpha1/idetemplate_types.go) (`Timeouts`)

## Context and Problem Statement

Sessions end after an idle timeout (default 60 min, warning after 50 min) or a maximum lifetime (24 h).
The old stack asks the IDE's monitor extension through the operator, which couples it to the IDE and writes status often.
With 1,000 sessions the operator must not write 1,000 status updates a minute.

## Decision Drivers

- Few API writes. Activity is coarse (5 minutes).
- No inbound server in the IDE (G7).
- A browser that is gone after the claim must count as idle.

## Considered Options

1. The IDE extension pushes activity to the control plane.
2. The agent collects activity, the operator pulls it from the agent.
3. The agent writes activity to the API server itself.

## Decision Outcome

Chosen option: "agent collects, operator pulls".

- The only activity source is the extension heartbeat (`POST /v1/heartbeat`, on real input, at most once per minute). Proxied requests do not count, because an open tab polls without a user. `GET /v1/status` returns the latest heartbeat as `lastActivity` and the number of open WebSocket connections.
- A claimed session with 0 connections and no activity counts as inactive: no browser means idle (G7).
- The operator polls `status` with bounded concurrency and jitter. At 1,000 sessions that is about 1,000 requests a minute, which is trivial. It uses the control listener with the instance token ([0006](0006-agent-sidecar.md)). The NetworkPolicy allows only the operator ([0019](0019-network-isolation.md)).
- The operator writes `status.lastActivity` only when it moves by more than 5 minutes. Status updates happen only on real change.
- Timeouts come from the template (`idle`, `warning`, `max`, defaults 60 min, 50 min, 24 h). At the warning time the operator sends `POST /v1/notify` and the extension shows it through the local long-poll.
- On expiry the operator deletes the session. The pod is always discarded, `volume` workspaces stay ([0007](0007-persistence-modes.md)).
- Calls are counted in `eduide_agent_requests_total` with `op=status`.

### Consequences

- Good: No inbound port in the IDE, few API writes, activity covers real input and network traffic.
- Good: The agent works the same for warm and cold pods.
- Bad: Up to one polling interval of delay for timeouts, acceptable for 60 minute limits.
- Bad: An unreachable agent has to be handled: after repeated failures the operator marks the session condition and does not end it on missing data alone.
- Follow-up: Tests simulate heartbeat, lost browser and unreachable agent with fakes ([0015](0015-test-strategy.md)).

## Pros and Cons of the Options

### IDE pushes

- Good, because direct.
- Bad, because every IDE needs credentials to the control plane.

### Agent collects, operator pulls

- Good, because bounded, throttled and local.
- Bad, because polling delay.

### Agent writes to the API

- Good, because no operator polling.
- Bad, because the agent would need API permissions, which pods must not have.
