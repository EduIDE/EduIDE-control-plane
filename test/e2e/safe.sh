# Sourced by test/e2e/run.sh and test/scale/run.sh. Needs REPO and CLUSTER.
# SAFETY: the default kube context can be a real cluster. Nothing runs without this.
export KUBECONFIG="$REPO/.e2e/kubeconfig"

assert_local() {
  local ctx server
  ctx=$(command kubectl config current-context 2>/dev/null || true)
  server=$(command kubectl config view --minify -o 'jsonpath={.clusters[0].cluster.server}' 2>/dev/null || true)
  if [[ "$ctx" != "kind-$CLUSTER" || "$server" != https://127.0.0.1:* ]]; then
    echo "refusing to continue: context '$ctx' server '$server' is not the local kind cluster" >&2
    exit 1
  fi
}
# Every kubectl and helm call goes through these functions, so none can reach another cluster.
kubectl() { assert_local; command kubectl "$@"; }
helm() { assert_local; command helm "$@"; }
log() { printf '\n==> %s\n' "$*"; }
