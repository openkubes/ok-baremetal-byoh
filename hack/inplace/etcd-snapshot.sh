#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# etcd snapshot before a control-plane upgrade, and its restore verification.
#   etcd-snapshot.sh plan
#       Read-only: the members, the image of etcd and what save would do.
#   etcd-snapshot.sh save OUT_DIR          (changes the host: separate approval)
#       Saves a snapshot on the first control-plane host, copies it with the
#       pinned SSH of SSH_CMD/SCP_CMD to OUT_DIR, and removes the copy on the host.
#   etcd-snapshot.sh verify SNAPSHOT ETCD_IMAGE
#       Local only: snapshot status and a restore into a throw-away directory,
#       with the etcd image of the cluster (DOCKER_CONTEXT selects the daemon).
. "$(dirname "$0")/lib.sh"
mode=${1:?usage: etcd-snapshot.sh plan|save OUT_DIR|verify SNAPSHOT ETCD_IMAGE}
cp1() { w get nodes -l node-role.kubernetes.io/control-plane -o jsonpath='{.items[0].metadata.name}'; }
case $mode in
plan)
	node=$(cp1)
	echo "first control-plane node: $node"
	etcdctl_cp "$node" member list -w table
	w -n kube-system get pod "etcd-$node" -o jsonpath='image {.spec.containers[0].image}{"\n"}'
	echo "save: etcdctl snapshot save /var/lib/etcd-snapshot/<ts>.db on $node, copy, sha256, remove on host"
	;;
save)
	out=${2:?OUT_DIR}
	: "${SSH_CMD:?SSH_CMD (pinned ssh to the first control-plane host)}" "${SCP_CMD:?SCP_CMD (pinned scp)}" "${SCP_HOST:?SCP_HOST user@ip}"
	name=etcd-$(date -u +%Y%m%dT%H%M%SZ).db
	$SSH_CMD "install -d -m 700 /var/lib/etcd-snapshot && ETCDCTL_API=3 crictl exec \$(crictl ps --name etcd -q | head -1) etcdctl --endpoints=https://127.0.0.1:2379 --cacert=/etc/kubernetes/pki/etcd/ca.crt --cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt --key=/etc/kubernetes/pki/etcd/healthcheck-client.key snapshot save /var/lib/etcd/$name && mv /var/lib/etcd/$name /var/lib/etcd-snapshot/$name && sha256sum /var/lib/etcd-snapshot/$name"
	(umask 077 && $SCP_CMD "$SCP_HOST:/var/lib/etcd-snapshot/$name" "$out/$name")
	(cd "$out" && shasum -a 256 "$name" | tee "$name.sha256")
	$SSH_CMD "rm -f /var/lib/etcd-snapshot/$name"
	;;
verify)
	snap=${2:?SNAPSHOT}
	image=${3:?ETCD_IMAGE}
	dir=$(cd "$(dirname "$snap")" && pwd)
	docker run --rm --network none -v "$dir:/in:ro" --entrypoint etcdutl "$image" snapshot status "/in/$(basename "$snap")" -w table
	docker run --rm --network none -v "$dir:/in:ro" --entrypoint etcdutl "$image" snapshot restore "/in/$(basename "$snap")" --data-dir /tmp/restore --skip-hash-check=false
	echo "RESTORE VERIFIED: $snap"
	;;
*)
	echo "unknown mode $mode" >&2
	exit 1
	;;
esac
