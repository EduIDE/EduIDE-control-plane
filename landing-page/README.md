# EduIDE Landing Page

> **Experimental.** Part of the EduIDE control plane. Interfaces, configuration and behavior can change without notice.

The page a student lands on: it starts an IDE session through the control plane REST API (`/api/v1`) and lists the user's running sessions and saved workspaces. A Vite and React app, served by a small Go static server.

## Features

- Launch from an Artemis link (`?appDef=<template>&gitUri=...`) or pick an environment
- Progress through the session event stream (SSE); redirect when the session is Ready, error by problem code when it fails (`capacity-exhausted`, `session-limit-reached` with the list of running sessions)
- Overview at `/me/overview`: running sessions (Open, Stop), workspaces without session (Start, Delete with confirmation), a warning 3 days before a workspace expires, live updates

## Link parameters

| Where | Name | Meaning |
|---|---|---|
| query | `appDef` | template name |
| query | `gitUri`, `gitRef`, `artemisUrl` | launch data (only keys the template allows are sent) |
| query | `title` | optional title shown in the overview |
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

Optional keys: `hstsIncludeSubdomains` (default `true`, adds `includeSubDomains` to HSTS; set `false` when the session hosts are not subdomains of the landing host), `footerLinks` (attribution, bugReport, featureRequest, about) and `legal` (`imprintUrl`, `privacyUrl`: http(s) URLs of the operator's own legal pages; the footer links "Imprint" and "Privacy" appear only when set). `legacy` is reserved and not used yet.

For local development copy that file to `public/config.json` (git-ignored) and run `npm run dev`.

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

Multi-stage: Node build, Go build of `server/`, `gcr.io/distroless/static-debian12:nonroot`. The server listens on `:8080` and serves `/app/dist` with fallback to `index.html`, `/config.json`, `/healthz`. Responses carry a CSP that allows the Keycloak origin from the config, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. The CSP is derived at start, so a changed Keycloak URL needs a restart.

## License

EPL-2.0 OR GPL-2.0 WITH Classpath-exception-2.0, see `LICENSE`.
