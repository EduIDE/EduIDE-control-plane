# Changelog

All notable changes are documented in this file.
The section of a version becomes the notes of its GitHub Release.

## [Unreleased]

### Changes

- The deprecated `?artemisToken=` and a `?gitUri=` with credentials leave the address bar before the login, like the fragment forms; they no longer travel to Keycloak inside the redirect URI
- A repository URL with a query or a fragment no longer makes the launch fail: the display source drops both
- New message for `workspace-limit-reached`: the service limits the saved workspaces per user (`workspaces.perUser`)
- The landing page runs on the control plane REST API (`@eduide/control-plane-client`) instead of Theia Cloud; sessions are followed through server-sent events
- New home page: your environments on top (also at `/me/overview`), new environments as tiles below, with start time, saved or temporary workspace, what an image includes and project templates; English and German; no Artemis wording; colors follow the system setting (the theme switch is gone)
- The Artemis token is read from the URL fragment (`#artemisToken=`); `?artemisToken` is deprecated
- The repository URL is read from the URL fragment too (`#gitUri=`, Artemis#14702), because it can carry VCS credentials: it moves into sessionStorage before the login and is removed after the launch; `?gitUri` still works and warns when it carries credentials
- A refused repository host names the host; other invalid launch values show the key and rule the service reports (never the value)
- Runtime config is `/config.json`, served by a small Go static server that replaces nginx
- Imprint and privacy are mandatory: `config.json` needs `legal.imprint` and `legal.privacy` (an external `url` or a hosted Markdown `path`), the chart needs `legal.imprint` and `legal.privacy` values. `legal.imprintUrl` and `legal.privacyUrl` are removed. `/imprint` and `/privacy` render the Markdown without a login
- New public status page at `/status` (no login) and read-only admin panel at `/admin` (lazy loaded, needs the admin role); the footer links to `/status`
- The status page lists every template (a warm template is instant for temporary workspaces even without an estimate), and the connection banner appears only after about 10 s without a stream
- The environment list retries `/templates` with a growing pause until it works (and asks again when the event stream comes back), so the tiles appear after a service outage without a reload
- Language and build tool logos are part of the bundle (`src/assets/logos`, resolved by `src/logos.ts`); an API `logoUrl` counts only as a same-origin path, absolute addresses are ignored. The logo files moved out of `public/`
- The "Admin" menu entry is looked up when the avatar menu first opens, not on every page load
- `footerLinks.attribution.text` and `.url` are no longer used; the footer shows "EduIDE" plus `attribution.version`
- The footer is the same on every page (GitHub, bug report, imprint, privacy) and sits at the bottom of the viewport; `footerLinks.github` is new
- The browser tab title comes from `config.title` (index.html says "EduIDE" until then)
- Removed: Sentry, anonymous mode, `/session-ended` resume, git link checks (the service validates launch data)

## [1.5.1] - 2026-11-18

### Fixes

- Resume of a stored external Git link launches with the parsed URL, like a fresh link. Before, it passed the stored string on, so a link such as `https://github.com\@evil.example/x.git` that the host check read as github.com could reach git unparsed ([#78](https://github.com/EduIDE/EduIDE-Landing-Page/pull/78))

## [1.5.0] - 2026-11-18

### Features

- Open a GitHub, GitLab or Gitea/Forgejo repository from a link: `/?appDef=<app>&gitUri=https://<host>/<path>.git&gitRef=<ref>`. The page asks for one click naming the repository, passes `GIT_REF`, and sets `GIT_SOURCE` itself (`artemis` or `external`); each `gitRef` gets its own workspace. Alpha: on only while `allowedGitHosts` lists a host ([#50](https://github.com/EduIDE/EduIDE-Landing-Page/pull/50))

### Upgrade notes

- A link with a `gitUri` is now either a trusted Artemis link or an external one. An external link is checked (HTTPS, host in `allowedGitHosts` or `trustedArtemisHosts`, no credentials) and passes on only `gitUri` and `gitRef`. Artemis links only work when the hosts of `gitUri` and `artemisUrl` are in `trustedArtemisHosts`; installations whose Artemis links come from other hosts now get refused or lose their Artemis parameters. Add those hosts to `trustedArtemisHosts` in the chart. While `allowedGitHosts` is empty or missing (git links off, the chart default), this does not apply: a link that is not a trusted Artemis link works as in 1.4.x when it carries an `artemisToken` (a click, every parameter but `gitRef` passed on, no `GIT_SOURCE`), and is refused without one ([#50](https://github.com/EduIDE/EduIDE-Landing-Page/pull/50))

## [1.4.1] - 2026-11-16

### Fixes

- The "Files are lost when the session ends." / "Files are kept between sessions." hint on the launch and progress screens is readable in dark mode again; it used the browser's default black text ([#73](https://github.com/EduIDE/EduIDE-Landing-Page/pull/73))

## [1.4.0] - 2026-11-16

### Features

- Persistence is now set per app (`ephemeral` in the app config, falling back to `useEphemeralStorage`), and every app tile, the launch screen and the progress screen show a "Temporary" or "Saved" badge so users know whether their files are kept ([#66](https://github.com/EduIDE/EduIDE-Landing-Page/pull/66))
- Go logo for Go apps ([#66](https://github.com/EduIDE/EduIDE-Landing-Page/pull/66))
- A reloaded tab of an ended session now explains that the session ended and offers a one-click "Resume my session" (same workspace, same volume), or a fresh start after an ephemeral session ([#46](https://github.com/EduIDE/EduIDE-Landing-Page/pull/46))
- A 553 answer now says the previous session is still shutting down ([#46](https://github.com/EduIDE/EduIDE-Landing-Page/pull/46))

### Fixes

- Logging out no longer fails with Keycloak's "Invalid redirect uri" ([#64](https://github.com/EduIDE/EduIDE-Landing-Page/pull/64))
- A launch that returns no session URL is retried instead of redirecting to an empty address ("'https:' is not a valid URL") ([#68](https://github.com/EduIDE/EduIDE-Landing-Page/pull/68))

### Upgrade notes

- Template launches no longer become persistent implicitly. The old rule (persistent whenever a build system is chosen) is gone, so apps that must keep their files, such as the TUM `-standalone` apps, need `ephemeral: false` in the chart. Pass `ephemeral` per app through the EduIDE-Helm chart in the same chart release that bumps `versions.landingPage` to 1.4.0.
- The new "session ended" page relies on EduIDE-Helm redirecting ended-session requests to `/session-ended`.
