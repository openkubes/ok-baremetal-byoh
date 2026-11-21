#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
ok_capd_verify_target

[[ "${OK_CAPD_CONFIRM_DELETE:-}" == "${OK_CAPD_KIND_NAME}" ]] || ok_capd_fail "set OK_CAPD_CONFIRM_DELETE=${OK_CAPD_KIND_NAME} to authorize deletion of this exact disposable Kind cluster"

clusters="$(ok_capd_kubectl get clusters.cluster.x-k8s.io -A -o name)"
[[ -z "$clusters" ]] || ok_capd_fail "CAPI workload clusters still exist; delete and verify them through CAPI before cleaning the management cluster: $clusters"

machines="$(ok_capd_kubectl get machines.cluster.x-k8s.io -A -o name)"
[[ -z "$machines" ]] || ok_capd_fail "CAPI Machines still exist without a Cluster; inspect and resolve them before cleanup: $machines"

docker_containers="$(docker ps -aq --filter label=cluster.x-k8s.io/cluster-name)"
[[ -z "$docker_containers" ]] || ok_capd_fail "Docker containers carrying CAPI cluster labels still exist; inspect them before cleanup: $docker_containers"

printf 'Deleting only Kind cluster %s\n' "${OK_CAPD_KIND_NAME}"
kind delete cluster --name "${OK_CAPD_KIND_NAME}"
rm -f -- "${OK_CAPD_KUBECONFIG}"
printf 'PASS: deleted %s and its dedicated kubeconfig. No other Kind clusters were targeted.\n' "${OK_CAPD_KIND_NAME}"
