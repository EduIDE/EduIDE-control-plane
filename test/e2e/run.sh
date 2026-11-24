#!/usr/bin/env bash
# Builds the e2e cluster (kind, Calico, Envoy Gateway, Keycloak, Gateway, DNS, TLS), installs the
# charts and runs the Go tests. Safe to run again: every step checks before it changes anything.
#
# Environment:
#   E2E_CLUSTER      kind cluster name (default eduide-e2e)
#   E2E_PORT         host port on 127.0.0.1 for the gateway (default 18443)
#   TAG              image tag (default e2e)
#   E2E_INFRA_ONLY=1 stop after the infrastructure and run only the infrastructure smoke test
#   E2E_SKIP_IMAGES=1 do not build and load images
#   E2E_SKIP_BROWSER=1 skip the Playwright test
#   E2E_RUN          go test -run expression. Tests named TestGit* run in their own phase, with the Git providers on.
#   EG_VERSION       Envoy Gateway version (default v1.8.3, the version of the test cluster)
set -euo pipefail

REPO=$(cd "$(dirname "$0")/../.." && pwd)
cd "$REPO"
CLUSTER=${E2E_CLUSTER:-eduide-e2e}
PORT=${E2E_PORT:-18443}
TAG=${TAG:-e2e}
NS=eduide-e2e
EG_VERSION=${EG_VERSION:-v1.8.3}
CALICO_VERSION=v3.33.1
KIND_VERSION=v0.33.0
GIT_HOST=fake-git.$NS.svc
HOSTS=ide.e2e.test,instance.ide.e2e.test,webview.instance.ide.e2e.test,keycloak.e2e.test,smoke.e2e.test
IMG=ghcr.io/eduide/eduide-control-plane
export PATH="$REPO/bin:$PATH"
mkdir -p "$REPO/.e2e" "$REPO/bin"

# shellcheck source=safe.sh
source "$REPO/test/e2e/safe.sh"

[[ -x "$REPO/bin/kind" ]] || GOBIN="$REPO/bin" go install "sigs.k8s.io/kind@$KIND_VERSION"

log "certificates"
if [[ ! -f .e2e/certs/ca.crt ]]; then
  go run ./test/e2e/tools/certgen -out .e2e/certs -hosts "$HOSTS"
fi
# The fake Git provider has a CA of its own, so the tests prove that caConfigMapRef works.
if [[ ! -f .e2e/certs-git/ca.crt ]]; then
  go run ./test/e2e/tools/certgen -out .e2e/certs-git -hosts "$GIT_HOST"
fi

log "kind cluster $CLUSTER"
if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  sed "s/@E2E_PORT@/$PORT/" test/e2e/kind.yaml | kind create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --config -
else
  kind export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
fi
assert_local

log "Calico $CALICO_VERSION"
# The pod CIDR must not overlap the docker network of kind (OrbStack uses 192.168.x.x), so the default 192.168.0.0/16 is replaced.
curl -fsSL "https://raw.githubusercontent.com/projectcalico/calico/$CALICO_VERSION/manifests/calico.yaml" \
  | sed -e 's|# - name: CALICO_IPV4POOL_CIDR|- name: CALICO_IPV4POOL_CIDR|' -e 's|#   value: "192.168.0.0/16"|  value: "10.244.0.0/16"|' \
  | kubectl apply -f - >/dev/null
kubectl -n kube-system rollout status ds/calico-node --timeout=300s
kubectl wait --for=condition=Ready nodes --all --timeout=300s

