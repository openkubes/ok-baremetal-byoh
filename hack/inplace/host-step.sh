#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# One host at a time through an in-place upgrade, in an approved order.
#   host-step.sh members                  read-only: the etcd member IDs, for ETCD_MEMBERS
#   host-step.sh next TARGET              read-only: which host Cluster API updates now, and whether it is the next
#                                         host of SEQUENCE waiting for its approval (exit 0 = APPROVE, 2 hold, 3 STOP)
#   host-step.sh verify TARGET HOST       read-only: HOST is done (agent Succeeded, Node Ready at TARGET, Machine no
#                                         longer updating; control plane: static pods at TARGET) and the cluster is healthy
#   host-step.sh approve TARGET HOST      (mutation, typed confirmation) the per-host hold point: only when `next`
#                                         says APPROVE for HOST and the cluster is healthy, sets the approval
#                                         annotation on that one ByoHost, bound to its resourceVersion
# Required besides lib.sh: SEQUENCE (comma-separated ByoHost names in the order Cluster API offers them),
# UIDS_BASELINE (uids.py snapshot taken before the version patch), ETCD_MEMBERS (comma-separated member IDs from
# `members` before the first host), GW_SSH (pinned ssh to the gateway, for the HAProxy check).
# Health gate: all Nodes Ready, etcd healthy with exactly ETCD_MEMBERS, Cilium and CoreDNS ready, HAProxy backends all
# active, no Machine, ByoMachine, ByoHost or Node added, removed or replaced since UIDS_BASELINE.
. "$(dirname "$0")/lib.sh"

mode=${1:?usage: host-step.sh members | next TARGET | verify TARGET HOST | approve TARGET HOST}
APPROVAL=inplace.openkubes.org/approved-upgrade

collect() {
	k -n "$NS" get machines -l "cluster.x-k8s.io/cluster-name=$CLUSTER" -o json > "$TMPD/machines.json"
	k -n "$NS" get byomachines -o json > "$TMPD/byomachines.json"
	k -n "$NS" get byohosts -o json > "$TMPD/byohosts.json"
	w get nodes -o json > "$TMPD/nodes.json"
}

