# 0012. User data only in Secrets, allowlist for injectable keys

- Status: Accepted
- Date: 2026-11-18
- Decisions: G16, G18
- Contracts: [api/v1alpha1/idetemplate_types.go](../../api/v1alpha1/idetemplate_types.go) (`InjectableKey`, `KeyFormat`, `KeySource`), [api/v1alpha1/idesession_types.go](../../api/v1alpha1/idesession_types.go) (`dataSecretRef`), [api/agent/v1.yaml](../../api/agent/v1.yaml) (`ClaimRequest`)

## Context and Problem Statement

A launch carries user data such as the Artemis URL and token and the Git URL.
In the old stack the request can name `fromSecrets` and `fromConfigMaps`, and an attacker used that path to read secrets of the cluster (the Bonn incident).
Data also showed up in places that outlive the session.

## Decision Drivers

- No way to make the control plane read arbitrary secrets or config maps.
- Secrets in CRs, logs, traces, metrics or status are leaks.
- Per-key validation, since values end up in a developer environment.

## Considered Options

1. Free-form key-value injection with a denylist.
2. Allowlist per template with a validation rule per key.

## Decision Outcome

Chosen option: "allowlist per template".

- The operator enforces the list again before it pushes the claim to the agent: keys outside `injectableKeys` are dropped and values are cut to `maxLength`. It logs the key names, never values.
- `IDETemplate.spec.injectableKeys` lists the permitted keys. Each has a source and a format:
  - `source: Request` takes the value from the request and needs a `format`: `HTTPSURL`, `Token`, `Enum` (with `values`) or `Pattern` (with `pattern`), plus `maxLength` (default 2048, at most 8192) and `required`.
  - `source: UserName` or `UserEmail` takes the value from the verified token, not from the request.
- Rules from G18: `ARTEMIS_URL` and `GIT_URI` only over `https` and only for hosts in the deployment's `allowedHosts`. `GIT_USER` and `GIT_MAIL` come from the token. `ARTEMIS_TOKEN` has length and character limits. `TEMPLATE` accepts only the build systems of the template. `THEIA` is static env in the template.
- Unknown keys give HTTP 400 with the code `unknown-key`. There is no `fromSecrets` and no `fromConfigMaps`. The payload is at most 8 KiB per launch.
- The service writes one Secret per session and sets `spec.dataSecretRef`. The Secret has an OwnerReference to the session, so Kubernetes deletes it with the session.
- The operator copies the Secret data once into the claim request ([0006](0006-agent-sidecar.md)). The agent keeps it in memory only.
- User data never appears in the CR spec (only non-secret `display.title` and `display.source`), logs, traces, metrics or status.
  An e2e test greps logs and spans for the token ([0015](0015-test-strategy.md)). Spans carry a salted user id, not names or e-mail addresses ([0013](0013-observability.md)).
- The Artemis token reaches the service in the request body after the landing page read it from the URL fragment (G16, [0010](0010-rest-api-spec-first.md)).

### Consequences

- Good: The Bonn attack path does not exist. Validation is declarative and in the template.
- Good: One place to see what a template accepts.
- Bad: Every new data key needs a template change. That is intended.
- Bad: The Secret exists in etcd for the life of the session. Encryption at rest is a cluster setting.
- Follow-up: Fuzz the key validation and payload parsing.

## Pros and Cons of the Options

### Denylist

- Good, because flexible.
- Bad, because every new route to a secret is a new bypass.

### Allowlist

- Good, because closed by default.
- Bad, because stricter for template authors.
