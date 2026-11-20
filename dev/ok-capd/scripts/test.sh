#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

"${OK_CAPD_ROOT}/scripts/preflight.sh"
ok_capd_verify_target
ok_capd_verify_provider_versions

for item in "capi-system/capi-controller-manager" \
            "capi-kubeadm-bootstrap-system/capi-kubeadm-bootstrap-controller-manager" \
            "capi-kubeadm-control-plane-system/capi-kubeadm-control-plane-controller-manager" \
            "capd-system/capd-controller-manager" \
            "cert-manager/cert-manager" \
            "cert-manager/cert-manager-cainjector" \
            "cert-manager/cert-manager-webhook"; do
  ns="${item%%/*}"
  deployment="${item#*/}"
  ok_capd_kubectl -n "$ns" rollout status "deployment/$deployment" --timeout=180s
done

node="${OK_CAPD_KIND_NAME}-control-plane"
docker exec "$node" test -S /var/run/docker.sock || ok_capd_fail "Docker socket is not mounted in the Kind node"
ok_capd_kubectl -n capd-system get deployment capd-controller-manager -o json \
  | jq -e '.spec.template.spec.volumes[] | select(.hostPath.path == "/var/run/docker.sock")' >/dev/null \
  || ok_capd_fail "CAPD controller deployment does not mount the Docker socket"

printf '\nPASS: CAPI/CAPD controllers are available and CAPD has Docker socket access.\n'
printf 'NOTE: this is a bootstrap smoke test; creating a CAPD workload cluster is the next implementation step.\n'
