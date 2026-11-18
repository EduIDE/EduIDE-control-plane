# AGENTS.md - landing-page

The page a student lands on (experimental). Vite and React SPA on the control plane API, served by a Go static server.

`CLAUDE.md` is a symlink to this file.

## Layout

```
src/main.tsx             startup order: fragment capture, config.json, Keycloak, render
src/artemisToken.ts      #artemisToken handling (G16)
src/launch.ts            launch request, state mapping, traceparent
src/runLaunch.ts         launch state machine: POST, follow SSE, reconnect
src/overview.ts           environment rows and delete (G15)
src/i18n.ts, I18nProvider.tsx  English and German strings, language choice
src/legal.ts             /imprint and /privacy routes, sanitized Markdown
src/templates.ts         start time lines and launch info from templates
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
- The legal pages and the footer render before Keycloak (`main.tsx`), so they work without a login.
- Every UI string exists in English and German in `i18n.ts`; no emojis, icons come from `lucide-react`; the GitHub mark is an inline SVG in `components/Icons.tsx`.
- Prettier with 4-space indent, `simple-import-sort` and `no-null` are errors, warnings are fatal.
- Run `npm run lint`, `npm test`, `npm run build` and `cd server && go test ./...`.
