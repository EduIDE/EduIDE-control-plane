# 0019. Network isolation of session pods with NetworkPolicy, default deny

- Status: Accepted
- Date: 2026-11-18
- Decisions: G4
- Contracts: [api/agent/v1.yaml](../../api/agent/v1.yaml), [api/v1alpha1/labels.go](../../api/v1alpha1/labels.go)

## Context and Problem Statement

In the old stack an oauth2-proxy in every pod guards the IDE.
With auth at the gateway ([0005](0005-session-auth-at-gateway.md)) nothing in the pod stops another pod in the namespace from calling the IDE port directly.
IDEs run user code, so session pods must not reach each other or the control ports of other pods.

## Decision Drivers

- A compromised or malicious session must not read other sessions.
- Defence in depth: the instance token at the agent is the first line, the network the second ([0006](0006-agent-sidecar.md)).
- The cluster must really enforce the policies, which has to be tested.

## Considered Options

1. Rely on the agent token only.
2. NetworkPolicies with default deny, plus a negative e2e test.
3. A service mesh with mutual TLS.

## Decision Outcome

Chosen option: "NetworkPolicies with default deny and a negative test".

- Default deny for ingress in the namespace of the deployment.
- Agent proxy port (8080) of a session pod: ingress only from the Envoy pods of the gateway. The IDE itself listens on `127.0.0.1` and has no network port.
- Agent control port (8081): ingress only from the operator. The kubelet probes `/healthz` and `/readyz` on that port without auth.
- Session pods cannot reach one another: no ingress rule allows session pods. Egress is limited by default: DNS to `kube-system` and the internet, without the private and link-local ranges (`networkPolicy.sessionEgressDeny`, extended by `networkPolicy.sessionEgressExtraDeny` for the pod CIDR and the node network of the cluster). `networkPolicy.sessionEgress` replaces the default.
- RBAC: no Role grants `pods/exec`, `pods/attach`, `pods/portforward` or wildcards; `charts/test.sh` checks it. `kubectl auth can-i ... --as operator` impersonates a user named `operator`, not the ServiceAccount, and says nothing about the chart Role. Use `--as system:serviceaccount:<ns>:<release>-eduide-control-plane-operator`.
- authz and service accept traffic from the gateway and, for metrics, from the monitoring namespace.
- Policies are part of the control plane chart ([0018](0018-charts-oci.md)) and are selected by the labels in [labels.go](../../api/v1alpha1/labels.go).
- Enforcement check: an e2e negative test starts a foreign pod in the namespace and expects that it cannot reach the IDE port or the agent control port. The same test runs as a smoke test after every deployment. The clusters differ (Calico on one, k3s with Flannel and the built-in policy controller on another, not yet verified there), so the result matters per cluster.
- If a cluster does not enforce policies, the deployment is not released for productive use. The instance token still holds, and the token sits in the HTTPRoute, so the network line is not optional ([0004](0004-gateway-api-only.md)).

### Consequences

- Good: Sessions are isolated from each other and from the control ports.
- Good: The enforcement is tested instead of assumed.
- Bad: Depends on the CNI. A cluster without enforcement needs a fix or another cluster.
- Follow-up: Runbook for "policy not enforced" in `docs/operations/`.

## Pros and Cons of the Options

### Agent token only

- Good, because no CNI dependency.
- Bad, because a single line of defence, and the token is visible in the route spec.

### NetworkPolicy plus test

- Good, because a standard mechanism with a verifiable result.
- Bad, because it needs an enforcing CNI.

### Service mesh

- Good, because strong identity.
- Bad, because a heavy component per cluster and memory in every pod.
