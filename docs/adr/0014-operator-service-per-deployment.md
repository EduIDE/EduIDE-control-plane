# 0014. One operator and one service per deployment

- Status: Accepted
- Date: 2026-11-18
- Decisions: G5, G10, U6, U7
- Contracts: [internal/config/config.go](../../internal/config/config.go), [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go) (`LabelManagedBy`)

## Context and Problem Statement

EduIDE runs several deployments (one per tenant and environment), each in its own namespace with its own Keycloak realm, hosts and capacity.
We decide whether one control plane serves all of them or each deployment brings its own.

## Decision Drivers

- Isolation between tenants: a fault or an attack in one deployment stays there.
- Separate release timing per deployment, in particular around exam periods.
- Simple RBAC: namespace-scoped, no cluster-wide secrets access.
- Memory stays small enough per deployment (target below 150 MiB).

## Considered Options

1. One shared control plane per cluster for all tenants.
2. One operator, one service and one authz per deployment (namespace).

## Decision Outcome

Chosen option: "one set per deployment".

- Each deployment (a namespace) runs its own `operator` (one active replica, leader election), `service` (2 or more replicas), `authz` (2 replicas, PodDisruptionBudget) and, per session, the `agent` sidecar. There is no sharing between deployments.
- Everything is scoped to the namespace: informers use the own namespace and the label `app.kubernetes.io/managed-by=eduide`. RBAC is namespaced and minimal. Session pods have no service account token.
- The deployment-wide settings (hosts, gateway reference, OIDC, placement, capacity, telemetry, rate limit) come from one typed config file in a ConfigMap ([internal/config/config.go](../../internal/config/config.go)).
- CRDs are cluster-wide and shared. The CRD chart is installed per cluster and first, the control plane chart per deployment ([0018](0018-charts-oci.md), [0011](0011-crd-versioning-cel.md)).
- The deployment names its own gateway section and SecurityPolicy ([0005](0005-session-auth-at-gateway.md)). Keycloak realm, hosts and node pool label are deployment values, not repo defaults (U6).
- Node pools (U7) are a placement profile per deployment, overridable per template. Capacity pressure is evaluated per node pool ([0008](0008-idepool-scale-bare-pods.md)).
- authz is its own Deployment with its own informer for sessions only, so it fails and releases separately from the service (G5).

### Consequences

- Good: Strong isolation, per-tenant upgrades and rollbacks, no cross-tenant cache or lock.
- Good: Operators need no cluster-wide permissions beyond reading CRDs.
- Bad: More small pods in total (about four per deployment). The memory target is per deployment so it holds.
- Bad: Operators of different versions run side by side, so CRD changes must stay additive.
- Follow-up: The chart sets resource requests and limits and a PDB for authz and service.

## Pros and Cons of the Options

### Shared control plane

- Good, because the fewest pods.
- Bad, because a larger blast radius, cluster-wide RBAC and shared release timing.

### One set per deployment

- Good, because isolation and independent releases.
- Bad, because duplication of small processes.
