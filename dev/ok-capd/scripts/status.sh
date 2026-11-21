#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
ok_capd_verify_target
printf '=== Management cluster ===\n'
printf 'Docker context: %s\n' "$(docker context show)"
printf 'Kind cluster: %s\n' "${OK_CAPD_KIND_NAME}"
printf 'Kubeconfig: %s\n' "${OK_CAPD_KUBECONFIG}"
ok_capd_kubectl get nodes -o wide
printf '\n=== CAPI providers ===\n'
if ok_capd_kubectl get crd providers.clusterctl.cluster.x-k8s.io >/dev/null 2>&1; then
  ok_capd_kubectl get providers.clusterctl.cluster.x-k8s.io -A
else
  printf 'CAPI provider inventory is not installed yet\n'
fi
printf '\n=== Controller deployments ===\n'
ok_capd_kubectl get deployments -A
printf '\n=== CAPI workload clusters ===\n'
ok_capd_kubectl get clusters.cluster.x-k8s.io -A || true
