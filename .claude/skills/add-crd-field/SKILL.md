---
name: add-crd-field
description: Use when adding or changing a field, enum value or validation rule on an IDETemplate, IDEPool, IDEWorkspace or IDESession CRD in api/v1alpha1, including CEL rules, codegen, tests and docs.
---

# Add a CRD field

Rules:
- Edit only the Go types in `api/v1alpha1/*_types.go`. `zz_generated.*` and `config/crd/*.yaml` are generated and never edited by hand.
- Inside a version only additive changes: new optional fields, new enum values, looser limits. Renaming, removing, retyping, making a field required or tightening a rule needs an ADR first (see write-adr) and usually a new version ([ADR 0011](../../../docs/adr/0011-crd-versioning-cel.md)).
- Every change comes with tests. Docs change in the same change.
- Every local cluster command uses an isolated `KUBECONFIG` (see run-e2e). Never run kubectl against the default context.

Steps:
1. Check that the change is additive. If not, stop and write an ADR.
2. Add the field to the type with `json` tag, `+optional` or a `+kubebuilder:default`, and validation markers (`MinLength`, `Maximum`, `Enum`, `MaxItems`). Bound every string and list so CEL cost limits hold.
3. Add CEL rules as `+kubebuilder:validation:XValidation` with a clear `message`. Use `self == oldSelf` for immutable fields.
4. Use labels and annotations only through `api/v1alpha1/labels.go`. A new one is added there.
5. Run `make generate`. It rewrites deepcopy code, `config/crd/` and the chart CRDs.
6. Add tests: unit or table tests in `api/v1alpha1/crd_test.go` (valid and invalid objects, CEL messages), and an envtest case in the controller that uses the field.
7. Use the field in the operator or service. New logic needs its own tests.
8. Update docs: the CRD reference is generated, the architecture or data model page in `docs/architecture/` and `CHANGELOG.md` by hand. If the field changes the REST API, follow add-api-endpoint.
9. Run `make test`, `make lint` and `make verify-generate`. All must pass with no diff after generate.
10. Do not commit without asking.
