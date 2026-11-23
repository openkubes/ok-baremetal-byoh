# shellcheck shell=bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# Shared helpers of the in-place upgrade tooling. Every command names its
# kubeconfig and context; the current context is never used. Sourcing this file
# proves the identity of both clusters before any script talks to them.
#   MGMT_KUBECONFIG, MGMT_CONTEXT  management cluster (required)
#   MGMT_SERVER, MGMT_CA_SHA256     its expected API URL and sha256 of the context's
#                                   certificate-authority-data string (required)
#   NS, CLUSTER, CLUSTER_UID        namespace, name and UID of the workload Cluster (required)
set -euo pipefail

: "${MGMT_KUBECONFIG:?set MGMT_KUBECONFIG}" "${MGMT_CONTEXT:?set MGMT_CONTEXT}" "${NS:?set NS}" "${CLUSTER:?set CLUSTER}"
: "${MGMT_SERVER:?set MGMT_SERVER}" "${MGMT_CA_SHA256:?set MGMT_CA_SHA256}" "${CLUSTER_UID:?set CLUSTER_UID}"
# HERE is the directory of the tooling, for the scripts that source this file.
# shellcheck disable=SC2034
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
TMPD=$(mktemp -d)
chmod 700 "$TMPD"
trap 'rm -rf -- "$TMPD"' EXIT

k() { kubectl --kubeconfig "$MGMT_KUBECONFIG" --context "$MGMT_CONTEXT" --request-timeout=20s "$@"; }

# The management context: pinned server and CA, then the pinned UID of the workload Cluster.
kubectl --kubeconfig "$MGMT_KUBECONFIG" config view --raw -o json |
	python3 -I "$HERE/kubeconfig_identity.py" management "$MGMT_CONTEXT" "$MGMT_SERVER" "$MGMT_CA_SHA256" ||
	{ echo "refused: management kubeconfig identity" >&2; exit 1; }
_uid=$(k -n "$NS" get cluster "$CLUSTER" -o jsonpath='{.metadata.uid}')
[ "$_uid" = "$CLUSTER_UID" ] || { echo "refused: Cluster $NS/$CLUSTER has UID '$_uid', expected $CLUSTER_UID" >&2; exit 1; }

# The workload kubeconfig (from the Cluster's kubeconfig Secret, kept only in the
# private temporary directory of this run) must point at the Cluster's
# controlPlaneEndpoint and trust the CA of its <cluster>-ca Secret. Checked here, in
# the main shell, so that a refusal stops the script; w names its single context.
k -n "$NS" get secret "$CLUSTER-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$TMPD/workload.kubeconfig"
chmod 600 "$TMPD/workload.kubeconfig"
_endpoint=$(k -n "$NS" get cluster "$CLUSTER" -o jsonpath='https://{.spec.controlPlaneEndpoint.host}:{.spec.controlPlaneEndpoint.port}')
WCTX=$(kubectl --kubeconfig "$TMPD/workload.kubeconfig" config view --raw -o json |
	python3 -I "$HERE/kubeconfig_identity.py" workload "$_endpoint" "$(k -n "$NS" get secret "$CLUSTER-ca" -o jsonpath='{.data.tls\.crt}')") ||
	{ echo "refused: workload kubeconfig identity" >&2; exit 1; }

# w runs kubectl against the workload cluster.
w() { kubectl --kubeconfig "$TMPD/workload.kubeconfig" --context "$WCTX" --request-timeout=20s "$@"; }

# etcdctl_cp runs etcdctl in the etcd pod of a control-plane node.
etcdctl_cp() {
	local node=$1
	shift
	w -n kube-system exec "etcd-$node" -- etcdctl \
		--cacert=/etc/kubernetes/pki/etcd/ca.crt --cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt \
		--key=/etc/kubernetes/pki/etcd/healthcheck-client.key --command-timeout=5s "$@"
}

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
