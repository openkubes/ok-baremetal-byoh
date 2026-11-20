#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
ok_capd_verify_target
name="${OK_CAPD_WORKLOAD_NAME}"
workload_kubeconfig="${OK_CAPD_CONFIG_DIR}/workloads/${name}.kubeconfig"

ok_capd_kubectl get cluster "$name" >/dev/null 2>&1 || ok_capd_fail "workload Cluster $name does not exist"

printf 'This will delete only CAPI workload Cluster %s and its CAPD-managed infrastructure.\n' "$name"
printf 'The management Kind cluster %s will be preserved.\n' "$OK_CAPD_KIND_NAME"
[[ "${OK_CAPD_CONFIRM_WORKLOAD_DELETE:-}" == "$name" ]] || ok_capd_fail "set OK_CAPD_CONFIRM_WORKLOAD_DELETE=$name to authorize this workload deletion"

ok_capd_kubectl delete cluster "$name" --wait=false
ok_capd_kubectl wait --for=delete "cluster.cluster.x-k8s.io/$name" --timeout=20m

remaining="$(ok_capd_kubectl get machines -l "cluster.x-k8s.io/cluster-name=$name" -o name)"
[[ -z "$remaining" ]] || ok_capd_fail "Machines remain after Cluster deletion: $remaining"

remaining_docker="$(docker ps -aq --filter "label=cluster.x-k8s.io/cluster-name=$name")"
[[ -z "$remaining_docker" ]] || ok_capd_fail "CAPD Docker containers remain after Cluster deletion: $remaining_docker"

if [[ -f "$workload_kubeconfig" && ! -L "$workload_kubeconfig" ]]; then
  rm -f -- "$workload_kubeconfig"
fi
printf 'PASS: workload Cluster %s and its CAPD infrastructure are deleted. Management cluster preserved.\n' "$name"
