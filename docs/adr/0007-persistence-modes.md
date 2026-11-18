# 0007. Persistence modes `none` and `volume`, matrix with prewarming, `snapshot` as later extension

- Status: Accepted
- Date: 2026-11-18
- Decisions: G14, G19
- Contracts: [api/v1alpha1/ideworkspace_types.go](../../api/v1alpha1/ideworkspace_types.go), [api/v1alpha1/idetemplate_types.go](../../api/v1alpha1/idetemplate_types.go), [internal/config/config.go](../../internal/config/config.go) (`Workspaces`)

## Context and Problem Statement

EduIDE needs four variants: with or without persistence, with or without prewarming.
Today they exclude each other: the eager path has no workspaces, and persistent workspaces work only on the lazy path.
A volume cannot be attached to a running pod, so a prewarmed pod cannot receive a persistent workspace.

## Decision Drivers

- Cover the four variants EduIDE needs without a new storage system.
- `volume` must work on every cluster (Ceph RBD, Longhorn, local-path).
- Workspaces must not vanish while in use, and expiry must follow last use, not creation (G19).

## Considered Options

1. Prewarmed pods with late volume attach (not possible in Kubernetes).
2. `none` and `volume` now, `snapshot` later behind the same enum.
3. Object store snapshots from the start.

## Decision Outcome

Chosen option: "`none` and `volume` now, `snapshot` later".

| | without prewarming | with prewarming |
|---|---|---|
| `none` | cold start, `emptyDir` | claim an idle pod, `emptyDir` |
| `volume` | create or reuse the PVC, cold start | cold start; the pool serves only sessions without persistence, `status.startStrategy` is `cold` |

- `IDEWorkspace.spec.persistence.mode` is an enum with only `volume` today. It is immutable. Adding `snapshot` is an additive enum change ([0011](0011-crd-versioning-cel.md)).
- A session without `workspaceRef` has no persistence (`emptyDir`). A template lists allowed and default modes.
- `existingClaimName` imports an existing PVC for the migration. Import sets `status.lastUsed` to the import time ([0017](0017-migration-parallel-run.md)).
- Launch is idempotent (G14): the key is template plus workspace, and for sessions without persistence template plus a hash of the start data. Stopping always discards the pod; `volume` workspaces stay.
- Workspace expiry (G19), done by the operator ([0002](0002-go-single-module-four-binaries.md)), replaces the old garbage collector:
  - `status.lastUsed` is set at the start and the end of every session. A workspace with a running session never expires.
  - `retention` defaults to 14 days per deployment, overridable per template and per workspace.
  - Two stages: phase `Expired`, then after the grace period (24 h) PVC and CR are deleted. The annotation `eduide.github.io/retain=true` cancels expiry.
  - The overview warns three days before expiry, and starting the workspace extends it ([0010](0010-rest-api-spec-first.md)).
- The session finalizer records the workspace usage at the end of a session.
- `snapshot` (phase 9, optional, lowest priority) would let the agent load a tar+zstd archive from an S3-compatible store into an `emptyDir` on claim and save it at the end and periodically. It needs an object store, which no cluster has today. Nothing but the extensible enum and the agent claim protocol is prepared now.

### Consequences

- Good: All four variants work with storage every cluster has.
- Good: Expiry follows last use and cannot delete in-use data.
- Bad: Persistent sessions always start cold until `snapshot` exists. Image prepull and early route creation keep cold starts short ([0004](0004-gateway-api-only.md)).
- Follow-ups: e2e covers all four matrix cells ([0015](0015-test-strategy.md)); `snapshot` needs its own ADR when built.

## Pros and Cons of the Options

### Late volume attach

- Good, because it would give persistent and warm.
- Bad, because Kubernetes does not support it.

### `none` and `volume`, `snapshot` later

- Good, because it ships with existing storage and stays additive.
- Bad, because of the cold start for persistent sessions.

### Snapshots from the start

- Good, because it solves persistent and warm.
- Bad, because it needs an object store and risks losing changes on hard pod loss.
