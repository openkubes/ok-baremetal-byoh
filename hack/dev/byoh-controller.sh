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
SOURCE="${BYOH_CONTROLLER_SOURCE:-local}"
case "$SOURCE" in
  local)
    IMAGE="localhost/openkubes/byoh-controller:git-$REV"
    ;;
  ghcr)
    DIGEST="${BYOH_CONTROLLER_DIGEST:-}"
    [[ "$DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || {
      echo "STOP: GHCR requires a full sha256 digest" >&2
      exit 1
    }
    IMAGE="ghcr.io/openkubes/byoh-controller@$DIGEST"
    ;;
  *)
    echo "STOP: unsupported BYOH_CONTROLLER_SOURCE" >&2
    exit 1
    ;;
esac

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
  [[ "$SOURCE" == local ]] ||
    fail "local build is only available for local controller images"
  need docker
  clean_source
  [[ -n "$(sed -n 's/^toolchain go//p' "$ROOT/go.mod")" ]] || fail "Go toolchain directive missing"
  docker build --build-arg GO_VERSION="$(sed -n 's/^toolchain go//p' "$ROOT/go.mod")" -t "$IMAGE" "$ROOT"
  echo "Built $IMAGE"
}
load() {
  [[ "$SOURCE" == local ]] ||
    fail "kind load is only available for local controller images"
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
  if [[ "$SOURCE" == local ]]; then
    check_loaded
  fi
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

  if [[ "$SOURCE" == ghcr ]]; then
    kubectl --kubeconfig "$KC" -n byoh-system get pods \
      -l cluster.x-k8s.io/provider=infrastructure-byoh,control-plane=controller-manager \
      -o json | EXPECTED_IMAGE="$IMAGE" python3 -c '
import json
import os
import re
import sys

data = json.load(sys.stdin)
expected = os.environ["EXPECTED_IMAGE"]

ready = []
for pod in data["items"]:
    if pod.get("metadata", {}).get("deletionTimestamp"):
        continue
    spec = pod.get("spec", {})
    containers = {
        c["name"]: c for c in spec.get("containers", [])
    }
    statuses = {
        c["name"]: c
        for c in pod.get("status", {}).get("containerStatuses", [])
    }
    manager = containers.get("manager")
    status = statuses.get("manager")
    if not manager or manager.get("image") != expected:
        continue
    if not status or not status.get("ready"):
        continue
    image_id = status.get("imageID", "")
    if not re.search(r"sha256:[0-9a-f]{64}", image_id):
        continue
    ready.append((pod["metadata"]["name"], image_id))

if len(ready) != 1:
    sys.exit("STOP: expected exactly one ready GHCR controller pod with a valid imageID")

print("PASS: GHCR controller pod", ready[0][0])
print("Runtime imageID:", ready[0][1])
' || fail "GHCR runtime image verification failed"
  fi

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
  if [[ "$SOURCE" == local ]]; then
    build
    load
  fi
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
