#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
ok_capd_verify_target
name="${OK_CAPD_WORKLOAD_NAME}"
workload_kubeconfig="${OK_CAPD_CONFIG_DIR}/workloads/${name}.kubeconfig"

printf '=== CAPI Cluster ===\n'
ok_capd_kubectl get cluster "$name" -o wide
printf '\n=== Control plane ===\n'
ok_capd_kubectl get kubeadmcontrolplane "$name-control-plane" -o wide
printf '\n=== MachineDeployment ===\n'
ok_capd_kubectl get machinedeployment "$name-md-0" -o wide
printf '\n=== Machines ===\n'
ok_capd_kubectl get machines -l "cluster.x-k8s.io/cluster-name=$name" -o wide
printf '\n=== CAPD infrastructure ===\n'
ok_capd_kubectl get devclusters,devmachines -l "cluster.x-k8s.io/cluster-name=$name" -o wide

if [[ -f "$workload_kubeconfig" && ! -L "$workload_kubeconfig" ]]; then
  printf '\n=== Workload Kubernetes nodes ===\n'
  kubectl --kubeconfig "$workload_kubeconfig" get nodes -o wide
else
  printf '\nWorkload kubeconfig not available yet: %s\n' "$workload_kubeconfig"
fi

printf '\n=== CAPD Docker containers ===\n'
docker ps -a --filter "name=$name-"
