# 0001. Architecture decisions as ADRs (MADR)

- Status: Accepted
- Date: 2026-11-18
- Decisions: G13, U2, U3
- Contracts: [README.md](README.md), [0000-template.md](0000-template.md), [decisions.md](decisions.md), [write-adr skill](../../.claude/skills/write-adr/SKILL.md)

## Context and Problem Statement

The control plane is built by Claude agents working in parallel work packages against fixed contracts (G13).
Humans and agents need a durable record of why the contracts look the way they do, so later changes do not silently undo a decision.
The grilling session of 2026-11-18 produced 19 decisions (G1 to G19) and 10 implementation decisions (U1 to U10) that need a home in the repo.

## Decision Drivers

- Agents start every session without memory, so the reasons must live in the repo.
- Changes to API and CRDs need a place to argue for breaking changes.
- Low ceremony: one file per decision, reviewed in the same pull request as the code.

## Considered Options

1. No written decisions, the plan document is enough.
2. One long architecture document.
3. Numbered ADRs in MADR format under `docs/adr/`.

## Decision Outcome

Chosen option: "numbered ADRs in MADR format".

- Every file is `docs/adr/NNNN-short-title.md`, numbered without gaps, never renumbered.
- The sections are Context and Problem Statement, Decision Drivers, Considered Options, Decision Outcome and Consequences, as in [0000-template.md](0000-template.md).
- An accepted ADR is not edited except for typos and links.
  A changed decision gets a new ADR that sets the old one to "Superseded by".
- Each ADR names the G or U numbers it builds on and links the contract files it shapes.
- [decisions.md](decisions.md) holds the full G and U tables and links each row to the implementing ADRs.
- [README.md](README.md) is the index. A new ADR adds one row to the index and, when it implements a G or U row, to `decisions.md`.
- The [write-adr skill](../../.claude/skills/write-adr/SKILL.md) describes the steps for agents.
- A change to an API or a CRD that breaks the versioning rules needs an ADR first ([0010](0010-rest-api-spec-first.md), [0011](0011-crd-versioning-cel.md)).

### Consequences

- Good: Decisions are findable, reviewable and linkable from code and pull requests.
- Good: Agents get the reasoning with the contracts, not only the result.
- Bad: Small overhead per decision. Trivial choices stay out of ADRs.
- Follow-ups: CI may later check that the index lists every ADR file.

## Pros and Cons of the Options

### Plan document only

- Good, because it exists already.
- Bad, because it lives outside the repo and mixes plan and decisions.

### One long document

- Good, because it reads linearly.
- Bad, because supersession and review per decision are awkward.

### Numbered ADRs

- Good, because each decision has its own history and status.
- Bad, because the reader assembles the overview from many files, which the index mitigates.
