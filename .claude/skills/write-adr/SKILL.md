---
name: write-adr
description: Use when a decision shapes architecture, a contract (CRD, REST API, agent protocol, metrics) or breaks a versioning rule, and needs an ADR. Covers template, numbering and linking.
---

# Write an ADR

Rules:
- One decision per ADR, MADR format, files in `docs/adr/`. Details in [ADR 0001](../../../docs/adr/0001-adr-madr.md).
- Accepted ADRs are not rewritten. A changed decision gets a new ADR, and the old one gets the status "Superseded by NNNN".
- Needed before: a breaking API or CRD change, a renamed or removed metric, a new binary or dependency of weight, a change to auth or isolation.
- Style: English, active voice, present tense, hyphens instead of dashes, 40 to 120 lines, no university-specific hosts (use `example.org`).

Steps:
1. Find the next number: `ls docs/adr/` and take the highest plus one. No gaps.
2. Copy `docs/adr/0000-template.md` to `docs/adr/NNNN-short-title.md`.
3. Fill the header: status (`Proposed` while under review, `Accepted` once agreed), date, the G and U numbers from `docs/adr/decisions.md` it builds on, and links to the contract files it shapes (`api/v1alpha1`, `api/openapi/v1.yaml`, `api/agent/v1.yaml`, `docs/contracts/metrics.md`, `internal/config/config.go`).
4. Write Context, Decision Drivers, at least two Considered Options, Decision Outcome and Consequences (good, bad, follow-ups). Name the follow-ups that change code, docs or other ADRs.
5. Add a row to the table in `docs/adr/README.md`. If the ADR implements a G or U row, add its link in `docs/adr/decisions.md`.
6. Link the ADR from the code or docs that depend on it (type comments, docs pages).
7. When it supersedes an ADR, set the old status to "Superseded by NNNN" and nothing else.
8. Check the links, and that the text has no em or en dashes. Do not commit without asking.
