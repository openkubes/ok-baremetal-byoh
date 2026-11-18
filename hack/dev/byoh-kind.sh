#!/usr/bin/env bash
set -euo pipefail

# Manual local bootstrap only. No BYOH controller, host enrollment, or teardown.
NAME=ok-byoh-dev
IMAGE=kindest/node:v1.36.4
K8S=v1.36.4
CAPI=v1.14.4
KUBECONFIG_FILE="${HOME}/.config/ok/byoh-dev/kubeconfig"
CTX="kind-${NAME}"

fail() { printf 'STOP: %s\n' "$*" >&2; exit 1; }
require_tools() {
  local tool
  for tool in kind docker kubectl clusterctl; do
    command -v "$tool" >/dev/null 2>&1 || fail "missing tool: $tool"
  done
  docker info >/dev/null 2>&1 || fail 'Docker daemon unavailable'
}
cluster_exists() { kind get clusters | grep -Fxq -- "$NAME"; }
check_target() {
  cluster_exists || fail "Kind cluster $NAME not found in current Docker context"
  [[ -f "$KUBECONFIG_FILE" && ! -L "$KUBECONFIG_FILE" ]] || fail "missing/unsafe dedicated kubeconfig: $KUBECONFIG_FILE"
  local current server actual_server node_names actual_version
  current="$(kubectl --kubeconfig "$KUBECONFIG_FILE" config current-context)"
  [[ "$current" == "$CTX" ]] || fail "unexpected context: $current"
  server="$(kubectl --kubeconfig "$KUBECONFIG_FILE" config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
  actual_server="$(kind get kubeconfig --name "$NAME" | kubectl --kubeconfig /dev/stdin config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
  [[ -n "$server" && "$server" == "$actual_server" ]] || fail 'Kubeconfig API server does not match Kind'
  node_names="$(kubectl --kubeconfig "$KUBECONFIG_FILE" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
  [[ "$node_names" == "${NAME}-control-plane" ]] || fail "unexpected nodes: $node_names"
  actual_version="$(kubectl --kubeconfig "$KUBECONFIG_FILE" get node "${NAME}-control-plane" -o jsonpath='{.status.nodeInfo.kubeletVersion}')"
  [[ "$actual_version" == "$K8S" ]] || fail "unexpected Kubernetes version: $actual_version"
  printf 'Verified: %s (%s; %s; %s)\n' "$NAME" "$CTX" "$K8S" "$server"
}
preflight() {
  require_tools
  printf 'Docker context: %s\nTarget: %s (%s), CAPI %s\nKubeconfig: %s\n' "$(docker context show)" "$NAME" "$IMAGE" "$CAPI" "$KUBECONFIG_FILE"
  kind get clusters
  kind version
  clusterctl version
  docker manifest inspect "$IMAGE" >/dev/null || fail "image unavailable: $IMAGE"
  echo 'Preflight passed (no Kubernetes writes)'
}
create() {
  require_tools
  cluster_exists && fail "$NAME already exists; use make byoh-kind-status"
  [[ ! -e "$KUBECONFIG_FILE" && ! -L "$KUBECONFIG_FILE" ]] || fail "kubeconfig already exists: $KUBECONFIG_FILE"
  docker manifest inspect "$IMAGE" >/dev/null || fail "image unavailable: $IMAGE"
  (umask 077; mkdir -p -- "$(dirname "$KUBECONFIG_FILE")")
  chmod 700 "$(dirname "$KUBECONFIG_FILE")"
  env -u KUBECONFIG KIND_EXPERIMENTAL_PROVIDER=docker kind create cluster --name "$NAME" --image "$IMAGE" --kubeconfig "$KUBECONFIG_FILE" --wait 120s
  chmod 600 "$KUBECONFIG_FILE"
  check_target
}
status() {
  require_tools
  check_target
  kubectl --kubeconfig "$KUBECONFIG_FILE" get nodes -o wide
  kubectl --kubeconfig "$KUBECONFIG_FILE" get deployments -A
  if kubectl --kubeconfig "$KUBECONFIG_FILE" get crd providers.clusterctl.cluster.x-k8s.io >/dev/null 2>&1; then
    kubectl --kubeconfig "$KUBECONFIG_FILE" get providers.clusterctl.cluster.x-k8s.io -A
  else
    echo 'CAPI Provider CRD not installed'
  fi
}
verify_capi() {
  require_tools
  check_target
  local ns count
  for ns in capi-system capi-kubeadm-bootstrap-system capi-kubeadm-control-plane-system cert-manager; do
    kubectl --kubeconfig "$KUBECONFIG_FILE" -n "$ns" rollout status deployment --timeout=120s
  done
  kubectl --kubeconfig "$KUBECONFIG_FILE" get providers.clusterctl.cluster.x-k8s.io -A
  count="$(kubectl --kubeconfig "$KUBECONFIG_FILE" get providers.clusterctl.cluster.x-k8s.io -A -o jsonpath='{range .items[*]}{.spec.version}{"\n"}{end}' | grep -Fxc "$CAPI" || true)"
  [[ "$count" == 3 ]] || fail "expected exactly three CAPI $CAPI providers; found $count"
  printf 'CAPI %s ready on %s\n' "$CAPI" "$NAME"
}
init_capi() {
  require_tools
  check_target
  if kubectl --kubeconfig "$KUBECONFIG_FILE" get crd providers.clusterctl.cluster.x-k8s.io >/dev/null 2>&1; then
    fail 'CAPI provider inventory already exists; refusing to modify/reinitialize it'
  fi
  local ns
  for ns in capi-system capi-kubeadm-bootstrap-system capi-kubeadm-control-plane-system cert-manager; do
    if kubectl --kubeconfig "$KUBECONFIG_FILE" get namespace "$ns" >/dev/null 2>&1; then
      fail "namespace $ns already exists; inspect state before installing"
    fi
  done
  clusterctl init --kubeconfig "$KUBECONFIG_FILE" --core "cluster-api:$CAPI" --bootstrap "kubeadm:$CAPI" --control-plane "kubeadm:$CAPI"
  verify_capi
}
case "${1:-}" in
  preflight) preflight ;;
  create) create ;;
  status) status ;;
  capi-init) init_capi ;;
  capi-verify) verify_capi ;;
  *) fail 'usage: byoh-kind.sh {preflight|create|status|capi-init|capi-verify}' ;;
esac
