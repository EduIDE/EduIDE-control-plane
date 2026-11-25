# 0023. Central feedback receiver in its own chart

- Status: Proposed
- Date: 2026-11-25
- Decisions: none (plan `2026-11-13-feedback-channels`, step "Receiver")
- Contracts: [cmd/feedback](../../cmd/feedback/main.go), [charts/eduide-feedback-receiver](../../charts/eduide-feedback-receiver/values.yaml)

## Context and Problem Statement

Users of every installation can send feedback from the IDE.
The services of the installations forward it with a per-installation key.
Something central must turn it into issues in one triage repository (`EduIDE/feedback-inbox`) without handing a GitHub credential to every installation.
Which component does this, and where does it run?

## Decision Drivers

- No GitHub credential in any installation: a compromised installation must not read or write the triage repository.
- A GitHub App with only Issues read/write on one repository, not a personal access token.
- No database and no user data; the receiver forgets everything after the request.
- It runs once, on one cluster, and is off everywhere else.
- Small: one binary, no new dependency.

## Considered Options

1. Every service creates the issue itself with a shared token.
2. A new binary `cmd/feedback`, deployed as an optional component of the `eduide-control-plane` chart.
3. A new binary `cmd/feedback`, deployed by its own small chart `eduide-feedback-receiver`.

## Decision Outcome

Chosen option: "A new binary and its own chart".

- `POST /v1/feedback` checks `Authorization: Bearer <key>` in constant time against the key of the named installation, validates lengths again, limits to 60 requests per hour and installation (in memory), and creates the issue through the GitHub App.
- Unknown installation and wrong key both answer 401, so the answer does not reveal which installations exist.
- The app JWT (RS256) is signed with the standard library. The installation token is cached until five minutes before it expires. No new dependency (`golang.org/x/time` is already used by the service).
- Keys come from a mounted Secret with one key per installation name. The receiver reads the file per request, so a new installation needs no restart.
- Issue: title `[<installation>] <title>`, description and a table of the technical context, labels `via-ide`, `needs-triage`, `bug` or `enhancement`. Nothing else leaves the receiver, and the answer carries no issue data.
- Logs hold installation, kind, result and the issue number, never text.
- Answers: 202, 400, 401, 429 (with `Retry-After`), 502.

### Why its own chart

The `eduide-control-plane` chart is one release per installation and refuses to render without OIDC, hosts and templates.
The receiver belongs to no installation: it exists once, on the TUM AET cluster, and its Secrets (GitHub App key, keys of all installations) must not appear in the namespace of an installation.
An optional component in the control plane chart would drag in those required values and tie the receiver's lifecycle to one installation, which the pool scaler accepts only because it works on that installation's pools.
The landing page chart is the precedent for a component that is not part of an installation's control plane: small, own `values.schema.json`, own `ci` values, released by the same tag ([0018](0018-charts-oci.md)).
The chart keeps a switch `enabled: false`; nothing is rendered until it is true, and it fails without the required values.

### Network

As in [0019](0019-network-isolation.md): ingress only from the Envoy pods of the gateway on port 8080, no other pod. Egress only to DNS and TCP 443 outside the private and link-local ranges, because the receiver talks to GitHub only. There is no metrics port, so no monitoring rule.

### Consequences

- Good: Installations hold only their own revocable key. Revoking one means deleting one Secret key.
- Good: One credential for GitHub, scoped to one repository.
- Good: Off by default and independent of every installation.
- Bad: One more image, chart and Secret set to maintain. A GitHub outage drops feedback after the service answered 502; there is no queue by design.
- Bad: The rate limit is per replica and lost on restart. One replica is the default.
- Follow-up: A maintainer creates the GitHub App and the triage repository ([operations](../operations/feedback-receiver.md)). The per-installation intake in the service is a separate change.

## Pros and Cons of the Options

### Every service creates the issue

- Good, because no central component.
- Bad, because a GitHub credential in every installation and no common rate limit.

### Optional component of the control plane chart

- Good, because one chart less.
- Bad, because required values of an installation block it, and its Secrets sit next to an installation.

### Own chart

- Good, because it matches its lifecycle: once, central.
- Bad, because one more chart in the release.
