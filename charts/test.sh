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

  # Components with more than one replica spread over nodes (authz fails closed: one node must not hold all of it).
  nospread=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Deployment" and .spec.replicas > 1 and (.spec.template.spec.topologySpreadConstraints[0].topologyKey != "kubernetes.io/hostname")) | .metadata.name' | wc -l)
  [ "$nospread" -eq 0 ] || fail "$nospread Deployments with several replicas do not spread over nodes ($v)"
  [ "$(echo "$out" | yq 'select(.kind == "Deployment" and .metadata.name == "r-eduide-control-plane-authz") | .spec.template.spec.topologySpreadConstraints[0].labelSelector.matchLabels."app.kubernetes.io/component"')" = "authz" ] || fail "authz does not spread over nodes ($v)"
  ok "replicas spread over nodes"

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
  rules=$(echo "$mon" | yq -o=json -I=0 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert != null)')
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
  rules=$(echo "$mon" | yq -o=json -I=0 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert != null)')
  [ "$(echo "$rules" | yq -p=json 'select(.labels.namespace != "eduide-system" or .labels.eduide_namespace != "ns")' | wc -l)" -eq 0 ] || fail "extraLabels missing on an alert"
  [ "$(echo "$mon" | yq 'select(.kind == "ConfigMap" and .metadata.name == "r-eduide-control-plane-dashboard") | .metadata.namespace')" = "grafana" ] || fail "dashboard namespace not set"
  ok "alert extraLabels and dashboard namespace ($v)"

  # Usage recording rules: prefix, namespace scope, no extra labels, nothing aggregated per pod, instance, session or owner.
  rec=$(echo "$mon" | yq -o=json -I=0 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.record != null)')
  [ "$(echo "$rec" | grep -c .)" -ge 20 ] || fail "usage recording rules missing ($v)"
  [ "$(echo "$rec" | yq -p=json 'select((.record | test("^eduide:usage:")) | not)' | wc -l)" -eq 0 ] || fail "a recording rule lacks the eduide:usage: prefix"
  [ "$(echo "$rec" | yq -p=json 'select(.labels != null)' | wc -l)" -eq 0 ] || fail "a recording rule carries extra labels"
  # Only the outermost aggregation counts: M5 sums per pod inside before it takes quantiles across pods.
  ! echo "$rec" | yq -p=json '.expr' | awk 'match($0, /by \([^)]*\)/) {print substr($0, RSTART, RLENGTH)}' | grep -E '(pod|instance|session|owner|uid)[,)]' || fail "a recording rule aggregates by pod, instance, session or owner"
  ! echo "$rec" | yq -p=json '.expr' | grep -E 'eduide_[a-z_]+\{' | grep -v 'namespace="ns"' || fail "a usage rule lacks the namespace selector"
  ok "usage recording rules ($v)"

  # Admin API (U16): the service may read events and quotas, the scaler may write events, the config carries the role and the scaler interval.
  svc=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Role" and .metadata.name == "r-eduide-control-plane-service") | .rules')
  grep -q '"events.k8s.io"' <<<"$svc" && grep -q '"resourcequotas"' <<<"$svc" || fail "service Role lacks events or resourcequotas ($v)"
  [ "$(echo "$svc" | yq -p=json '[.[] | select(.apiGroups[0] == "events.k8s.io") | .verbs[]] | sort | join(",")')" = "list,watch" ] || fail "service may do more than list and watch events"
  scl=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Role" and .metadata.name == "r-eduide-control-plane-pool-scaler") | .rules')
  [ "$(echo "$scl" | yq -p=json '[.[] | select(.apiGroups[0] == "events.k8s.io") | .verbs[]] | join(",")')" = "create" ] || fail "pool-scaler Role must only create events"
  # The scaler writes pools only through the scale subresource: it must not be able to change minIdle or maxInstances.
  [ "$(echo "$scl" | yq -p=json '[.[] | select(.resources[0] == "idepools") | .verbs[]] | sort | join(",")')" = "get,list,watch" ] || fail "pool-scaler Role may write idepools itself"
  cfg=$(echo "$out" | yq 'select(.kind == "ConfigMap" and .metadata.name == "r-eduide-control-plane-config") | .data["config.yaml"]')
  grep -q 'rolePath: realm_access.roles' <<<"$cfg" || fail "oidc.admin is not rendered ($v)"
  grep -q 'scalerInterval: 30s' <<<"$cfg" || fail "scalerInterval is not rendered with the pool scaler ($v)"
  ok "admin API rules and config ($v)"

  # Git accounts (ADR 0023): the config names files, never secrets; only the service mounts the key and the client
  # secrets and may update Secrets; without providers the chart renders nothing for the feature.
  ! grep -q 'clientSecretRef' <<<"$cfg" && grep -q 'encryptionKeyFile: /etc/eduide/git-accounts/encryption-key' <<<"$cfg" && grep -q 'clientSecretFile: /etc/eduide/git-accounts/client-secret-github' <<<"$cfg" || fail "git accounts config is wrong ($v)"
  [ "$(echo "$out" | yq 'select(.kind == "Deployment" and (.spec.template.spec.volumes[].name == "git-accounts")) | .metadata.name')" = "r-eduide-control-plane-service" ] || fail "git-accounts volume is not only in the service ($v)"
  echo "$out" | yq -e 'select(.kind == "Secret" and .metadata.name == "r-eduide-control-plane-git-accounts-key")' >/dev/null || fail "git accounts key Secret missing ($v)"
  [ "$(echo "$svc" | yq -p=json '[.[] | select(.resources[0] == "secrets") | .verbs[]] | sort | join(",")')" = "create,delete,get,list,patch,update" ] || fail "service Secret verbs with git accounts ($v)"
  nogit=$(helm template r "$cp" -f "$v" -n ns --set config.gitAccounts=null)
  ! grep -q 'git-accounts\|gitAccounts' <<<"$nogit" || fail "chart renders git accounts without providers ($v)"
  [ "$(echo "$nogit" | yq -o=json -I=0 'select(.kind == "Role" and .metadata.name == "r-eduide-control-plane-service") | .rules[] | select(.resources[0] == "secrets") | .verbs | join(",")')" = '"create,get,delete"' ] || fail "service Secret verbs without git accounts ($v)"
  ok "git accounts ($v)"

  # The agent follows image.pullPolicy only when it is not the default, so default installations keep their config.
  ! grep -q 'imagePullPolicy' <<<"$cfg" || fail "default values render an agent pull policy ($v)"
  always=$(helm template r "$cp" -f "$v" -n ns --set image.pullPolicy=Always | yq 'select(.kind == "ConfigMap" and .metadata.name == "r-eduide-control-plane-config") | .data["config.yaml"]')
  [ "$(echo "$always" | yq '.agent.imagePullPolicy')" = "Always" ] || fail "image.pullPolicy=Always does not reach agent.imagePullPolicy ($v)"
  ok "agent pull policy ($v)"

  # Git proxy (ADR 0023): ClusterIP only, never routed, policies present, nothing rendered without providers.
  gp=r-eduide-control-plane-git-proxy
  [ "$(echo "$out" | yq 'select(.kind == "Deployment" and .metadata.name == "'$gp'") | .spec.replicas')" = "2" ] || fail "git-proxy does not run 2 replicas ($v)"
  [ "$(echo "$out" | yq 'select(.kind == "Service" and .metadata.name == "'$gp'") | .spec.type // "ClusterIP"')" = "ClusterIP" ] || fail "git-proxy Service is not ClusterIP ($v)"
  echo "$out" | yq -e 'select(.kind == "PodDisruptionBudget" and .metadata.name == "'$gp'")' >/dev/null || fail "git-proxy PodDisruptionBudget missing ($v)"
  [ "$(echo "$out" | yq 'select(.kind == "PodDisruptionBudget" and .metadata.name == "'$gp'") | .spec.maxUnavailable')" = "1" ] || fail "git-proxy PodDisruptionBudget must use maxUnavailable (minAvailable blocks drains with one replica) ($v)"
  one=$(helm template r "$cp" -f "$v" -n ns --set gitProxy.replicas=1)
  [ "$(echo "$one" | yq 'select(.kind == "PodDisruptionBudget" and .metadata.name == "'$gp'") | .spec.minAvailable')" = "null" ] || fail "git-proxy PodDisruptionBudget blocks drains with one replica ($v)"
  [ "$(echo "$out" | yq 'select(.kind == "HTTPRoute") | .spec.rules[].backendRefs[].name' | grep -c git-proxy || true)" -eq 0 ] || fail "an HTTPRoute points to git-proxy ($v)"
  ! echo "$out" | yq 'select(.kind == "HTTPRoute") | .spec.hostnames[]' | grep -q git-proxy || fail "git-proxy has a hostname ($v)"
  for np in session-to-git-proxy git-proxy service-internal; do
    echo "$out" | yq -e 'select(.kind == "NetworkPolicy" and .metadata.name == "r-eduide-control-plane-'$np'")' >/dev/null || fail "NetworkPolicy $np missing ($v)"
  done
  # The session rule selects pods, not an ipBlock, so no deny list can take it away.
  [ "$(echo "$out" | yq -o=json -I=0 'select(.kind == "NetworkPolicy" and .metadata.name == "r-eduide-control-plane-session-to-git-proxy") | .spec.egress[0].to[0].podSelector.matchLabels."app.kubernetes.io/component"')" = '"git-proxy"' ] || fail "session egress rule to git-proxy ($v)"
  [ "$(echo "$out" | yq -o=json -I=0 'select(.kind == "NetworkPolicy" and .metadata.name == "r-eduide-control-plane-session-to-git-proxy") | .spec.egress[0].to[0].ipBlock')" = "null" ] || fail "session rule to git-proxy uses an ipBlock ($v)"
  [ "$(echo "$out" | yq 'select(.kind == "NetworkPolicy" and .metadata.name == "r-eduide-control-plane-service-internal") | .spec.ingress[0].from[0].podSelector.matchLabels."app.kubernetes.io/component"')" = "git-proxy" ] || fail "the internal port does not accept git-proxy only ($v)"
  [ "$(echo "$out" | yq 'select(.kind == "NetworkPolicy" and .metadata.name == "r-eduide-control-plane-git-proxy") | .spec.ingress[0].from[0].podSelector.matchLabels."eduide.github.io/component"')" = "session" ] || fail "git-proxy does not accept session pods ($v)"
  gpe=$(echo "$out" | yq -o=json -I=0 'select(.kind == "NetworkPolicy" and .metadata.name == "r-eduide-control-plane-git-proxy")')
  for cidr in 10.0.0.0/8 169.254.0.0/16 fc00::/7; do grep -q "\"$cidr\"" <<<"$gpe" || fail "git-proxy egress does not deny $cidr ($v)"; done
  # No volume of git-proxy holds a Secret: it needs neither the salt nor the account key.
  [ "$(echo "$out" | yq 'select(.kind == "Deployment" and .metadata.name == "'$gp'") | .spec.template.spec.volumes[] | select(.secret != null) | .name' | wc -l | tr -d ' ')" = "0" ] || fail "git-proxy mounts a Secret ($v)"
  grep -q 'proxyURL: http://r-eduide-control-plane-git-proxy.ns.svc:8080' <<<"$cfg" && grep -q 'internalURL: http://r-eduide-control-plane-service.ns.svc:8083' <<<"$cfg" || fail "git proxy URLs not in the config ($v)"
  [ "$(echo "$out" | yq 'select(.kind == "Service" and .metadata.name == "r-eduide-control-plane-service") | .spec.ports[].port' | paste -sd, -)" = "8080,8083" ] || fail "service ports with git accounts ($v)"
  ! grep -q 'git-proxy\|8083' <<<"$nogit" || fail "chart renders git-proxy or the internal port without providers ($v)"
  ok "git proxy ($v)"

  # The control plane stays below 150 MiB of memory requests (pool scaler and git-proxy excluded, they are optional).
  total=$(echo "$out" | yq -o=json -I=0 'select(.kind == "Deployment" and .metadata.name != "r-eduide-control-plane-pool-scaler" and .metadata.name != "r-eduide-control-plane-git-proxy") | .spec.replicas * (.spec.template.spec.containers[0].resources.requests.memory | sub("Mi"; "") | to_number)' | paste -sd+ - | bc)
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
