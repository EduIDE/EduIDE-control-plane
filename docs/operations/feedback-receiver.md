# Feedback receiver

The central receiver files feedback from the IDE of every installation as an issue in one triage repository.
It runs once, on one cluster, from the chart `eduide-feedback-receiver` (off by default). Design: [ADR 0024](../adr/0024-feedback-receiver.md).
The per-installation side (service endpoint, `config.feedback` in the `eduide-control-plane` chart) is described with that change.

## Create the GitHub App (maintainer)

1. Create the repository `EduIDE/feedback-inbox` (private) with the labels `via-ide`, `needs-triage`, `bug` and `enhancement`. Create the labels first, so every issue gets them.
2. In the organization settings create a GitHub App. Callback URL and webhook stay empty and webhooks off.
3. Repository permissions: **Issues: Read and write**. Nothing else (metadata read is implied). Install the app on **only** the repository `EduIDE/feedback-inbox`.
4. Note the App ID (app page) and the installation ID (the number in the URL of the installation settings page).
5. Generate a private key (PEM) on the app page.

## Create the Secrets

In the namespace of the receiver:

```bash
kubectl create secret generic eduide-feedback-github --from-file=private-key.pem=./app-private-key.pem
kubectl create secret generic eduide-feedback-keys --from-literal=<installation>="$(openssl rand -hex 32)"
```

The key name in `eduide-feedback-keys` is the installation name, the value is the key that installation sends.
The installation name is `config.deployment` of the installation, lowercase letters, digits, `.` and `-`.
Delete the local copy of the private key afterwards.

## Install

```yaml
enabled: true
host: feedback.example.org
gateway: {name: eduide, namespace: envoy-gateway-system, sectionName: https}
installationKeysSecret: eduide-feedback-keys
github:
  appID: "123456"
  installationID: "7890123"
  repository: EduIDE/feedback-inbox
  privateKeySecret: {name: eduide-feedback-github, key: private-key.pem}
```

`helm install feedback oci://ghcr.io/eduide/eduide-control-plane/charts/eduide-feedback-receiver -f values.yaml`.
The render fails when a required value is missing. The pod is `app.kubernetes.io/name=eduide-feedback-receiver`.

## Add or rotate an installation key

```bash
kubectl patch secret eduide-feedback-keys -p "{\"stringData\":{\"<installation>\":\"$(openssl rand -hex 32)\"}}"
```

Kubernetes updates the mounted files within about a minute; the receiver reads them on every request, so it needs no restart.
Put the same value into the Secret that `config.feedback.keySecret` of that installation names.
To revoke an installation, remove its key from the Secret.

## Check

Only `/v1/feedback` is routed to the receiver; `GET /healthz` is for the kubelet probes.
A request with a wrong key answers 401, a valid one 202 and creates an issue.

## Logs

JSON lines with `installation`, `kind`, `result` (`created`, `unauthorized`, `invalid`, `rate_limited`, `github_error`) and `issue`.
They never hold the feedback text.
`github_error` with `answered 401` or `403`: the key or the app permissions are wrong. `404`: the app is not installed on the repository, or `github.repository` is wrong.

## Limits

60 requests per hour per installation, kept in memory (lost on restart). Body 32 KiB, title 1 to 120, description 1 to 5000 characters.
