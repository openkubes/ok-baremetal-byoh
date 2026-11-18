#!/usr/bin/env bash
set -euo pipefail

# Local-only BYOH controller workflow. Never uses the ambient kubeconfig.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NAME="${BYOH_KIND_NAME:-ok-byoh-dev}"
case "$NAME" in
  ok-byoh-dev) SUBDIR=byoh-dev ;;
  ok-byoh-make-test) SUBDIR=byoh-make-test ;;
  *) echo "STOP: unsupported BYOH_KIND_NAME" >&2; exit 1 ;;
esac
KC="$HOME/.config/ok/$SUBDIR/kubeconfig"
CTX="kind-$NAME"
KUSTOMIZE="$ROOT/bin/kustomize-v5.7.0"
REV="$(git -C "$ROOT" rev-parse --short=12 HEAD)"
IMAGE="localhost/openkubes/byoh-controller:git-$REV"

fail() { echo "STOP: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing tool: $1"; }
check_target() {
  need kubectl; need kind; need docker
  BYOH_KIND_NAME="$NAME" bash "$ROOT/hack/dev/byoh-kind.sh" status >/dev/null ||
    fail "kind target identity check failed"
  [[ "$(kubectl --kubeconfig "$KC" config current-context)" == "$CTX" ]] ||
    fail "unexpected kind context"
}
check_capi() {
  BYOH_KIND_NAME="$NAME" bash "$ROOT/hack/dev/byoh-kind.sh" capi-verify >/dev/null ||
    fail "CAPI is not ready"
}
clean_source() {
  [[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=all)" ]] ||
    fail "source tree is not clean; commit changes before building a revision-tagged image"
}
build() {
  need docker
  clean_source
  [[ -n "$(sed -n 's/^toolchain go//p' "$ROOT/go.mod")" ]] || fail "Go toolchain directive missing"
  docker build --build-arg GO_VERSION="$(sed -n 's/^toolchain go//p' "$ROOT/go.mod")" -t "$IMAGE" "$ROOT"
  echo "Built $IMAGE"
}
load() {
  check_target
  docker image inspect "$IMAGE" >/dev/null || fail "missing local image $IMAGE; run byoh-controller-build"
  kind load docker-image "$IMAGE" --name "$NAME"
  check_loaded
}
check_loaded() {
  docker exec "$NAME-control-plane" crictl inspecti "$IMAGE" >/dev/null 2>&1 ||
    fail "image not available inside $NAME"
}
render() (
  need python3
  [[ -x "$KUSTOMIZE" ]] || fail "missing $KUSTOMIZE; run make kustomize"
  local file="$1" raw
  raw="$(mktemp)"
  trap 'rm -f "$raw"' EXIT
  "$KUSTOMIZE" build "$ROOT/config/default" > "$raw"
  python3 "$ROOT/hack/dev/byoh-controller-manifest.py" "$raw" "$file" "$IMAGE"
)
deploy() (
  check_target
  check_capi
  check_loaded
  local manifest
  manifest="$(mktemp)"
  trap 'rm -f "$manifest"' EXIT
  render "$manifest"
  kubectl --kubeconfig "$KC" apply --dry-run=client -f "$manifest" -o name >/dev/null ||
    fail "manifest dry-run failed"
  # Revalidate immediately before the cluster-scoped write.
  check_target
  kubectl --kubeconfig "$KC" apply -f "$manifest"
  verify
)
verify() {
  check_target
  check_capi
  local actual mode
  actual="$(kubectl --kubeconfig "$KC" -n byoh-system get deployment byoh-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}')"
  [[ "$actual" == "$IMAGE" ]] || fail "unexpected controller image: $actual (expected $IMAGE)"
  mode="$(kubectl --kubeconfig "$KC" -n byoh-system get deployment byoh-controller-manager -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MANUAL_CSR_APPROVAL")].value}')"
  [[ "$mode" == enable ]] || fail "automatic CSR approval must be disabled"
  kubectl --kubeconfig "$KC" -n byoh-system rollout status deployment/byoh-controller-manager --timeout=180s
  local permission
  for permission in 'create secrets' 'get secrets' 'delete secrets'; do
    kubectl --kubeconfig "$KC" auth can-i $permission -n kube-system \
      --as=system:serviceaccount:byoh-system:byoh-controller-manager |
      grep -Fxq yes || fail "missing controller RBAC: $permission in kube-system"
  done
  echo "PASS: BYOH controller $IMAGE ready in $CTX; manual CSR approval"
}
up() {
  if kind get clusters | grep -Fxq "$NAME"; then
    check_target
  else
    BYOH_KIND_NAME="$NAME" bash "$ROOT/hack/dev/byoh-kind.sh" create
  fi
  if kubectl --kubeconfig "$KC" get crd providers.clusterctl.cluster.x-k8s.io >/dev/null 2>&1; then
    check_capi
  else
    BYOH_KIND_NAME="$NAME" bash "$ROOT/hack/dev/byoh-kind.sh" capi-init
  fi
  build
  load
  deploy
}
case "${1:-}" in
  build) build ;;
  load) load ;;
  render) render "${2:?output manifest path required}" ;;
  deploy) deploy ;;
  verify) verify ;;
  up) up ;;
  *) fail "usage: $0 {build|load|render OUTPUT|deploy|verify|up}" ;;
esac
