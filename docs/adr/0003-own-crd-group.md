# 0003. Own CRD group, no compatibility with theia.cloud

- Status: Accepted
- Date: 2026-11-18
- Decisions: G6, U6
- Contracts: [api/v1alpha1/groupversion_info.go](../../api/v1alpha1/groupversion_info.go), [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go)

## Context and Problem Statement

The Java stack uses the `theia.cloud` group and its AppDefinition, Workspace and Session kinds.
The new control plane has different concepts (templates with an injection allowlist, pools, workspaces with retention) and must run next to the old stack in the same cluster during migration.
We choose the API group, the kinds and the naming of labels and annotations.

## Decision Drivers

- Parallel operation of both stacks in one cluster ([0017](0017-migration-parallel-run.md)).
- Upstream compatibility with Eclipse Theia Cloud is explicitly not a goal.
- No university-specific names in the repo (U6).

## Considered Options

1. Reuse `theia.cloud` and extend it.
2. A new group on a university domain.
3. A new group `eduide.github.io`.

## Decision Outcome

Chosen option: "a new group `eduide.github.io`".

- CRDs: `idetemplates`, `idepools`, `ideworkspaces`, `idesessions` in `eduide.github.io/v1alpha1`.
- Labels and annotations use the prefix `eduide.github.io/`, defined once in [labels.go](../../api/v1alpha1/labels.go) as a contract between operator, service, authz, pool scaler and charts.
  The scaler annotations `pool-scaler.eduide/...` become `eduide.github.io/scaler-mode` and `eduide.github.io/scaler-override`.
- The CRD-to-API mapping of the old stack is not preserved.
  Migration of data (existing PVCs) is a one-time import ([0007](0007-persistence-modes.md), [0017](0017-migration-parallel-run.md)).
- The repository contains no university hosts, realms or pool labels.
  Hosts, realm and node pool label come from deployment values outside the repo, and examples use `example.org` (U6).

### Consequences

- Good: Both stacks coexist, there is no conflict on kinds or finalizers.
- Good: The group does not depend on any institution.
- Bad: Old clients and tooling for `theia.cloud` do not work and are not supported.
- Follow-ups: The versioning rules for the group are in [0011](0011-crd-versioning-cel.md).

## Pros and Cons of the Options

### Reuse `theia.cloud`

- Good, because existing tools keep working.
- Bad, because both stacks would fight over the same kinds, and the schema cannot change freely.

### University domain

- Good, because the owner is obvious.
- Bad, because it ties the project to one institution.

### `eduide.github.io`

- Good, because it matches the GitHub organization and is neutral.
- Bad, because it is not a domain the project controls beyond GitHub.
