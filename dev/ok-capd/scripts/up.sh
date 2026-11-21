#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

bash "${OK_CAPD_ROOT}/scripts/preflight.sh"

if ok_capd_kind_exists; then
  ok_capd_verify_target
  printf 'Preserving existing verified Kind cluster %s\n' "${OK_CAPD_KIND_NAME}"
else
  [[ ! -e "${OK_CAPD_KUBECONFIG}" && ! -L "${OK_CAPD_KUBECONFIG}" ]] || ok_capd_fail "kubeconfig path already exists; inspect before proceeding: ${OK_CAPD_KUBECONFIG}"
  (umask 077; mkdir -p "${OK_CAPD_CONFIG_DIR}")
  chmod 700 "${OK_CAPD_CONFIG_DIR}"
  env -u KUBECONFIG KIND_EXPERIMENTAL_PROVIDER=docker kind create cluster \
    --name "${OK_CAPD_KIND_NAME}" \
    --image "${OK_CAPD_NODE_IMAGE}" \
    --config "${OK_CAPD_KIND_CONFIG}" \
    --kubeconfig "${OK_CAPD_KUBECONFIG}" \
    --wait 180s
  chmod 600 "${OK_CAPD_KUBECONFIG}"
  ok_capd_verify_target
fi

if ok_capd_kubectl get crd providers.clusterctl.cluster.x-k8s.io >/dev/null 2>&1; then
  ok_capd_verify_provider_versions
else
  for ns in capi-system capi-kubeadm-bootstrap-system capi-kubeadm-control-plane-system capd-system cert-manager; do
    if ok_capd_kubectl get namespace "$ns" >/dev/null 2>&1; then
      ok_capd_fail "namespace $ns already exists without a complete provider inventory; inspect manually"
    fi
  done
  clusterctl init \
    --kubeconfig "${OK_CAPD_KUBECONFIG}" \
    --config "${OK_CAPD_CLUSTERCTL_CONFIG}" \
    --core "cluster-api:${OK_CAPD_CAPI_VERSION}" \
    --bootstrap "kubeadm:${OK_CAPD_CAPI_VERSION}" \
    --control-plane "kubeadm:${OK_CAPD_CAPI_VERSION}" \
    --infrastructure "docker:${OK_CAPD_CAPD_VERSION}" \
    --wait-providers
  ok_capd_verify_provider_versions
fi

bash "${OK_CAPD_ROOT}/scripts/status.sh"
printf '\nPASS: isolated CAPI + CAPD management cluster is ready. BYOH is not installed.\n'
