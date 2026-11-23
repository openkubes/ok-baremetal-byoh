#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# Replacement and bypass attempts against the REAL management API, as server-side
# dry-runs: each request passes authentication, RBAC and the admission chain, and
# nothing is persisted (every call goes through dry(), which always adds
# --dry-run=server). A case PASSES only if the No-Replacement guard denies it (the
# denial names a no-replacement.openkubes.org webhook), never because of RBAC.
# Usage: negative-tests.sh [--expect-unprotected]
#   --expect-unprotected  baseline before the guard: report what is NOT yet denied
. "$(dirname "$0")/lib.sh"
expect=protected
[ "${1:-}" = --expect-unprotected ] && expect=unprotected

dry() { k --dry-run=server "$@" 2>&1; }

kcp=$(k -n "$NS" get kcp -l "cluster.x-k8s.io/cluster-name=$CLUSTER" -o jsonpath='{.items[0].metadata.name}')
md=$(k -n "$NS" get md -l "cluster.x-k8s.io/cluster-name=$CLUSTER" -o jsonpath='{.items[0].metadata.name}')
cpm=$(k -n "$NS" get machines -l "cluster.x-k8s.io/cluster-name=$CLUSTER,cluster.x-k8s.io/control-plane" -o jsonpath='{.items[0].metadata.name}')
wm=$(k -n "$NS" get machines -l "cluster.x-k8s.io/cluster-name=$CLUSTER,cluster.x-k8s.io/deployment-name=$md" -o jsonpath='{.items[0].metadata.name}')
k -n "$NS" get machine "$cpm" -o json | python3 -c 'import json,sys
m = json.load(sys.stdin)
for k in ("uid", "resourceVersion", "creationTimestamp", "managedFields", "ownerReferences", "generation"):
    m["metadata"].pop(k, None)
m["metadata"]["name"] = m["metadata"]["name"] + "-replacement"
m.pop("status", None)
print(json.dumps(m))' > "$TMPD/replacement.json"
printf '{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"okx-grant-forged","namespace":"%s"},"data":{"grantID":"forged"}}' "$NS" > "$TMPD/grant.json"

pass=0 fail=0
case_() { # case_ NAME OUTPUT
	local name=$1 out=$2 denied=no
	grep -q 'no-replacement.openkubes.org' <<<"$out" && grep -q -i 'denied' <<<"$out" && denied=yes
	if [ "$expect" = protected ]; then
		if [ $denied = yes ]; then echo "PASS  $name: $(grep -o 'denied the request: .*' <<<"$out" | head -1 | cut -c1-140)"; pass=$((pass + 1));
		else echo "FAIL  $name: $(head -1 <<<"$out" | cut -c1-160)"; fail=$((fail + 1)); fi
	else
		if [ $denied = yes ]; then echo "GUARDED     $name"; else echo "UNPROTECTED $name: $(head -1 <<<"$out" | cut -c1-120)"; fi
	fi
}

echo "== $(ts) $NS/$CLUSTER ($expect) KCP $kcp, MD $md, Machines $cpm $wm"
case_ "delete a control-plane Machine" "$(dry -n "$NS" delete machine "$cpm")"
case_ "delete a worker Machine" "$(dry -n "$NS" delete machine "$wm")"
case_ "create a replacement Machine" "$(dry create -f "$TMPD/replacement.json")"
case_ "scale the KCP to 1 (scale subresource)" "$(dry -n "$NS" scale kcp "$kcp" --replicas=1)"
case_ "scale the MD to 0 (scale subresource)" "$(dry -n "$NS" scale md "$md" --replicas=0)"
case_ "change the KCP replicas" "$(dry -n "$NS" patch kcp "$kcp" --type merge -p '{"spec":{"replicas":5}}')"
case_ "raise the KCP maxSurge" "$(dry -n "$NS" patch kcp "$kcp" --type merge -p '{"spec":{"rollout":{"strategy":{"rollingUpdate":{"maxSurge":2}}}}}')"
case_ "change the KCP machine template" "$(dry -n "$NS" patch kcp "$kcp" --type merge -p '{"spec":{"machineTemplate":{"spec":{"infrastructureRef":{"name":"other"}}}}}')"
case_ "change the KCP kubeadm configuration" "$(dry -n "$NS" patch kcp "$kcp" --type merge -p '{"spec":{"kubeadmConfigSpec":{"preKubeadmCommands":["true"]}}}')"
case_ "change the MD infrastructure template" "$(dry -n "$NS" patch md "$md" --type merge -p '{"spec":{"template":{"spec":{"infrastructureRef":{"name":"other"}}}}}')"
case_ "change the KCP version without a grant" "$(dry -n "$NS" patch kcp "$kcp" --type merge -p "{\"spec\":{\"version\":\"${TARGET:-v1.37.2}\"}}")"
case_ "delete the KCP" "$(dry -n "$NS" delete kcp "$kcp")"
case_ "delete the MD" "$(dry -n "$NS" delete md "$md")"
case_ "remove the protection label" "$(dry label ns "$NS" safety.openkubes.org/in-place-only-)"
case_ "delete the protected namespace" "$(dry delete ns "$NS")"
case_ "create a forged grant without the label" "$(dry create -f "$TMPD/grant.json")"
if [ "$expect" = protected ]; then
	echo "RESULT: $pass denied by the guard, $fail not"
	[ "$fail" -eq 0 ]
fi
