#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
ok_capd_verify_target
ok_capd_verify_provider_versions
bash "${OK_CAPD_ROOT}/scripts/workload-render.sh"

name="${OK_CAPD_WORKLOAD_NAME}"
manifest="/tmp/ok-capd-smoke.yaml"
workload_dir="${OK_CAPD_CONFIG_DIR}/workloads"
workload_kubeconfig="${workload_dir}/${name}.kubeconfig"

[[ ! -L "$workload_dir" ]] || ok_capd_fail "workload kubeconfig directory may not be a symlink: $workload_dir"
if [[ -e "$workload_dir" && ! -d "$workload_dir" ]]; then
  ok_capd_fail "workload kubeconfig path exists but is not a directory: $workload_dir"
fi
(umask 077; mkdir -p "$workload_dir")
chmod 700 "$workload_dir"
[[ ! -L "$workload_kubeconfig" ]] || ok_capd_fail "workload kubeconfig may not be a symlink: $workload_kubeconfig"

if ok_capd_kubectl get cluster "$name" >/dev/null 2>&1; then
  printf 'Preserving existing CAPI workload Cluster %s; no manifests reapplied.\n' "$name"
else
  for resource in "devcluster.infrastructure.cluster.x-k8s.io/$name" \
                  "kubeadmcontrolplane.controlplane.cluster.x-k8s.io/$name-control-plane" \
                  "machinedeployment.cluster.x-k8s.io/$name-md-0"; do
    if ok_capd_kubectl get "$resource" >/dev/null 2>&1; then
      ok_capd_fail "orphaned workload resource exists: $resource; inspect manually before continuing"
    fi
  done

  printf '=== Server-side validation ===\n'
  ok_capd_kubectl apply --dry-run=server -f "$manifest"
  printf '\n=== Applying CAPD workload resources ===\n'
  ok_capd_kubectl apply -f "$manifest"
fi

printf '\n=== Waiting for Cluster Ready ===\n'
ok_capd_kubectl wait --for=condition=Available=True "cluster.cluster.x-k8s.io/$name" --timeout=20m
printf '\n=== Waiting for control plane Ready ===\n'
ok_capd_kubectl wait --for=condition=Available=True "kubeadmcontrolplane.controlplane.cluster.x-k8s.io/$name-control-plane" --timeout=20m
printf '\n=== Waiting for worker MachineDeployment Ready ===\n'
ok_capd_kubectl wait --for=condition=Available=True "machinedeployment.cluster.x-k8s.io/$name-md-0" --timeout=20m

ok_capd_kubectl get secret "$name-kubeconfig" -o name >/dev/null || ok_capd_fail "workload kubeconfig Secret $name-kubeconfig was not created"
secret_json="$(ok_capd_kubectl get secret "$name-kubeconfig" -o json)"
encoded="$(jq -er '.data.value' <<<"$secret_json")" || ok_capd_fail "workload kubeconfig Secret has no data.value"
tmp_kubeconfig="$(mktemp "$workload_dir/.kubeconfig.XXXXXXXX")"
if ! printf '%s' "$encoded" | python3 -c 'import base64,sys; sys.stdout.buffer.write(base64.b64decode(sys.stdin.read(), validate=True))' > "$tmp_kubeconfig"; then
  rm -f "$tmp_kubeconfig"
  ok_capd_fail "failed to decode workload kubeconfig"
fi
chmod 600 "$tmp_kubeconfig"
mv -f "$tmp_kubeconfig" "$workload_kubeconfig"

printf '\n=== Waiting for workload Kubernetes nodes ===\n'
kubectl --kubeconfig "$workload_kubeconfig" wait --for=condition=Ready nodes --all --timeout=15m
printf '\n=== Workload nodes ===\n'
kubectl --kubeconfig "$workload_kubeconfig" get nodes -o wide

printf '\nPASS: CAPD workload cluster %s is Ready.\n' "$name"
printf 'Workload kubeconfig: %s\n' "$workload_kubeconfig"
