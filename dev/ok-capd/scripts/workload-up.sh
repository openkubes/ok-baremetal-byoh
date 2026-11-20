#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
for tool in curl python3; do
  command -v "$tool" >/dev/null 2>&1 || ok_capd_fail "required tool not found: $tool"
done
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

printf '\n=== Configure host-reachable workload API endpoint ===\n'
# CAPD publishes the load balancer API port on the Docker host. On macOS
# with Colima, the Docker bridge IP is inside the Linux VM and is not directly
# routable from macOS. Prefer the published loopback port when it is reachable.
lb_container="${name}-lb"
published_port="$(docker port "$lb_container" 6443/tcp 2>/dev/null | head -1 | awk -F: '{print $NF}')"
if [[ "$published_port" =~ ^[0-9]+$ ]] && curl -kfsS --connect-timeout 5 \
    "https://127.0.0.1:$published_port/readyz" >/dev/null; then
  cluster_entry="$(kubectl --kubeconfig "$workload_kubeconfig" config view --minify -o jsonpath='{.contexts[0].context.cluster}')"
  [[ -n "$cluster_entry" ]] || ok_capd_fail "cannot determine workload kubeconfig cluster entry"
  kubectl --kubeconfig "$workload_kubeconfig" config set-cluster "$cluster_entry" \
    --server="https://127.0.0.1:$published_port" >/dev/null
  api_ready="$(kubectl --kubeconfig "$workload_kubeconfig" get --raw=/readyz)" \
    || ok_capd_fail "published workload API endpoint is reachable by TCP but kubeconfig TLS/API verification failed"
  [[ "$api_ready" == "ok" ]] || ok_capd_fail "workload API /readyz did not return ok: $api_ready"
  printf 'Using host-reachable API endpoint https://127.0.0.1:%s\n' "$published_port"
else
  ok_capd_fail "CAPD load-balancer API port is not reachable via localhost; inspect Docker port publishing and Colima networking"
fi

printf '\n=== Waiting for workload Kubernetes nodes ===\n'
kubectl --kubeconfig "$workload_kubeconfig" wait --for=condition=Ready nodes --all --timeout=15m
printf '\n=== Workload nodes ===\n'
kubectl --kubeconfig "$workload_kubeconfig" get nodes -o wide

printf '\nPASS: CAPD workload cluster %s is Ready.\n' "$name"
printf 'Workload kubeconfig: %s\n' "$workload_kubeconfig"
