#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# Read-only monitor during an in-place upgrade: /readyz through the API endpoint
# every second, and every 5 seconds the etcd members (leader, term, index), the
# Nodes and their kubelets. Runs until STOP_FILE exists or MAX_SECONDS pass.
# Usage: monitor.sh OUT_DIR STOP_FILE [MAX_SECONDS]
. "$(dirname "$0")/lib.sh"
OUT=${1:?usage: monitor.sh OUT_DIR STOP_FILE [MAX_SECONDS]}
STOP=${2:?usage}
MAX=${3:-7200}
umask 077
end=$(($(date +%s) + MAX))
i=0
cp1=$(w get nodes -l node-role.kubernetes.io/control-plane -o jsonpath='{.items[0].metadata.name}')
while [ ! -e "$STOP" ] && [ "$(date +%s)" -lt "$end" ]; do
	start=$(python3 -c 'import time; print(time.time())')
	if out=$(w --request-timeout=3s get --raw /readyz 2>&1); then rc=0; else rc=$?; fi
	ms=$(python3 -c "import time; print(int((time.time()-$start)*1000))")
	printf '%s rc=%s ms=%s %s\n' "$(ts)" "$rc" "$ms" "$(tr '\n' ' ' <<<"${out:0:60}")" >> "$OUT/api-probe.log"
	if [ $((i % 5)) -eq 0 ]; then
		{
			echo "== $(ts)"
			etcdctl_cp "$cp1" endpoint status --cluster -w table 2>&1 || true
			w get nodes -o jsonpath='{range .items[*]}{.metadata.name}={.status.conditions[?(@.type=="Ready")].status}/{.status.nodeInfo.kubeletVersion}/{.spec.unschedulable} {end}{"\n"}' 2>&1 || true
		} >> "$OUT/etcd-nodes.log"
	fi
	i=$((i + 1))
	sleep 1
done
echo "$(ts) monitor ended" >> "$OUT/api-probe.log"
