# 0002. Go, controller-runtime, one module, four binaries with their own images

- Status: Accepted
- Date: 2026-11-18
- Decisions: G5, G11, U1, U2, U3, U5
- Contracts: [go.mod](../../go.mod), [internal/config/config.go](../../internal/config/config.go)

## Context and Problem Statement

The Java control plane of EduIDE-Cloud needs about 4.9 GiB on one cluster because the JVM sizes itself against the node, while CPU stays between 1 and 44 m.
The new control plane targets less than 150 MiB per deployment and a start path measured in seconds.
We choose the language, the framework, the repository layout and the image shape.

## Decision Drivers

- Memory per component, mainly for the long-running operator, service and authz.
- Failure isolation: the authz check sits in the critical path of every session (G5).
- One release tag for everything that ships together (U2).
- Small attack surface in images.

## Considered Options

1. Keep Java (Quarkus) and fix resource limits.
2. Go with controller-runtime, one module, one binary with subcommands.
3. Go with controller-runtime, one module, four binaries with separate images.

## Decision Outcome

Chosen option: "Go, one module, four binaries with separate images".

- Go 1.27.4 (U1), controller-runtime in kubebuilder layout.
- Module path `github.com/EduIDE/EduIDE-control-plane`, repository `EduIDE/EduIDE-control-plane` (G11, U3).
  The repository is public and marked experimental in the README, description and topics.
- Binaries: `cmd/operator`, `cmd/service`, `cmd/authz`, `cmd/agent`.
  Each has its own image `ghcr.io/eduide/eduide-control-plane/{operator,service,authz,agent}` and its own Deployment (the agent runs as sidecar).
  The authz service is a separate Deployment so a fault there does not take down the REST API, and it releases independently (G5).
- Images are multi-stage builds on `gcr.io/distroless/static-debian12:nonroot` (U5).
  The landing page ships as a small static Go server on the same base.
- The monorepo also holds the pool scaler, the landing page, the `eduide-session` extension, the charts, docs and ADRs.
  IDE images, the deployment chart `EduIDE-Helm` and the e2e harness for real IDE images stay outside (U2).
- One release tag `vX.Y.Z` versions all components together (see the [release skill](../../.claude/skills/release/SKILL.md)).
- Deployment-wide settings come from one typed config file in a ConfigMap, not from CLI flags ([internal/config/config.go](../../internal/config/config.go)).

### Consequences

- Good: No JVM sizing problem, a few MiB per binary is realistic.
- Good: Shared Go types for CRDs, config and agent protocol, with one dependency set.
- Bad: Four images to build and scan per release.
- Bad: The team gives up Quarkus tooling and the Java code base.
- Follow-ups: CI path filters build only the affected parts ([0015](0015-test-strategy.md)); the chart layout is in [0018](0018-charts-oci.md).

## Pros and Cons of the Options

### Keep Java

- Good, because no rewrite.
- Bad, because memory and start time stay structurally higher and upstream is close to unmaintained.

### One binary with subcommands

- Good, because a single image.
- Bad, because the authz service and the operator share a release and an image size, which removes the isolation of G5.

### Four binaries

- Good, because failure areas, scaling and releases stay separate.
- Bad, because more image plumbing.
