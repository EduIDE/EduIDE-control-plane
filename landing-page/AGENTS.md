# AGENTS.md - landing-page

The page a student lands on (experimental). Vite and React SPA on the control plane API, served by a Go static server.

`CLAUDE.md` is a symlink to this file.

## Layout

```
src/main.tsx             startup order: fragment capture, config.json, Keycloak, render
src/artemisToken.ts      #artemisToken handling (G16)
src/launch.ts            launch request, state mapping, traceparent
src/runLaunch.ts         launch state machine: POST, follow SSE, reconnect
src/overview.ts          overview rows (G15)
src/api.ts               control plane client and SSE streams
src/api-client/          committed copy of clients/typescript/src, never edit (npm run sync-client)
src/components/          presentational and page components
server/                  Go static server (own module, stdlib only)
```

## Rules

- `captureFromWindow` must run before `initKeycloak`: keycloak-js uses the fragment and Keycloak rejects fragments in redirect URIs.
- Launch data (token, git URL) never goes into logs, `display` (credentials are stripped from `display.source`), URLs or localStorage.
- The `display` fields are shown to the user, so they must stay non-secret.
- Runtime config comes only from the served config.json. Nothing is baked in at build time.
- Prettier with 4-space indent, `simple-import-sort` and `no-null` are errors, warnings are fatal.
- Run `npm run lint`, `npm test`, `npm run build` and `cd server && go test ./...`.
