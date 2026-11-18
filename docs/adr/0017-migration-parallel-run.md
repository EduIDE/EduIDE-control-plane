# 0017. Migration by parallel run per deployment

- Status: Accepted
- Date: 2026-11-18
- Decisions: G6, G12, G14, G19, U9, U10
- Contracts: [api/v1alpha1/ideworkspace_types.go](../../api/v1alpha1/ideworkspace_types.go) (`existingClaimName`), [api/v1alpha1/groupversion_info.go](../../api/v1alpha1/groupversion_info.go)

## Context and Problem Statement

The Java stack runs productive deployments for courses and exams.
The new control plane replaces it, and no deployment may lose sessions or workspaces or move during an exam period.

## Decision Drivers

- Each deployment can go back to the old stack.
- No downtime for running exams.
- Existing persistent workspaces survive.

## Considered Options

1. Big-bang switch of all deployments.
2. In-place conversion of the old CRs.
3. Parallel run per deployment with a switch in the landing page.

## Decision Outcome

Chosen option: "parallel run per deployment".

- Both stacks run side by side in a cluster, which is possible because the CRD group differs ([0003](0003-own-crd-group.md)). The landing page switches per deployment with a feature flag and uses the new API client.
- Order (G12): test deployments, then staging, then the pilot deployments, then production. Progress follows the exit criteria of the phases, not a calendar.
- Blackout: no productive deployment moves during an exam period.
- The way back to the Java stack stays open per deployment until the end of the following month. Until then the Java stack gets only security fixes and the immediate measures.
- Workspaces (G19): the `volume` mode imports existing PVCs through an import job that creates an `IDEWorkspace` with `existingClaimName` and sets `status.lastUsed` to the import time, so imported data is not deleted at once.
- Session data is not migrated: sessions are short-lived. Running old sessions end on the old stack.
- Test environment (U9, U10): the first target is a dedicated test namespace with its own gateway on the shared cluster. The Keycloak client of the existing test deployments is reused, and its secret is copied by pipe without printing. The agreed scope is in the plan; everything else in the cluster stays untouched. The Keycloak redirect URIs and the cookie domain cover the instance and webview host ([0005](0005-session-auth-at-gateway.md)).
- When all deployments run on the new stack, the Java stack and the workspace garbage collector are switched off, and the old repository is archived.
- Migration docs and runbooks live in `docs/operations/`.

### Consequences

- Good: Small, reversible steps. Real data validates the new stack before exams.
- Bad: Two stacks cost memory and operational attention for a while.
- Bad: Cluster-side work (CRDs, gateway, certificates) needs explicit approval for each cluster.
- Follow-up: A smoke test after each deployment includes the NetworkPolicy negative test ([0019](0019-network-isolation.md)).

## Pros and Cons of the Options

### Big bang

- Good, because one switch.
- Bad, because all tenants share the risk.

### In-place conversion

- Good, because no parallel stacks.
- Bad, because the schemas differ and there is no rollback.

### Parallel run

- Good, because reversible, per tenant.
- Bad, because temporary duplication.