cp_nodes() { w get nodes -l node-role.kubernetes.io/control-plane -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'; }

members() { # sorted member IDs, comma-separated
	etcdctl_cp "$(cp_nodes | head -1)" member list -w simple | cut -d, -f1 | tr -d ' ' | sort | paste -sd, -
}

health() { # health TARGET: prints the checks, returns the number of failures
	local fails=0 nodes ready cps healthy ids cdes crdy dns up
	ok() { if [ "$1" = ok ]; then echo "PASS  $2"; else echo "FAIL  $2"; fails=$((fails + 1)); fi; }
	is() { [ "$1" = "$2" ] && echo ok || echo no; }
	nodes=$(jq -r '.items | length' "$TMPD/nodes.json")
	ready=$(jq -r '[.items[] | select(any(.status.conditions[]; .type == "Ready" and .status == "True"))] | length' "$TMPD/nodes.json")
	ok "$(is "$ready" "$nodes")" "Nodes Ready $ready/$nodes"
	cps=$(cp_nodes | grep -c .)
	healthy=$(etcdctl_cp "$(cp_nodes | head -1)" endpoint health --cluster 2>&1 | grep -c 'is healthy' || true)
	ok "$(is "$healthy" "$cps")" "etcd members healthy $healthy/$cps"
	ids=$(members)
	ok "$(is "$ids" "$(tr ',' '\n' <<<"$ETCD_MEMBERS" | sort | paste -sd, -)")" "etcd member IDs unchanged ($ids)"
	read -r cdes crdy <<<"$(w -n kube-system get ds cilium -o jsonpath='{.status.desiredNumberScheduled} {.status.numberReady}')"
	ok "$(is "$crdy" "$cdes")" "Cilium ready $crdy/$cdes"
	dns=$(w -n kube-system get deploy coredns -o jsonpath='{.status.readyReplicas}/{.spec.replicas}')
	ok "$(is "${dns%/*}" "${dns#*/}")" "CoreDNS ready $dns"
	up=$($GW_SSH "journalctl -u haproxy -o cat --no-pager | grep -E 'is (UP|DOWN)' | tail -1" || true)
	ok "$(is "$(grep -o '[0-9] active' <<<"$up" | cut -d' ' -f1)" "$cps")" "HAProxy backends: ${up:-no events}"
	python3 -I "$HERE/uids.py" snapshot "$TMPD/machines.json" "$TMPD/byomachines.json" "$TMPD/byohosts.json" "$TMPD/nodes.json" > "$TMPD/uids-now.json"
	if python3 -I "$HERE/uids.py" compare "$UIDS_BASELINE" "$TMPD/uids-now.json" > "$TMPD/uids-compare.txt"; then
		ok ok "no Machine, ByoMachine, ByoHost or Node added, removed or replaced since $(basename "$UIDS_BASELINE")"
	else
		ok no "identities changed since $(basename "$UIDS_BASELINE"):"
		grep -iE 'fail|differ|missing|added|removed' "$TMPD/uids-compare.txt" | head -10
	fi
	return "$fails"
}

static_pods_at() { # static_pods_at NODE TARGET: control-plane static pods of NODE run TARGET
	local bad
	bad=$(w -n kube-system get pods -o json | jq -r --arg n "$1" --arg t "$2" \
		'.items[] | select(.spec.nodeName == $n and (.metadata.name | test("^kube-(apiserver|controller-manager|scheduler)-")))
		 | select(.spec.containers[0].image | endswith(":" + $t) | not) | .metadata.name')
	[ -z "$bad" ] || { echo "FAIL  static pods of $1 not at $2: $bad"; return 1; }
	echo "PASS  static pods of $1 at $2"
}

case $mode in
members)
	members
	;;
next | verify | approve)
	TARGET=${2:?TARGET}
	: "${SEQUENCE:?set SEQUENCE}" "${UIDS_BASELINE:?set UIDS_BASELINE}" "${ETCD_MEMBERS:?set ETCD_MEMBERS}" "${GW_SSH:?set GW_SSH}"
	[ -s "$UIDS_BASELINE" ] || { echo "UIDS_BASELINE $UIDS_BASELINE is missing or empty" >&2; exit 1; }
	command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
	collect
	echo "== $(ts) $mode $NS/$CLUSTER -> $TARGET (sequence $SEQUENCE)"
	if [ "$mode" = next ]; then
		python3 -I "$HERE/host_step.py" next "$TMPD/machines.json" "$TMPD/byomachines.json" "$TMPD/byohosts.json" "$TMPD/nodes.json" "$TARGET" "$SEQUENCE"
		exit $?
	fi
	HOST=${3:?HOST}
	if [ "$mode" = verify ]; then
		rc=0
		python3 -I "$HERE/host_step.py" verify "$TMPD/machines.json" "$TMPD/byomachines.json" "$TMPD/byohosts.json" "$TMPD/nodes.json" "$TARGET" "$HOST" || rc=$?
		if jq -e --arg h "$HOST" '.items[] | select(.metadata.name == $h) | .metadata.labels | has("node-role.kubernetes.io/control-plane")' "$TMPD/nodes.json" >/dev/null; then
			static_pods_at "$HOST" "$TARGET" || rc=1
		fi
		health "$TARGET" || rc=1
		[ "$rc" = 0 ] && echo "VERIFY PASS: $HOST" || echo "VERIFY FAIL: $HOST (do not approve the next host)"
		exit "$rc"
	fi
	# approve
	decision=$(python3 -I "$HERE/host_step.py" next "$TMPD/machines.json" "$TMPD/byomachines.json" "$TMPD/byohosts.json" "$TMPD/nodes.json" "$TARGET" "$SEQUENCE") || {
		echo "$decision"
		echo "refused: the gate does not say APPROVE" >&2
		exit 1
	}
	echo "$decision"
	grep -q "^APPROVE: $HOST " <<<"$decision" || { echo "refused: the host waiting for its approval is not $HOST" >&2; exit 1; }
	health "$TARGET" || { echo "refused: the cluster is not healthy" >&2; exit 1; }
	rv=$(k -n "$NS" get byohost "$HOST" -o jsonpath='{.metadata.resourceVersion}')
	printf 'Approve the in-place upgrade of ByoHost %s to %s (it is drained, then upgraded by its agent).\nType "APPROVE %s %s": ' "$HOST" "$TARGET" "$HOST" "$TARGET" > /dev/tty
	read -r answer < /dev/tty
	[ "$answer" = "APPROVE $HOST $TARGET" ] || { echo "not confirmed: nothing changed" >&2; exit 1; }
	k -n "$NS" annotate byohost "$HOST" "$APPROVAL=$TARGET" --resource-version="$rv"
	echo "$(ts) approved $HOST -> $TARGET (resourceVersion $rv); monitor with progress.sh / monitor.sh, then: host-step.sh verify $TARGET $HOST"
	;;
*)
	echo "usage: host-step.sh members | next TARGET | verify TARGET HOST | approve TARGET HOST" >&2
	exit 2
	;;
esac
