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

  grep -q 'failOpen: false' <<<"$out" || fail "SecurityPolicy failOpen: false missing ($v)"
  grep -q 'name: Sec-Fetch-Mode, type: RegularExpression' <<<"$out" || fail "OIDC denyRedirect for non-navigation requests missing ($v)"
  grep -q 'value: "^/api/v1/.\*/events$"' <<<"$out" || fail "no SSE route rule without request timeout ($v)"
  [ "$(docs '.kind == "SecurityPolicy"' | wc -l)" -eq 1 ] || fail "expected one SecurityPolicy"
  [ "$(docs '.kind == "NetworkPolicy"' | wc -l)" -ge 3 ] || fail "NetworkPolicies missing ($v)"
  ! grep -qE '^kind: Ingress$' <<<"$out" || fail "chart renders an Ingress"
  ok "SecurityPolicy, NetworkPolicy, no Ingress ($v)"

  bad=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.resources.requests.memory == null or .resources.limits.memory == null or .securityContext.readOnlyRootFilesystem != true or .securityContext.allowPrivilegeEscalation != false or .securityContext.capabilities.drop[0] != "ALL")' | wc -l)
  [ "$bad" -eq 0 ] || fail "$bad containers lack resources or a hardened securityContext ($v)"
  ok "all containers have resources and securityContext"

  # Operator rules equal config/rbac/role.yaml.
  want=$(yq -o=json -I=0 '.rules' config/rbac/role.yaml)
  got=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Role" and .metadata.name == "r-eduide-control-plane-operator") | .rules')
  [ "$want" = "$got" ] || fail "operator Role rules differ from config/rbac/role.yaml"
  ok "operator Role equals config/rbac/role.yaml"

  # No role grants pods/exec or wildcards (the operator never execs into pods).
  badrules=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Role") | .rules[] | (.resources[], .verbs[], .apiGroups[])' | grep -cE '^"(\*|pods/exec|pods/attach|pods/portforward)"$' || true)
  [ "$badrules" -eq 0 ] || fail "a Role grants pods/exec, attach, portforward or a wildcard ($v)"
  ok "no exec or wildcard rules"

  # Session egress: DNS plus the internet without private ranges, by default.
  egress=$(echo "$out" | yq -o=json -I=0 'select(.kind == "NetworkPolicy" and .metadata.name == "r-eduide-control-plane-session-egress")')
  [ -n "$egress" ] || fail "session egress policy missing by default ($v)"
  for cidr in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 100.64.0.0/10 fc00::/7 fe80::/10; do
    grep -q "\"$cidr\"" <<<"$egress" || fail "session egress does not deny $cidr"
  done
  [ "$(echo "$egress" | yq -p=json '.spec.egress[1].to[0].ipBlock.cidr')" = "0.0.0.0/0" ] || fail "session egress lacks 0.0.0.0/0"
  [ "$(echo "$egress" | yq -p=json '.spec.egress[1].to[1].ipBlock.cidr')" = "::/0" ] || fail "session egress lacks ::/0"
  [ "$(echo "$egress" | yq -p=json '.spec.egress[0].ports[0].port')" = "53" ] || fail "session egress lacks DNS"
  ok "default session egress ($v)"

  extra=$(helm template r "$cp" -f "$v" -n ns --set 'networkPolicy.sessionEgressExtraDeny={198.51.100.0/24}' | yq -o=json -I=0 'select(.metadata.name == "r-eduide-control-plane-session-egress")')
  grep -q '198.51.100.0/24' <<<"$extra" || fail "sessionEgressExtraDeny is not rendered"
  custom=$(helm template r "$cp" -f "$v" -n ns --set 'networkPolicy.sessionEgress[0].to[0].ipBlock.cidr=203.0.113.0/24' | yq -o=json -I=0 'select(.metadata.name == "r-eduide-control-plane-session-egress")')
  grep -q '203.0.113.0/24' <<<"$custom" && ! grep -q '10.0.0.0/8' <<<"$custom" || fail "sessionEgress does not replace the default"
  ok "session egress extra deny and override"

  # Unknown instance ids pass the SecurityPolicy like existing ones: catch-all route on both hosts.
  fb=$(echo "$out" | yq -o=json -I=0 'select(.kind == "HTTPRoute" and .metadata.name == "r-eduide-control-plane-session-fallback")')
  [ "$(echo "$fb" | yq -p=json '.metadata.labels["eduide.github.io/route"]')" = "session" ] || fail "fallback route lacks the session label"
  [ "$(echo "$fb" | yq -p=json '.spec.rules[0].matches[0].path.value')" = "/" ] || fail "fallback route is not a catch-all"
  [ "$(echo "$fb" | yq -p=json '.spec.hostnames | length')" = "2" ] || fail "fallback route must cover instance and webview host"
  ok "fallback route ($v)"

  # Alerts and dashboard are scoped to the namespace of the release.
  mon=$(helm template r "$cp" -f "$v" -n ns --set monitoring.rules.enabled=true --set monitoring.dashboard.enabled=true)
  rules=$(echo "$mon" | yq -o=json -I=0 'select(.kind == "PrometheusRule") | .spec.groups[].rules[]')
  [ "$(echo "$rules" | grep -c .)" -ge 10 ] || fail "alerts missing"
  [ "$(echo "$rules" | yq -p=json 'select(.labels.eduide_namespace != "ns")' | wc -l)" -eq 0 ] || fail "an alert lacks eduide_namespace"
  for a in EduIDEOperatorDown EduIDEServiceDown EduIDEAuthzDown EduIDEAgentRequestErrors; do
    grep -q "\"$a\"" <<<"$rules" || fail "alert $a missing"
  done
  ! grep -q '__NAMESPACE__\|__FULLNAME__' <<<"$mon" || fail "alert or dashboard placeholder left"
  ! echo "$rules" | yq -p=json '.expr' | grep -E 'eduide_[a-z_]+([^{a-z_]|$)' || fail "an alert expression lacks the namespace selector"
  grep -q 'label_values(eduide_sessions, namespace)' <<<"$mon" || fail "dashboard lacks the namespace variable"
  grep -q 'alert: EduIDECertificateNotReady' <<<"$mon" || fail "certificate alerts missing"
  ps=$(helm template r "$cp" -f "$v" -n ns --set poolScaler.enabled=true --set image.tag=sha-abc1234)
  grep -q 'pool-scaler:sha-abc1234' <<<"$ps" || fail "pool scaler ignores image.tag"
  tp=$(helm template r "$cp" -f "$v" -n ns --set 'templates[0].name=t' --set 'templates[0].spec.image=i' --set 'templates[0].pool.minIdle=1' --set 'templates[0].pool.targetIdle=2' --set 'templates[0].pool.maxInstances=3')
  ! grep -q 'targetIdle' <<<"$tp" || fail "chart renders spec.targetIdle, which the pool scaler owns"
  grep -q 'name: r-eduide-control-plane-ns-pool-scaler-nodes\|pool-scaler-nodes' <<<"$ps" && grep -q 'kind: ClusterRoleBinding' <<<"$ps" || fail "pool scaler lacks its read-only node ClusterRole"
  ok "alerts and dashboard per namespace ($v)"

  mon=$(helm template r "$cp" -f "$v" -n ns --set monitoring.rules.enabled=true --set monitoring.rules.extraLabels.namespace=eduide-system --set monitoring.dashboard.enabled=true --set monitoring.dashboard.namespace=grafana)
  rules=$(echo "$mon" | yq -o=json -I=0 'select(.kind == "PrometheusRule") | .spec.groups[].rules[]')
  [ "$(echo "$rules" | yq -p=json 'select(.labels.namespace != "eduide-system" or .labels.eduide_namespace != "ns")' | wc -l)" -eq 0 ] || fail "extraLabels missing on an alert"
  [ "$(echo "$mon" | yq 'select(.kind == "ConfigMap" and .metadata.name == "r-eduide-control-plane-dashboard") | .metadata.namespace')" = "grafana" ] || fail "dashboard namespace not set"
  ok "alert extraLabels and dashboard namespace ($v)"

  # Admin API (U16): the service may read events and quotas, the scaler may write events, the config carries the role and the scaler interval.
  svc=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Role" and .metadata.name == "r-eduide-control-plane-service") | .rules')
  grep -q '"events.k8s.io"' <<<"$svc" && grep -q '"resourcequotas"' <<<"$svc" || fail "service Role lacks events or resourcequotas ($v)"
  [ "$(echo "$svc" | yq -p=json '[.[] | select(.apiGroups[0] == "events.k8s.io") | .verbs[]] | sort | join(",")')" = "list,watch" ] || fail "service may do more than list and watch events"
  scl=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Role" and .metadata.name == "r-eduide-control-plane-pool-scaler") | .rules')
  [ "$(echo "$scl" | yq -p=json '[.[] | select(.apiGroups[0] == "events.k8s.io") | .verbs[]] | join(",")')" = "create" ] || fail "pool-scaler Role must only create events"
  cfg=$(echo "$out" | yq 'select(.kind == "ConfigMap" and .metadata.name == "r-eduide-control-plane-config") | .data["config.yaml"]')
  grep -q 'rolePath: realm_access.roles' <<<"$cfg" || fail "oidc.admin is not rendered ($v)"
  grep -q 'scalerInterval: 30s' <<<"$cfg" || fail "scalerInterval is not rendered with the pool scaler ($v)"
  ok "admin API rules and config ($v)"

  # The control plane stays below 150 MiB of memory requests (pool scaler excluded, it is optional).
  total=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Deployment" and .metadata.name != "r-eduide-control-plane-pool-scaler") | .spec.replicas * (.spec.template.spec.containers[0].resources.requests.memory | sub("Mi"; "") | to_number)' | paste -sd+ - | bc)
  [ "$total" -lt 150 ] || fail "memory requests are ${total}Mi, limit is 150Mi"
  ok "memory requests ${total}Mi"
done

for c in charts/eduide-landing-page; do
  for v in "$c"/ci/*-values.yaml; do
    out=$(helm template r "$c" -f "$v")
    [ "$(echo "$out" | yq -o=json -I=0 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.resources == null or .securityContext == null)' | wc -l)" -eq 0 ] || fail "landing page container lacks resources or securityContext"
    grep -qE '^kind: HTTPRoute$' <<<"$out" || fail "landing page HTTPRoute missing"
  done
  ok "landing page"
done
echo "all chart checks passed"
