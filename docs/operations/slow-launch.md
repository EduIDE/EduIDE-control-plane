# Runbook: slow launch

Symptom: users wait longer than the budget (warm under 2 s, cold under 30 s at p95), or sessions end in `Failed`.

## Find the slow phase

1. Metrics: `eduide_session_startup_seconds` by `strategy` and `result`, and `eduide_session_phase_seconds{phase=claim|pod_ready|agent_ready}` ([metrics](../contracts/metrics.md)).
2. One session: read the timestamps.

```bash
kubectl -n <ns> get idesession <name> -o yaml | grep -A12 -E 'startStrategy|timestamps|errorCode|message'
```

   Compare `claimed`, `podReady`, `agentReady`, `ready` with the creation time. The largest gap is the slow phase ([start-path.md](../architecture/start-path.md)).
3. Trace: find the launch by session name in Jaeger. **Planned:** Jaeger comes with the observability work package. The skill `debug-session-launch` in [.claude/skills](../../.claude/skills) lists the steps.

## Causes and fixes by phase

| Slow phase | Causes | Checks and fixes |
|---|---|---|
| Session created to `claimed` | Operator not running or not leader, work queue backlog, API server slow | `kubectl -n <ns> get lease eduide-operator`, operator logs and `/readyz`. Restart the operator. |
| `claimed` (claim) | No idle pod, conflicts | See [pool-empty.md](pool-empty.md). |
| `podReady` | Image pull, unschedulable pod, PVC binding (`volume`), slow IDE start | `kubectl -n <ns> describe pod ide-<instance>`; check events for pull, scheduling and volume errors; enable image prepull; check the StorageClass. |
| `agentReady` | Agent not ready, claim call fails | `kubectl -n <ns> logs ide-<instance> -c agent`; `eduide_agent_requests_total{result="error"}`; NetworkPolicy blocks operator to :8081. |
| `ready` to a usable IDE | Gateway not yet programmed, login redirect, IDE plugin host start | Check the HTTPRoute is accepted: `kubectl -n <ns> get httproute ide-<instance> -o yaml`. See [gateway-backlog.md](gateway-backlog.md) and [authz-errors.md](authz-errors.md). |

## Failed sessions

| `errorCode` | Meaning | Fix |
|---|---|---|
| `capacity-exhausted` | No room for the pod after `capacity.exhaustedAfter` | Add capacity or quota ([pool-empty.md](pool-empty.md)) |
| `template-not-found` | `templateRef` does not exist | Check the template name in the chart values |
| `workspace-not-found` | `workspaceRef` does not exist | Check the workspace; it may have expired |
| `start-failed` | Pod or agent failed | Read `status.message`, pod logs and events |

Users who hit the per-user limit get `session-limit-reached` from the API with the list of their running sessions; that is not a fault.

## Never

Do not copy launch data (tokens) from Secrets into tickets or chats. Logs and traces contain none; if you find one, treat it as a bug ([ADR 0012](../adr/0012-user-data-secrets-allowlist.md)).
