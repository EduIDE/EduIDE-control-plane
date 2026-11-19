# EduIDE Landing Page

> **Experimental.** Part of the EduIDE control plane. Interfaces, configuration and behavior can change without notice.

The page a student lands on: it starts an IDE session through the control plane REST API (`/api/v1`) and lists the user's running sessions and saved workspaces. A Vite and React app, served by a small Go static server.

## Features

- One page at `/` (and `/me/overview`): "Your environments" on top with live updates, "Start new" tiles below
- Environment rows show the stop time of a running environment (with the reason: idle or time limit) or the deletion date of a saved workspace, red only when files are about to be lost. Every row has Start and Delete (with a confirmation dialog)
- Tiles show the start time (`startEstimate`), saved or temporary workspace, and a dialog with what the environment includes. Templates with project templates (`variants`) open a picker first
- Launch from an Artemis link (`?appDef=<template>&gitUri=...`) or from a tile. Progress follows the session event stream (SSE) and redirects when the session is Ready. At the session limit the page lists the running environments to delete or open
- English and German (browser language, switchable in the avatar menu), colors follow the system light or dark setting
- `/status` (no login): the overall state in plain language, the three parts (sign in, starting environments, saved workspaces) and the start time per template, split into temporary and saved workspaces. Reads the public `GET /api/v1/status` every 30 s and shows "Status is not available right now" when the request fails
- `/admin` (login and admin role): a read-only panel with Status, Pools, Sessions (with details and trace link), Workspaces, Problems, Templates and Limits as real paths (`/admin/pools`, `/admin/sessions`, ...). It loads lazily, so students never download it. Live data comes from the admin event streams, other pages reload every 30 s. Charts and figures that need Prometheus are hidden when the service sends none. Users appear only as pseudonymous hash
- Footer on every page with GitHub, bug report, imprint and privacy. `/imprint` and `/privacy` work without a login

## Link parameters

| Where | Name | Meaning |
|---|---|---|
| query | `appDef` | template name |
| query | `gitUri`, `gitRef`, `artemisUrl` | launch data (only keys the template allows are sent) |
| query | `title` | optional title shown in the environment list |
| query | `persistence` | optional `none` or `volume`, else the template default |
| fragment | `artemisToken` | Artemis token. Read before Keycloak starts, kept in `sessionStorage`, removed from the address bar, deleted after the launch. `?artemisToken` still works and logs a deprecation warning |

## Configuration

The server serves `/etc/eduide/landing/config.json` as `GET /config.json` (a ConfigMap in the chart). The page loads it at startup.

```json
{
  "apiBaseUrl": "/api/v1",
  "keycloak": { "url": "https://keycloak.example.org", "realm": "eduide", "clientId": "landing" },
  "title": "EduIDE",
  "legacy": false
}
```

Required: `legal.imprint` and `legal.privacy`, each exactly one of `{ "url": "https://..." }` (linked directly) or `{ "path": "/legal/imprint.md" }` (Markdown that the server serves from the directory of `config.json`, rendered at `/imprint` and sanitized). The chart sets these from its `legal` values.

Optional keys: `hstsIncludeSubdomains` (default `true`, adds `includeSubDomains` to HSTS; set `false` when the session hosts are not subdomains of the landing host), `footerLinks` with `attribution.version` (shown as "EduIDE <version>"), `github.url` (default the control plane repository), `bugReport` (`text`, `url`), and `featureRequest` and `about` (not shown unless set). `legacy` is reserved and not used yet.

For local development copy that file (with `legal`) to `public/config.json` (git-ignored) and run `npm run dev`. Put Markdown files for the `path` variant in `public/legal/`.

## Admin panel

The panel needs the Keycloak role that the service config names (`oidc.admin`, see ADR 0021 and the install guide). The service checks the role on every request. The landing page asks `GET /api/v1/admin/status` once: only on `200` the avatar menu shows an "Admin" entry. With `403` or `404` the entry stays hidden, and opening `/admin` directly shows "No access". The check in the browser is a convenience, not a protection.

## Logos

All logos come from the bundle, the page loads no image from another origin (the CSP allows only `'self'` and `data:`). The files live in `src/assets/logos/<key>-logo.png`, Vite hashes them into `dist/assets`, and `src/logos.ts` resolves them. A template looks up its exact name (`java-17`), then its language prefix (`java-17` to `java`, `c-templates` to `c`), then the `logoUrl` of the API, and otherwise shows a folder icon. Project templates (`maven`, `gradle`, `make`, `bazel`) look up their id the same way.

The page uses a `logoUrl` from the API only when it is a path on its own origin (starts with `/`, not `//`). Absolute addresses are ignored. To add a logo, add `<key>-logo.png` to `src/assets/logos` and rebuild the image.

## Keycloak

The greeting uses the token claim `given_name` (else the first word of `name`). Both come from the `profile` client scope, so the landing page client needs that scope. Without it the page greets without a name and shows `?` in the avatar.

## Commands

```bash
npm run dev           # http://localhost:5173
npm run lint
npm test              # vitest
npm run build
npm run sync-client   # copy clients/typescript/src into src/api-client
```

## API client

`src/api-client/` is a committed copy of `clients/typescript/src` (generated from `api/openapi/v1.yaml`), so the Docker build needs only this directory as context. Run `npm run sync-client` after the API changes. CI runs `npm run check-client`, which fails when the copy is stale.

## Image

```bash
docker build -t eduide-landing-page landing-page   # context: landing-page/
```

Multi-stage: Node build, Go build of `server/`, `gcr.io/distroless/static-debian12:nonroot`. The server listens on `:8080` and serves `/app/dist` with fallback to `index.html`, `/config.json`, `/legal/imprint.md` and `/legal/privacy.md` (from the config directory, 404 when absent), `/healthz`. Responses carry a CSP that allows the Keycloak origin from the config, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. The CSP is derived at start, so a changed Keycloak URL needs a restart.

## License

EPL-2.0 OR GPL-2.0 WITH Classpath-exception-2.0, see `LICENSE`.
