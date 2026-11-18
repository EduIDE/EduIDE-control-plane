#!/usr/bin/env bash
# kwok scale scenarios on the kind cluster of test/e2e (run `E2E_INFRA_ONLY=1 make e2e` or `make e2e` first,
# and install the charts). Usage: test/scale/run.sh [setup|sessions1000|burst200|all]
#
# kwok simulates nodes and pods: no container runs. Pods become Ready at once, so the agent protocol is not
# exercised (the operator cannot reach a fake pod IP). The scenarios measure what scales with the object count:
# the operator reconcile throughput, API server load, informer memory and the route count.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/../.." && pwd)
cd "$REPO"
CLUSTER=${E2E_CLUSTER:-eduide-e2e}
NS=${E2E_NAMESPACE:-eduide-e2e}
KWOK_VERSION=v0.8.0
NODES=${SCALE_NODES:-20}
SESSIONS=${SCALE_SESSIONS:-1000}
BURST_PER_MINUTE=${SCALE_BURST_PER_MINUTE:-200}
BURST_MINUTES=${SCALE_BURST_MINUTES:-3}
# shellcheck source=../e2e/safe.sh
source "$REPO/test/e2e/safe.sh"
assert_local

setup() {
  log "kwok $KWOK_VERSION"
  local base="https://github.com/kubernetes-sigs/kwok/releases/download/$KWOK_VERSION"
  kubectl apply -f "$base/kwok.yaml" >/dev/null
  kubectl apply -f "$base/stage-fast.yaml" >/dev/null
  kubectl -n kube-system rollout status deploy/kwok-controller --timeout=120s

  log "$NODES fake nodes in node pool kwok"
  for i in $(seq 1 "$NODES"); do
    cat <<YAML
apiVersion: v1
kind: Node
metadata:
  name: kwok-node-$i
  annotations: {kwok.x-k8s.io/node: fake, node.alpha.kubernetes.io/ttl: "0"}
  labels: {type: kwok, kubernetes.io/hostname: kwok-node-$i, e2e.eduide.test/node-pool: kwok}
spec:
  taints: [{key: kwok.x-k8s.io/node, value: fake, effect: NoSchedule}]
status:
  allocatable: {cpu: "32", memory: 256Gi, pods: "110"}
  capacity: {cpu: "32", memory: 256Gi, pods: "110"}
YAML
    echo ---
  done | kubectl apply -f - >/dev/null

  # A template that runs only on the fake nodes.
  kubectl apply -f - >/dev/null <<YAML
apiVersion: eduide.github.io/v1alpha1
kind: IDETemplate
metadata: {name: scale, namespace: $NS, labels: {app.kubernetes.io/managed-by: eduide}}
spec:
  image: registry.example.org/fake:latest
  port: 3000
  persistence: {allowed: [none], default: none}
  placement:
    nodeSelector: {type: kwok}
    tolerations: [{key: kwok.x-k8s.io/node, operator: Exists}]
YAML
}

# session NAME OWNER writes a session and its data Secret (what the service does at launch).
session() {
  cat <<YAML
apiVersion: v1
kind: Secret
metadata: {name: $1-data, namespace: $NS, labels: {app.kubernetes.io/managed-by: eduide}}
stringData: {GIT_URI: "https://git.example.org/scale/$1.git"}
---
apiVersion: eduide.github.io/v1alpha1
kind: IDESession
metadata: {name: $1, namespace: $NS, labels: {app.kubernetes.io/managed-by: eduide}}
spec: {owner: $2, templateRef: scale, dataSecretRef: $1-data}
---
YAML
}

# metrics prints the numbers that matter after a scenario.
metrics() {
  log "result: $1"
  echo "sessions by phase:"
  kubectl -n "$NS" get idesessions -o json | jq -r '[.items[].status.phase // "none"] | group_by(.) | map("\(.[0]) \(length)") | .[]'
  echo "pods: $(kubectl -n "$NS" get pods -l app.kubernetes.io/managed-by=eduide --no-headers | wc -l | tr -d ' ')"
  echo "httproutes: $(kubectl -n "$NS" get httproutes -l eduide.github.io/route=session --no-headers | wc -l | tr -d ' ')"
  echo "time from creation to Ready (s):"
  kubectl -n "$NS" get idesessions -o json | jq -r '
    [.items[] | select(.status.timestamps.ready) | ((.status.timestamps.ready | fromdate) - (.metadata.creationTimestamp | fromdate))] | sort
    | if length == 0 then "no session became Ready" else "p50 \(.[length/2|floor]) p95 \(.[length*0.95|floor]) max \(.[-1])" end'
  echo "API server requests (total by verb, top 8):"
  kubectl get --raw /metrics | awk '/^apiserver_request_total/ {gsub(/.*verb="/,""); split($0,a,"\""); v[a[1]]+=$NF} END {for (k in v) print v[k], k}' | sort -rn | head -8
  kubectl -n "$NS" top pods --no-headers 2>/dev/null | head -5 || true
}

sessions1000() {
  log "scenario: $SESSIONS sessions at once"
  for i in $(seq 1 "$SESSIONS"); do session "scale-$i" "user$((i % 200))"; done | kubectl apply -f - >/dev/null
  local deadline=$((SECONDS + 600))
  until [[ $(kubectl -n "$NS" get idesessions -o json | jq '[.items[] | select(.status.phase == "Ready")] | length') -ge $SESSIONS ]]; do
    [[ $SECONDS -lt $deadline ]] || { echo "not all sessions Ready after 10 minutes"; break; }
    sleep 10
  done
  metrics sessions1000
  kubectl -n "$NS" delete idesessions -l app.kubernetes.io/managed-by=eduide --wait=false >/dev/null
}

burst200() {
  log "scenario: $BURST_PER_MINUTE launches per minute for $BURST_MINUTES minutes"
  local n=0
  for m in $(seq 1 "$BURST_MINUTES"); do
    local minute_end=$((SECONDS + 60))
    for _ in $(seq 1 "$BURST_PER_MINUTE"); do
      n=$((n + 1))
      session "burst-$n" "user$((n % 200))" | kubectl apply -f - >/dev/null &
      sleep "$(awk "BEGIN {print 60 / $BURST_PER_MINUTE}")"
    done
    wait
    [[ $SECONDS -ge $minute_end ]] || sleep $((minute_end - SECONDS))
  done
  sleep 60
  metrics burst200
  kubectl -n "$NS" delete idesessions -l app.kubernetes.io/managed-by=eduide --wait=false >/dev/null
}

case "${1:-all}" in
  setup) setup ;;
  sessions1000) setup; sessions1000 ;;
  burst200) setup; burst200 ;;
  all) setup; sessions1000; burst200 ;;
  *) echo "usage: $0 [setup|sessions1000|burst200|all]" >&2; exit 2 ;;
esac
