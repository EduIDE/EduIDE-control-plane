# eduide-session (experimental)

VS Code-API extension (runs as a Theia plugin) that connects an EduIDE session to the in-pod agent.
It replaces the `EduIDE-data-bridge` extension and the Theia Cloud monitor extension.
The extension never opens a server.
It long-polls the agent on loopback (`GET /v1/session?after=<version>&timeout=30`), keeps the latest data in memory and sends activity heartbeats (`POST /v1/heartbeat`).

Agent URL: `http://127.0.0.1:8082` (fixed, loopback only).
The contract is `api/agent/v1.yaml` (tag `local`).

## Command contract (unchanged from the data bridge)

Both commands return at once, like the data bridge did.

- `dataBridge.getEnv(keys: string[]): Record<string, string>` returns the requested keys that are set (`{}` before the claim).
- `dataBridge.getEnvState(): { injected: boolean; environment: Record<string, string> }`: `injected` is true once the session is claimed (the agent stores launch data together with the claim). Poll it until `injected` is true.

The extension id changes: Scorpio looks up `tum-aet.data-bridge` and must switch to `eduide.eduide-session`.

## Notices and heartbeat

- Notices `idle-warning` and `max-warning` show as warning messages with the action "I am still here", which sends a heartbeat at once. `info` shows as an information message. Each notice shows once.
- Heartbeats go out on text document changes (file and untitled), editor selection and active editor changes and window focus, at most once per minute.
  Terminal output does not count, a long build must not keep an abandoned session alive.
- Errors (agent not yet up) back off from 0.5 s to 15 s. A poll that hangs longer than twice its timeout is aborted and retried. `deactivate` aborts the pending poll.

## Milestones

The extension reports startup milestones to the agent with `POST /v1/milestones` (`{"name", "at"}`, `at` is an RFC3339 UTC timestamp with milliseconds).
The agent keeps the first report per name, so each name is sent at most once, with one retry on a network failure. Errors are ignored.

- `plugin-host-started`: on activation.
- `workspace-ready`: when a workspace folder is open, at activation or on the first workspace folder change that leaves a non-empty list.
- `first-input`: together with the first heartbeat that is sent.

`clone-done` is not reported: Scorpio exposes no hook for it.

## Migration

Remove `data-bridge` and the `monitor-theia` extension from the IDE image and install the `.vsix` instead.
The old `DATA_BRIDGE_ENABLED` switch and the HTTP injection endpoint are gone; launch data now arrives from the agent.
The old `dataBridge.showLogs` command is not provided.

## Development

```
npm ci
npm run lint
npm test      # plain node:test against a fake agent HTTP server, no VS Code needed
npm run package   # builds dist/extension.js (esbuild) and eduide-session.vsix
```
