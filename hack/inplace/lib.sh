# shellcheck shell=bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# Shared helpers of the in-place upgrade tooling. Every command names its
# kubeconfig and context; the current context is never used.
#   MGMT_KUBECONFIG, MGMT_CONTEXT  management cluster (required)
#   NS, CLUSTER                     namespace and Cluster of the workload (required)
set -euo pipefail

: "${MGMT_KUBECONFIG:?set MGMT_KUBECONFIG}" "${MGMT_CONTEXT:?set MGMT_CONTEXT}" "${NS:?set NS}" "${CLUSTER:?set CLUSTER}"
# HERE is the directory of the tooling, for the scripts that source this file.
# shellcheck disable=SC2034
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
TMPD=$(mktemp -d)
chmod 700 "$TMPD"
trap 'rm -rf -- "$TMPD"' EXIT

k() { kubectl --kubeconfig "$MGMT_KUBECONFIG" --context "$MGMT_CONTEXT" --request-timeout=20s "$@"; }

# w runs kubectl against the workload cluster with its kubeconfig Secret, kept
# only in the private temporary directory of this run.
w() {
	if [ ! -s "$TMPD/workload.kubeconfig" ]; then
		k -n "$NS" get secret "$CLUSTER-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$TMPD/workload.kubeconfig"
		chmod 600 "$TMPD/workload.kubeconfig"
	fi
	kubectl --kubeconfig "$TMPD/workload.kubeconfig" --request-timeout=20s "$@"
}

# etcdctl_cp runs etcdctl in the etcd pod of a control-plane node.
etcdctl_cp() {
	local node=$1
	shift
	w -n kube-system exec "etcd-$node" -- etcdctl \
		--cacert=/etc/kubernetes/pki/etcd/ca.crt --cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt \
		--key=/etc/kubernetes/pki/etcd/healthcheck-client.key --command-timeout=5s "$@"
}

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
