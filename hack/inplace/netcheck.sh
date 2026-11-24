#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# Connectivity and policy checks of the okx-netcheck workload (netcheck/workload.yaml, netcheck/policy.yaml), a health
# check between the hosts of an in-place upgrade. It only reads and runs `kubectl exec` in the okx-netcheck clients;
# applying and removing the workload and the policies are separate, approved steps.
# Usage: netcheck.sh open|policy|probe SECONDS
#   open    no policy applied: every client reaches every echo pod (all node pairs), the Service, DNS
#   policy  policy.yaml applied: allowed clients reach echo, denied clients do not, DNS for all
#   probe   one connection per second from an allowed client to the Service; reports failures
# Exit 0 PASS, 1 FAIL.
. "$(dirname "$0")/lib.sh"
mode=${1:?usage: netcheck.sh open|policy|probe SECONDS}
NETNS=okx-netcheck
SVC=echo.$NETNS.svc.cluster.local:8080
n() { w -n "$NETNS" "$@"; }
fails=0 total=0
res() { # res EXPECT(ok|no) GOT(ok|no) TEXT
	total=$((total + 1))
	if [ "$1" = "$2" ]; then echo "PASS  $3"; else echo "FAIL  $3 (expected $1, got $2)"; fails=$((fails + 1)); fi
}
# stdin is /dev/null: kubectl exec would otherwise consume the input of the surrounding read loop
conn() { n exec "$1" -- /agnhost connect --timeout=3s "$2" </dev/null >/dev/null 2>&1 && echo ok || echo no; }
# dns POD: the API Service name resolves. agnhost connect reports "DNS: ..." for a resolution
# error and TIMEOUT/REFUSED when the name resolved but the connection was blocked (denied clients).
dns() {
	local out
	if out=$(n exec "$1" -- /agnhost connect --timeout=3s kubernetes.default.svc.cluster.local:443 </dev/null 2>&1); then echo ok
	elif grep -qE '^(TIMEOUT|REFUSED)' <<<"$out"; then echo ok
	else echo no; fi
}

echo "== $(date -u +%FT%TZ) netcheck $mode"
n get pods -o wide --no-headers | awk '{print "  " $1, $3, $6, $7}'
echos=()
while read -r line; do echos+=("$line"); done < <(n get pods -l app=echo -o jsonpath='{range .items[*]}{.status.podIP} {.spec.nodeName}{"\n"}{end}')
nodes=$(w get nodes --no-headers | wc -l | tr -d ' ')
[ "${#echos[@]}" = "$nodes" ] || { echo "FAIL  ${#echos[@]} echo pods for $nodes nodes"; exit 1; }

case $mode in
open|policy)
	for role in allowed denied; do
		want=ok
		[ "$mode" = policy ] && [ "$role" = denied ] && want=no
		while read -r pod node; do
			for e in "${echos[@]}"; do
				ip=${e% *}
				res "$want" "$(conn "$pod" "$ip:8080")" "$role $node -> echo ${e#* } ($ip)"
			done
			res "$want" "$(conn "$pod" "$SVC")" "$role $node -> Service"
			# DNS stays allowed for every client (CNP egress rule to kube-dns)
			res ok "$(dns "$pod")" "$role $node -> DNS"
		done < <(n get pods -l "app=client,role=$role" -o jsonpath='{range .items[*]}{.metadata.name} {.spec.nodeName}{"\n"}{end}')
	done
	;;
probe)
	secs=${2:?probe SECONDS}
	pod=$(n get pods -l app=client,role=allowed -o jsonpath='{.items[0].metadata.name}')
	for _ in $(seq "$secs"); do
		r=$(conn "$pod" "$SVC")
		total=$((total + 1))
		[ "$r" = ok ] || { fails=$((fails + 1)); echo "$(date -u +%T) probe failed"; }
		sleep 1
	done
	echo "probe: $fails of $total failed"
	;;
*) echo "unknown mode $mode" >&2; exit 2 ;;
esac
echo "RESULT: $((total - fails))/$total passed"
[ "$fails" = 0 ]
