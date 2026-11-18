# 0020. Start estimates, image metadata, lifecycle times and one-call delete in the API

- Status: Accepted
- Date: 2026-11-18
- Decisions: U11, U12, U13, U14 (numbers from [decisions.md](decisions.md))
- Contracts: [api/openapi/v1.yaml](../../api/openapi/v1.yaml), [api/v1alpha1/idetemplate_types.go](../../api/v1alpha1/idetemplate_types.go)

## Context and Problem Statement

The redesigned landing page (U15) shows how long a start takes, what an IDE brings along, when a session stops, when a workspace is deleted, and deletes entries in one step.
The landing page must not compute timeouts, keep template texts or chain two calls itself.
The values have to come from the control plane, as additive changes within `v1` and `v1alpha1` ([0010](0010-rest-api-spec-first.md), [0011](0011-crd-versioning-cel.md)).

## Decision Drivers

- One place for time zones, template overrides and defaults.
- What an IDE contains belongs to the EduIDE version, so it travels with the image.
- No new stored state in the service; it reads from the cache.
- No breaking change.

## Considered Options

1. The landing page computes timeouts and keeps template texts.
2. The service computes everything on request and the operator reads image metadata from the registry.
3. A separate metadata service.

## Decision Outcome

Chosen option: "The service computes on request, the operator reads the image".

### U13: lifecycle times

- `Session.stopsAt` and `Session.stopReason` (`idle`, `max`): the earlier of the idle end and the maximum lifetime.
  The idle end is the later of the claim or agent ready time and `lastActivity`, plus the template idle timeout (default 60 min).
  The maximum end is `createdAt` plus the template maximum timeout (default 24 h).
  Absent before the claim and for failed or deleting sessions.
- `Workspace.deletesAt`: when workspace and volume are actually deleted, `expiredAt` (else `expiresAt`) plus `workspaces.grace`.
  Absent while a session runs on the workspace and with the retain annotation.
  `expiresAt` stays.
- `OverviewItem.stopsAt` and `OverviewItem.deletesAt` repeat them for the item. Session events and overview events carry the values because they carry the objects.
- The service uses the same defaults and base time as the operator ([0016](0016-activity-detection.md)); the defaults live in `api/v1alpha1/helpers.go`.

### U11: start estimates

- The service keeps a ring buffer of 512 samples per template, in memory per replica. A sample is created when the session informer sees a session become Ready: `creationTimestamp` to `status.timestamps.ready`, with the start strategy and whether the session has a workspace.
- `Template.startEstimate` is `{none?, volume?}` with `{seconds, strategy, samples}` per allowed persistence mode. A mode without data is absent, and so is the whole object.
- `none`: median of warm starts (`warm-ready`) if the pool of the template has `status.idleReady > 0` now, else median of the other starts without workspace (strategy `cold`). `volume`: median of starts with a workspace; they never claim from a pool, so the strategy is `cold`.
- Window: the last hour; when it has no sample, the last 24 h with at least 3 samples.
- `GET /templates/events` (SSE, event `templates`) sends the full list, starts with the current state, sends at most one event per 5 s and a `: ping` every 15 s. It needs the same bearer token as the other endpoints.

### U12: image metadata

- The image build writes the OCI annotation `io.eduide.template` (JSON) on the manifest or image index: `imageVersion`, `variants` (`id`, `name`, `description`, `logoUrl`), `includes` (`group`, `items` with `name`, `version`).
- The operator resolves the tag to a digest (cheap HEAD request) at most every 10 minutes per image reference and reads the annotation once per digest. No running pod is needed. It uses `go-containerregistry` with anonymous pulls. Private registries are not supported yet: the template gets the condition below.
- The value is validated and capped at 64 KiB: variant ids match `^[a-z0-9][a-z0-9._-]{0,63}$` and are unique, logo URLs are http(s) without credentials, no control characters, at most 32 variants, 32 groups and 256 items per group.
- Result in `IDETemplate.status.image` (`digest`, `version`, `variants`, `includes`, `lastChecked`), written only when the content changes. `lastChecked` is the time of the last write of the block.
- Condition `ImageMetadata`: `True` (`Read`), or `False` with `AnnotationMissing`, `AnnotationInvalid` or `RegistryError`. It never affects `Ready`. A registry error keeps the last good metadata; a missing or invalid annotation of a new digest clears it.
- `GET /templates` returns `variants`, `includes` and `imageVersion`.
- The variant chosen at launch goes as launch data `TEMPLATE`. A template key `TEMPLATE` without `values` accepts the variant ids of the image.

### U14: delete in one call

`DELETE /workspaces/{name}` deletes the sessions on the workspace, waits until they are gone (at most 30 s, 500 `internal` on timeout and the workspace stays), then deletes the workspace and answers 204.
The code `workspace-in-use` is no longer sent. It stays in the enum for compatibility and is documented as unused.

### Consequences

- Good: the landing page shows service-computed times and metadata and needs no extra calls for delete.
- Good: all changes are additive.
- Bad: start estimates are per replica and start empty after a restart until sessions become Ready (a restart replays the informer, so recent sessions refill it).
- Bad: private registries show no image metadata.
- Follow-ups: the image build in EduIDE writes the annotation; the landing page uses the new fields (U15).

## Pros and Cons of the Options

### The landing page computes

- Bad, because it duplicates timeout defaults and template overrides.

### The service computes, the operator reads the image

- Good, because it uses data the control plane already has.
- Bad, because the estimate is not shared between replicas.

### A separate metadata service

- Bad, because it adds a component for little data.