log "node pools"
WORKERS=$(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' -o name | sort)
kubectl label --overwrite "$(sed -n 1p <<<"$WORKERS")" e2e.eduide.test/node-pool=pool-a
kubectl label --overwrite "$(sed -n 2p <<<"$WORKERS")" e2e.eduide.test/node-pool=pool-b

log "namespaces, CA, TLS, secrets"
for n in envoy-gateway-system "$NS"; do
  kubectl create namespace "$n" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$n" create configmap e2e-ca --from-file=ca.crt=.e2e/certs/ca.crt --dry-run=client -o yaml | kubectl apply -f - >/dev/null
done
kubectl -n "$NS" create secret tls e2e-tls --cert=.e2e/certs/tls.crt --key=.e2e/certs/tls.key --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" create secret generic e2e-gateway-client --from-literal=client-secret=e2e-gateway-secret --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" create secret tls fake-git-tls --cert=.e2e/certs-git/tls.crt --key=.e2e/certs-git/tls.key --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" create configmap fake-git-ca --from-file=ca.crt=.e2e/certs-git/ca.crt --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" create secret generic e2e-git-oauth --from-literal=client-secret=e2e-secret --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" create configmap keycloak-realm --from-file=realm.json=test/e2e/realm.json --dry-run=client -o yaml | kubectl apply -f - >/dev/null

log "Envoy Gateway $EG_VERSION (also installs the Gateway API CRDs)"
# An interrupted earlier run leaves the release in a pending state that blocks upgrades.
if helm -n envoy-gateway-system status eg 2>/dev/null | grep -q '^STATUS: pending'; then
  helm -n envoy-gateway-system uninstall eg >/dev/null
fi
helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm --version "$EG_VERSION" \
  -n envoy-gateway-system -f test/e2e/eg-values.yaml --wait --timeout 5m >/dev/null

log "fake IDE image"
docker build -q -t "$IMG/fake-ide:e2e" test/fake-ide >/dev/null
kind load docker-image "$IMG/fake-ide:e2e" --name "$CLUSTER"
log "fake Git provider image"
docker build -q -t "$IMG/fake-git:e2e" test/fake-git >/dev/null
kind load docker-image "$IMG/fake-git:e2e" --name "$CLUSTER"

log "CoreDNS: *.e2e.test resolves to the gateway entry Service"
corefile=$(kubectl -n kube-system get cm coredns -o jsonpath='{.data.Corefile}')
if [[ $(grep -cF gateway-entry <<<"$corefile") != 1 ]]; then
  rule='    rewrite name regex (.*)\\.e2e\\.test gateway-entry.envoy-gateway-system.svc.cluster.local answer auto'
  patched=$(grep -vF gateway-entry <<<"$corefile" | awk -v rule="$rule" '{print} /^[[:space:]]*ready[[:space:]]*$/ {print rule}')
  kubectl -n kube-system patch cm coredns --type merge -p "$(jq -n --arg c "$patched" '{data:{Corefile:$c}}')" >/dev/null
  kubectl -n kube-system rollout restart deploy/coredns >/dev/null
  kubectl -n kube-system rollout status deploy/coredns --timeout=120s
fi

log "Gateway, Keycloak, smoke route, fake Git provider"
kubectl apply -f test/e2e/manifests/gateway.yaml >/dev/null
kubectl apply -f test/e2e/manifests/keycloak.yaml >/dev/null
kubectl apply -f test/e2e/manifests/fake-git.yaml >/dev/null
kubectl -n "$NS" wait --for=condition=Programmed gateway/eduide --timeout=180s
kubectl -n "$NS" rollout status deploy/keycloak --timeout=300s
kubectl -n "$NS" rollout status deploy/fake-git --timeout=120s
# Envoy Gateway reads the OIDC discovery document when it sees the SecurityPolicy, so Keycloak and DNS must work first.
# A policy that was rejected earlier is not retried: recreate it.
if [[ "$(kubectl -n "$NS" get securitypolicy smoke -o 'jsonpath={.status.ancestors[0].conditions[?(@.type=="Accepted")].status}' 2>/dev/null)" == False ]]; then
  kubectl -n "$NS" delete securitypolicy smoke >/dev/null
fi
kubectl apply -f test/e2e/manifests/smoke.yaml >/dev/null
kubectl -n "$NS" rollout status deploy/smoke --timeout=120s
kubectl -n "$NS" wait securitypolicy/smoke --timeout=120s \
  --for='jsonpath={.status.ancestors[0].conditions[?(@.type=="Accepted")].status}=True'

# service and authz verify tokens of the Keycloak at its https issuer URL: they need the test CA.
# A helm upgrade can drop the patch, so every install is followed by this call.
trust_e2e_ca() {
  for d in $(kubectl -n "$NS" get deploy -o name | grep -E '(service|authz)$' || true); do
    c=$(kubectl -n "$NS" get "$d" -o jsonpath='{.spec.template.spec.containers[0].name}')
    kubectl -n "$NS" patch "$d" --type strategic -p "$(jq -n --arg c "$c" '{spec:{template:{spec:{
      volumes:[{name:"e2e-ca",configMap:{name:"e2e-ca"}}],
      containers:[{name:$c,env:[{name:"SSL_CERT_FILE",value:"/etc/e2e-ca/ca.crt"}],
        volumeMounts:[{name:"e2e-ca",mountPath:"/etc/e2e-ca",readOnly:true}]}]}}}}')" >/dev/null
  done
  for d in $(kubectl -n "$NS" get deploy -o name); do
    kubectl -n "$NS" rollout status "$d" --timeout=300s
  done
  kubectl -n "$NS" wait --for=condition=Available deploy --all --timeout=300s
}

