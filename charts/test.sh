#!/usr/bin/env bash
# Lints and renders all charts and checks the rules that must hold for every deployment.
# Needs helm and yq (mikefarah). Never talks to a cluster.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok: $*"; }

# The CRD chart holds byte-identical copies of config/crd (make generate).
for f in config/crd/*.yaml; do
  cmp -s "$f" "charts/eduide-control-plane-crds/templates/$(basename "$f")" || fail "$f differs from the CRD chart: run make generate"
done
[ "$(ls config/crd | wc -l)" -eq "$(ls charts/eduide-control-plane-crds/templates | wc -l)" ] || fail "CRD chart has extra or missing files"
ok "CRD chart equals config/crd"

for c in charts/*/; do
  c=${c%/}
  if ls "$c"/ci/*-values.yaml >/dev/null 2>&1; then
    for v in "$c"/ci/*-values.yaml; do helm lint "$c" -f "$v" >/dev/null || fail "helm lint $c -f $v"; done
  else
    helm lint "$c" >/dev/null || fail "helm lint $c"
  fi
  ok "lint $c"
done

# A deployment without the required values must be refused.
if helm template r charts/eduide-control-plane >/dev/null 2>&1; then fail "control plane chart renders without required values"; fi
ok "required values are enforced"

cp=charts/eduide-control-plane
for v in "$cp"/ci/*-values.yaml; do
  out=$(helm template r "$cp" -f "$v" -n ns)
  docs() { echo "$out" | yq -o=json -I=0 "select($1)"; }

  echo "$out" | grep -q 'failOpen: false' || fail "SecurityPolicy failOpen: false missing ($v)"
  [ "$(docs '.kind == "SecurityPolicy"' | wc -l)" -eq 1 ] || fail "expected one SecurityPolicy"
  [ "$(docs '.kind == "NetworkPolicy"' | wc -l)" -ge 3 ] || fail "NetworkPolicies missing ($v)"
  ! echo "$out" | grep -qE '^kind: Ingress$' || fail "chart renders an Ingress"
  ok "SecurityPolicy, NetworkPolicy, no Ingress ($v)"

  bad=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.resources.requests.memory == null or .resources.limits.memory == null or .securityContext.readOnlyRootFilesystem != true or .securityContext.allowPrivilegeEscalation != false or .securityContext.capabilities.drop[0] != "ALL")' | wc -l)
  [ "$bad" -eq 0 ] || fail "$bad containers lack resources or a hardened securityContext ($v)"
  ok "all containers have resources and securityContext"

  # Operator rules equal config/rbac/role.yaml.
  want=$(yq -o=json -I=0 '.rules' config/rbac/role.yaml)
  got=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Role" and .metadata.name == "r-eduide-control-plane-operator") | .rules')
  [ "$want" = "$got" ] || fail "operator Role rules differ from config/rbac/role.yaml"
  ok "operator Role equals config/rbac/role.yaml"

  # The control plane stays below 150 MiB of memory requests (pool scaler excluded, it is optional).
  total=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Deployment" and .metadata.name != "r-eduide-control-plane-pool-scaler") | .spec.replicas * (.spec.template.spec.containers[0].resources.requests.memory | sub("Mi"; "") | to_number)' | paste -sd+ - | bc)
  [ "$total" -lt 150 ] || fail "memory requests are ${total}Mi, limit is 150Mi"
  ok "memory requests ${total}Mi"
done

for c in charts/eduide-landing-page; do
  for v in "$c"/ci/*-values.yaml; do
    out=$(helm template r "$c" -f "$v")
    [ "$(echo "$out" | yq -o=json -I=0 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.resources == null or .securityContext == null)' | wc -l)" -eq 0 ] || fail "landing page container lacks resources or securityContext"
    echo "$out" | grep -qE '^kind: HTTPRoute$' || fail "landing page HTTPRoute missing"
  done
  ok "landing page"
done
echo "all chart checks passed"
