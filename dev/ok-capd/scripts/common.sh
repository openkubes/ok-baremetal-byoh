#!/usr/bin/env bash
set -euo pipefail

OK_CAPD_SCRIPT_DIR="$(dirname "$BASH_SOURCE")"
OK_CAPD_ROOT="$(cd "$OK_CAPD_SCRIPT_DIR/.." && pwd)"
# shellcheck source=../versions.env
source "$OK_CAPD_ROOT/versions.env"

OK_CAPD_CONFIG_DIR="$HOME/.config/ok/capd"
OK_CAPD_KUBECONFIG="$OK_CAPD_CONFIG_DIR/kubeconfig"
OK_CAPD_CONTEXT="kind-$OK_CAPD_KIND_NAME"
OK_CAPD_KIND_CONFIG="$OK_CAPD_ROOT/config/kind.yaml"
OK_CAPD_CLUSTERCTL_CONFIG="$OK_CAPD_ROOT/config/providers.yaml"

ok_capd_fail() {
  printf 'STOP: ok-capd: %s\n' "$*" >&2
  exit 1
}

ok_capd_require_tools() {
  local tool docker_os
  for tool in docker kind kubectl clusterctl jq; do
    command -v "$tool" >/dev/null 2>&1 || ok_capd_fail "required tool not found: $tool"
  done
  docker info >/dev/null 2>&1 || ok_capd_fail "Docker daemon is unavailable"
  docker_os="$(docker info --format '{{.OSType}}')"
  [[ "$docker_os" == "linux" ]] || ok_capd_fail "Docker daemon must provide Linux containers"
}

ok_capd_kind_exists() {
  kind get clusters 2>/dev/null | grep -Fxq -- "$OK_CAPD_KIND_NAME"
}

ok_capd_verify_target() {
  ok_capd_kind_exists || ok_capd_fail "Kind cluster $OK_CAPD_KIND_NAME does not exist"
  [[ -f "$OK_CAPD_KUBECONFIG" && ! -L "$OK_CAPD_KUBECONFIG" ]] || ok_capd_fail "missing or unsafe dedicated kubeconfig: $OK_CAPD_KUBECONFIG"

  local current server kind_kubeconfig actual_server nodes
  current="$(kubectl --kubeconfig "$OK_CAPD_KUBECONFIG" config current-context)"
  [[ "$current" == "$OK_CAPD_CONTEXT" ]] || ok_capd_fail "unexpected kubeconfig context: $current"
  server="$(kubectl --kubeconfig "$OK_CAPD_KUBECONFIG" config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
  kind_kubeconfig="$(kind get kubeconfig --name "$OK_CAPD_KIND_NAME")"
  actual_server="$(printf '%s\n' "$kind_kubeconfig" | kubectl --kubeconfig /dev/stdin config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
  [[ -n "$server" && "$server" == "$actual_server" ]] || ok_capd_fail "kubeconfig API server does not match the named Kind cluster"
  nodes="$(kubectl --kubeconfig "$OK_CAPD_KUBECONFIG" get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
  [[ "$nodes" == "$OK_CAPD_KIND_NAME-control-plane" ]] || ok_capd_fail "unexpected management-cluster nodes: $nodes"
}

ok_capd_kubectl() {
  kubectl --kubeconfig "$OK_CAPD_KUBECONFIG" "$@"
}

ok_capd_verify_provider_versions() {
  local inventory
  inventory="$(ok_capd_kubectl get providers.clusterctl.cluster.x-k8s.io -A -o json)"
  jq -e --arg v "$OK_CAPD_CAPI_VERSION" '
    [
      .items[]
      | select(
          (.metadata.name == "cluster-api")
          or (.metadata.name == "bootstrap-kubeadm")
          or (.metadata.name == "control-plane-kubeadm")
          or (.metadata.name == "infrastructure-docker")
        )
      | select(.spec.version == $v)
    ] | length == 4
  ' <<<"$inventory" >/dev/null || ok_capd_fail "expected core, kubeadm bootstrap, kubeadm control-plane and CAPD providers at $OK_CAPD_CAPI_VERSION"
}
