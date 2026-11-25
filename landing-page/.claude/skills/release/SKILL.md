---
name: release
description: Release the landing page. Use when asked to release, tag or ship a version that includes the landing page. The landing page has no release of its own: it ships with the control plane tag.
---

# Releasing the landing page

The landing page is released together with the control plane: one tag `vX.Y.Z` versions the `landing-page` image, the `eduide-landing-page` chart and everything else in this repository.
There is no separate repository, release workflow or version file for it.

Follow [docs/development/release.md](../../../../docs/development/release.md) and the skill in [.claude/skills/release/SKILL.md](../../../../.claude/skills/release/SKILL.md) at the repository root.

Landing page specifics:

- Keep `landing-page/CHANGELOG.md` current under `## [Unreleased]`. It is the history of the landing page; the repository `CHANGELOG.md` is the source of the release notes.
- Run `npm run lint`, `npm test`, `npm run build` and `cd server && go test ./...` in `landing-page/` before the release.
- Set `version` and `appVersion` of `charts/eduide-landing-page` to the release version.
