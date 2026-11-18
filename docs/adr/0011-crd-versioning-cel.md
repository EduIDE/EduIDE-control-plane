# 0011. CRD versioning and CEL instead of an admission webhook

- Status: Accepted
- Date: 2026-11-18
- Decisions: G10, U4
- Contracts: [api/v1alpha1](../../api/v1alpha1), [config/crd](../../config/crd), [api/v1alpha1/crd_test.go](../../api/v1alpha1/crd_test.go)

## Context and Problem Statement

The old stack runs a conversion webhook as its own pod with 1.2 GiB of memory, and only the newest CRD version is stored anyway.
We need validation without an admission webhook pod and rules for evolving the CRDs across clusters where several deployments run different operator versions.

## Decision Drivers

- No extra pod per deployment.
- Validation visible in the schema and testable with envtest.
- The CRD chart is installed once per cluster, operators upgrade per deployment (G10).

## Considered Options

1. Validating admission webhook in its own pod.
2. Webhook inside the operator binary.
3. CEL rules in the CRD schema.

## Decision Outcome

Chosen option: "CEL rules in the CRD schema, no webhook".

- Validation lives in `+kubebuilder:validation:XValidation` markers, for example `spec is immutable`, `minIdle must not exceed maxInstances`, `warning must be shorter than idle`, and the `injectableKeys` format rules.
  `crd_test.go` and envtest check the rules against real API servers.
- Types and CRD YAML come from controller-gen (`make generate`). `config/crd` is generated and never edited by hand.
- Versions: `v1alpha1` now, `v1beta1` at the first productive use, `v1` after one stable semester. Additive changes are the default.
- If a conversion is ever needed, the webhook runs inside the operator binary, never as an own pod.
- Mixed versions in one cluster (G10):
  - The CRD chart `eduide-control-plane-crds` is installed per cluster and always first, and it only adds.
  - Operators tolerate unknown fields (server-side apply with their own field manager).
  - A field is removed only when no deployment in the cluster uses it; a CI schema diff guards this (U4).
  - The operator checks the minimum CRD version at start and fails with a clear message.
- API changes inside a version are additive only. Anything else needs an ADR. The skill [add-crd-field](../../.claude/skills/add-crd-field/SKILL.md) holds the steps.
- Immutable fields use `self == oldSelf` rules. The `IDESession` spec is fully immutable.

### Consequences

- Good: No webhook pod, no webhook certificate, no admission latency from our code.
- Good: Rules ship with the schema and run in the API server.
- Bad: CEL cannot express cross-object checks (for example "template allows this persistence mode"). Those checks stay in the service and the operator, and the operator reports them as conditions.
- Bad: CEL cost limits force bounded lists and strings (`MaxItems`, `MaxLength` markers).
- Follow-up: CI includes the CRD schema diff and `make verify-generate`.

## Pros and Cons of the Options

### Webhook pod

- Good, because arbitrary logic.
- Bad, because memory, certificates and a failure mode for every write.

### Webhook in the operator

- Good, because no extra pod.
- Bad, because writes fail when the operator is down.

### CEL

- Good, because declarative, in-process of the API server.
- Bad, because limited expressiveness.
