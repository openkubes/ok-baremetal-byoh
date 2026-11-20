#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
ok_capd_verify_target

name="${OK_CAPD_WORKLOAD_NAME}"
state_dir="$HOME/.local/state/ok/capd/diagnostics"
timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
report="$state_dir/workload-${name}-${timestamp}.log"

[[ ! -L "$state_dir" ]] || ok_capd_fail "diagnostics state directory may not be a symlink: $state_dir"
(umask 077; mkdir -p "$state_dir")
chmod 700 "$state_dir"
(umask 077; touch "$report")
chmod 600 "$report"

capture() {
  local title="$1"
  shift
  printf '\n===== %s =====\n' "$title" | tee -a "$report"
  set +e
  "$@" 2>&1 | tee -a "$report"
  local command_status="${PIPESTATUS[0]}"
  set -e
  if [[ "$command_status" -ne 0 ]]; then
    printf 'WARN: command exited with status %s; continuing diagnostics.\n' "$command_status" | tee -a "$report"
  fi
}

printf 'OpenKubes CAPD workload diagnostics\n' | tee -a "$report"
printf 'UTC timestamp: %s\n' "$timestamp" | tee -a "$report"
printf 'Management cluster: %s\n' "$OK_CAPD_KIND_NAME" | tee -a "$report"
printf 'Workload cluster: %s\n' "$name" | tee -a "$report"
printf 'Docker context: %s\n' "$(docker context show)" | tee -a "$report"
printf 'Report: %s\n' "$report" | tee -a "$report"

cluster_conditions() {
  ok_capd_kubectl get cluster "$name" -o json | jq '{
    phase: .status.phase,
    infrastructureReady: .status.infrastructureReady,
    controlPlaneReady: .status.controlPlaneReady,
    conditions: .status.conditions
  }'
}

capture "1. CAPI Cluster conditions" cluster_conditions
capture "2a. KubeadmControlPlane" \
  ok_capd_kubectl get kubeadmcontrolplane "$name-control-plane" -o yaml
capture "2b. CAPI Machines for this workload" \
  ok_capd_kubectl get machines -A -l "cluster.x-k8s.io/cluster-name=$name" -o wide
capture "2c. CAPD DevMachines for this workload" \
  ok_capd_kubectl get devmachines -A -l "cluster.x-k8s.io/cluster-name=$name" -o wide
capture "2d. CAPD DevCluster" \
  ok_capd_kubectl get devcluster "$name" -o yaml
capture "3a. ClusterResourceSets and bindings" \
  ok_capd_kubectl get clusterresourcesets,clusterresourcesetbindings -A
capture "3b. CNI ConfigMap" \
  ok_capd_kubectl get configmap "cni-$name-crs-0" -o yaml
capture "4a. CAPD Docker containers" \
  docker ps -a --filter "label=cluster.x-k8s.io/cluster-name=$name"
capture "4b. CAPD controller logs (last 30 minutes, max 150 lines)" \
  ok_capd_kubectl -n capd-system logs deployment/capd-controller-manager --since=30m --tail=150
recent_events() {
  ok_capd_kubectl get events -A --sort-by=.metadata.creationTimestamp | tail -80
}

capture "5. Kubernetes events (last 80 across namespaces, newest last)" recent_events

printf '\nDiagnostics complete. No Kubernetes or Docker resources were modified.\n' | tee -a "$report"
printf 'Saved report: %s\n' "$report"
