#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
printf 'Docker context: %s\n' "$(docker context show)"
printf 'Kind cluster: %s\n' "${OK_CAPD_KIND_NAME}"
printf 'Node image: %s\n' "${OK_CAPD_NODE_IMAGE}"
printf 'CAPI/CAPD: %s / %s\n' "${OK_CAPD_CAPI_VERSION}" "${OK_CAPD_CAPD_VERSION}"
printf 'Kubernetes node version: %s\n' "${OK_CAPD_KUBERNETES_VERSION}"
printf 'cert-manager: %s\n' "${OK_CAPD_CERT_MANAGER_VERSION}"
kind version
clusterctl version
clusterctl version | grep -F "${OK_CAPD_CAPI_VERSION}" >/dev/null || ok_capd_fail "clusterctl must be ${OK_CAPD_CAPI_VERSION}"
docker manifest inspect "${OK_CAPD_NODE_IMAGE}" >/dev/null || ok_capd_fail "Kind node image is unavailable: ${OK_CAPD_NODE_IMAGE}"
printf 'PASS: preflight completed; no cluster resources changed\n'
