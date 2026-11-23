#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# Read-only preflight of an in-place upgrade of a BYOH cluster (five hosts in the
# Scaleway lab). Usage: preflight.sh TARGET_VERSION [EXPECTED_CONTROL_PLANES EXPECTED_WORKERS]
# Exit 0 READY, 2 BLOCKED (every failed check is listed), 1 error.
# Optional: GW_SSH="ssh -o UserKnownHostsFile=... -o StrictHostKeyChecking=yes root@GW" adds the HAProxy check;
# OKX_CILIUM_PRERELEASE=<version> approves a pinned Cilium pre-release of cilium-compat.json for this run.
. "$(dirname "$0")/lib.sh"

TARGET=${1:?usage: preflight.sh TARGET_VERSION [CONTROL_PLANES WORKERS]}
CPS=${2:-3}
WORKERS=${3:-2}
fails=0
check() { # check NAME OK DETAIL
	if [ "$2" = ok ]; then printf 'PASS  %-34s %s\n' "$1" "$3"; else printf 'FAIL  %-34s %s\n' "$1" "$3"; fails=$((fails + 1)); fi
}
is() { [ "$1" = "$2" ] && echo ok || echo no; }

echo "== $(ts) preflight $NS/$CLUSTER -> $TARGET (context $MGMT_CONTEXT)"
avail=$(k -n "$NS" get cluster "$CLUSTER" -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')
check "Cluster Available" "$(is "$avail" True)" "$avail"

kcp=$(k -n "$NS" get kcp -l "cluster.x-k8s.io/cluster-name=$CLUSTER" -o jsonpath='{.items[0].metadata.name}')
read -r rep ready upd ver surge <<<"$(k -n "$NS" get kcp "$kcp" -o jsonpath='{.spec.replicas} {.status.readyReplicas} {.status.upToDateReplicas} {.spec.version} {.spec.rollout.strategy.rollingUpdate.maxSurge}')"
check "KCP $kcp replicas" "$(is "$rep/$ready/$upd" "$CPS/$CPS/$CPS")" "$rep/$ready/$upd"
check "KCP maxSurge is 0" "$(is "$surge" 0)" "maxSurge=$surge (a surge creates a replacement Machine)"
FROM=$ver
python3 - "$FROM" "$TARGET" <<'PY' && tr_ok=ok || tr_ok=no
import re, sys
f, t = (tuple(map(int, re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", v).groups())) for v in sys.argv[1:3])
sys.exit(0 if f[0] == t[0] and t > f and t[1] <= f[1] + 1 else 1)
PY
check "supported transition" "$tr_ok" "$FROM -> $TARGET"

md=$(k -n "$NS" get md -l "cluster.x-k8s.io/cluster-name=$CLUSTER" -o jsonpath='{.items[0].metadata.name}')
read -r mrep mready mupd mver msurge <<<"$(k -n "$NS" get md "$md" -o jsonpath='{.spec.replicas} {.status.readyReplicas} {.status.upToDateReplicas} {.spec.template.spec.version} {.spec.rollout.strategy.rollingUpdate.maxSurge}')"
check "MD $md replicas" "$(is "$mrep/$mready/$mupd" "$WORKERS/$WORKERS/$WORKERS")" "$mrep/$mready/$mupd"
check "MD maxSurge is 0" "$(is "$msurge" 0)" "maxSurge=$msurge"
check "MD version matches KCP" "$(is "$mver" "$FROM")" "$mver"

machines=$(k -n "$NS" get machines -l "cluster.x-k8s.io/cluster-name=$CLUSTER" -o jsonpath='{range .items[*]}{.metadata.name}={.status.nodeInfo.kubeletVersion} {end}')
n=$(wc -w <<<"$machines" | tr -d ' ')
check "Machines" "$(is "$n" $((CPS + WORKERS)))" "$machines"
bad=$(tr ' ' '\n' <<<"$machines" | grep -v "=$FROM\$" | grep -c . || true)
check "Machine kubelets at $FROM" "$(is "$bad" 0)" "$bad differ"

hosts=$(k -n "$NS" get byohosts -o jsonpath='{range .items[*]}{.metadata.name}:{.spec.byoMachineRef.name}:{.status.inPlaceUpgrade.phase}:{.status.agentInfo.version} {end}')
attached=$(tr ' ' '\n' <<<"$hosts" | awk -F: 'NF>1 && $2!=""' | grep -c . || true)
running=$(tr ' ' '\n' <<<"$hosts" | awk -F: '$3=="Running"' | grep -c . || true)
check "ByoHosts attached" "$(is "$attached" $((CPS + WORKERS)))" "$hosts"
check "no running in-place upgrade" "$(is "$running" 0)" "$running running"
crd=$(k get crd byohosts.infrastructure.cluster.x-k8s.io -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.inPlaceUpgrade.type}')
check "ByoHost CRD has inPlaceUpgrade" "$(is "$crd" object)" "${crd:-absent} (controller/agent with in-place support)"

for d in capi-system/capi-controller-manager capi-kubeadm-control-plane-system/capi-kubeadm-control-plane-controller-manager; do
	args=$(k -n "${d%/*}" get deploy "${d#*/}" -o jsonpath='{.spec.template.spec.containers[0].args}')
	gate=$(grep -o 'InPlaceUpdates=true' <<<"$args" || true)
	check "${d#*/} InPlaceUpdates" "$(is "$gate" InPlaceUpdates=true)" "${gate:-off}"
done
sdk=$(k -n capi-system get deploy capi-controller-manager -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -o 'RuntimeSDK=true' || true)
check "RuntimeSDK" "$(is "$sdk" RuntimeSDK=true)" "${sdk:-off}"
disc=$(k get extensionconfig okx-inplace-extension -o jsonpath='{.status.conditions[?(@.type=="Discovered")].status}' 2>/dev/null || true)
check "ExtensionConfig Discovered" "$(is "$disc" True)" "${disc:-absent}"
hooks=$(k get validatingwebhookconfiguration okx-no-replacement-guard -o jsonpath='{range .webhooks[*]}{.failurePolicy}{" "}{end}' 2>/dev/null || true)
nfail=$(wc -w <<<"$hooks" | tr -d ' ')
check "guard webhooks (6, Fail)" "$(is "$nfail/$(tr ' ' '\n' <<<"$hooks" | grep -c '^Fail$' || true)" 6/6)" "$hooks"
label=$(k get ns "$NS" -o jsonpath='{.metadata.labels.safety\.openkubes\.org/in-place-only}')
check "namespace protected" "$(is "$label" true)" "${label:-unlabelled}"

nodes=$(w get nodes -o jsonpath='{range .items[*]}{.metadata.name}={.status.conditions[?(@.type=="Ready")].status}/{.status.nodeInfo.kubeletVersion} {end}')
nready=$(tr ' ' '\n' <<<"$nodes" | grep -c "=True/$FROM\$" || true)
check "Nodes Ready at $FROM" "$(is "$nready" $((CPS + WORKERS)))" "$nodes"
cp1=$(w get nodes -l node-role.kubernetes.io/control-plane -o jsonpath='{.items[0].metadata.name}')
health=$(etcdctl_cp "$cp1" endpoint health --cluster 2>&1 | grep -c 'is healthy' || true)
check "etcd members healthy" "$(is "$health" "$CPS")" "$health/$CPS"
read -r cdes crdy cimg <<<"$(w -n kube-system get ds cilium -o jsonpath='{.status.desiredNumberScheduled} {.status.numberReady} {.spec.template.spec.containers[0].image}')"
check "Cilium ready" "$(is "$crdy" "$cdes")" "$crdy/$cdes"
oimg=$(w -n kube-system get deploy cilium-operator -o jsonpath='{.spec.template.spec.containers[0].image}')
# A pre-release passes only when pinned in cilium-compat.json and approved for this run (OKX_CILIUM_PRERELEASE).
cdetail=$(python3 -I "$HERE/cilium_compat.py" "$HERE/cilium-compat.json" "$cimg" "$oimg" "$TARGET" "${OKX_CILIUM_PRERELEASE:-}") && cc=ok || cc=no
check "Cilium supports ${TARGET%.*}" "$cc" "$cdetail"
if [ -n "${GW_SSH:-}" ]; then
	up=$($GW_SSH "journalctl -u haproxy -o cat --no-pager | grep -E 'is (UP|DOWN)' | tail -1" || true)
	active=$(grep -o '[0-9] active' <<<"$up" | cut -d' ' -f1)
	check "HAProxy backends" "$(is "${active:-}" "$CPS")" "${up:-no events}"
else
	echo "SKIP  HAProxy backends                   GW_SSH not set"
fi
if [ "$fails" -gt 0 ]; then
	echo "BLOCKED: $fails checks failed"
	exit 2
fi
echo "READY"