if [[ "${E2E_INFRA_ONLY:-}" != 1 ]]; then
  if [[ "${E2E_SKIP_IMAGES:-}" != 1 ]]; then
    log "images (make images TAG=$TAG)"
    make images TAG="$TAG"
    for i in operator service authz agent git-proxy landing-page; do
      kind load docker-image "$IMG/$i:$TAG" --name "$CLUSTER"
    done
  fi

  log "charts"
  helm upgrade --install eduide-crds charts/eduide-control-plane-crds -n "$NS" >/dev/null
  helm upgrade --install eduide charts/eduide-control-plane -n "$NS" -f test/e2e/values.yaml \
    --set image.tag="$TAG" >/dev/null
  helm upgrade --install eduide-landing charts/eduide-landing-page -n "$NS" -f test/e2e/values-landing.yaml \
    --set image.tag="$TAG" >/dev/null

  trust_e2e_ca
fi

# The Git accounts run installs the same chart again with values-git.yaml (providers on, one egress rule for
# git-proxy), so the default run stays free of the feature. A run without E2E_RUN does both.
run_git_phase() {
  log "charts with the Git providers (values-git.yaml)"
  helm upgrade --install eduide charts/eduide-control-plane -n "$NS" -f test/e2e/values.yaml -f test/e2e/values-git.yaml \
    --set image.tag="$TAG" >/dev/null
  trust_e2e_ca
  E2E_GIT_CA_FILE="$REPO/.e2e/certs-git/ca.crt" go test -tags e2e ./test/e2e/... -run "${E2E_RUN:-^TestGit}" -count=1 -v -timeout 40m
}

log "tests"
export E2E_PORT="$PORT" E2E_CA_FILE="$REPO/.e2e/certs/ca.crt" E2E_NAMESPACE="$NS"
RUN_GIT=1
if [[ "${E2E_INFRA_ONLY:-}" == 1 ]]; then
  RUN='^TestInfra'
  RUN_GIT=0
elif [[ -z "${E2E_RUN:-}" ]]; then
  RUN=.
elif [[ "$E2E_RUN" == *TestGit* ]]; then
  RUN=''
else
  RUN=$E2E_RUN
  RUN_GIT=0
fi
[[ -n "$RUN" ]] && go test -tags e2e ./test/e2e/... -run "$RUN" -skip '^TestGit' -count=1 -v -timeout 40m

if [[ "${E2E_SKIP_BROWSER:-}" != 1 && -n "$RUN" ]]; then
  log "browser tests (Playwright; skip with E2E_SKIP_BROWSER=1)"
  grep=()
  [[ "${E2E_INFRA_ONLY:-}" == 1 ]] && grep=(--grep infra)
  (cd test/e2e/browser && npm ci --silent && npx playwright install chromium && npx playwright test ${grep[@]+"${grep[@]}"})
fi

[[ "$RUN_GIT" == 1 ]] && run_git_phase
exit 0
