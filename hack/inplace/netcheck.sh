#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# Connectivity and policy checks of the okx-netcheck workload (netcheck/workload.yaml, netcheck/policy.yaml), a health
# check between the hosts of an in-place upgrade. It only reads and runs `kubectl exec` in the okx-netcheck clients;
# applying and removing the workload and the policies are separate, approved steps.
# Usage: netcheck.sh open|policy|smoke NODE|probe SECONDS
#   open    no policy applied: every client reaches every echo pod (all node pairs), the Service, DNS
#   policy  policy.yaml applied: allowed clients reach echo, denied clients do not, DNS for all
#   smoke  one upgraded node and a distinct peer under the existing policy
#   probe   one connection per second from an allowed client to the Service; reports failures
# Exit 0 PASS, 1 FAIL. Matrix execution is serial unless OKX_NETCHECK_CONCURRENCY is set.
# OKX_NETCHECK_CONCURRENCY: 1..16
mode=${1:?usage: netcheck.sh open|policy|smoke NODE|probe SECONDS}
case $mode in
open|policy|smoke)
	concurrency=${OKX_NETCHECK_CONCURRENCY:-1}
	[[ "$concurrency" =~ ^([1-9]|1[0-6])$ ]] ||
		{ echo "OKX_NETCHECK_CONCURRENCY must be 1..16" >&2; exit 2; }
	;;
probe) ;;
*) echo "unknown mode $mode" >&2; exit 2 ;;
esac
. "$(dirname "$0")/lib.sh"
NETNS=okx-netcheck
SVC=echo.$NETNS.svc.cluster.local:8080
probe_slot=0
printf 'OKX_CONNECT_OK\n' > "$TMPD/success"
printf 'TIMEOUT\ncommand terminated with exit code 1\n' > "$TMPD/timeout"
printf 'REFUSED\ncommand terminated with exit code 1\n' > "$TMPD/refused"
n() { w -n "$NETNS" "$@"; }
fails=0 total=0
res() { # res EXPECTED_RESULT ACTUAL_RESULT TEXT
	total=$((total + 1))
	if [ "$1" = "$2" ]; then echo "PASS  $3"; else echo "FAIL  $3 (expected $1, got $2)"; fails=$((fails + 1)); fi
}
conn() {
	local output="$TMPD/connect-$probe_slot" rc=0
	# Preserve both streams byte for byte. Each active slot owns its files.
	# /dev/null keeps kubectl exec from consuming the surrounding loop's input.
	# The container shell acknowledges only a successful agnhost execution.
	if ! {
		# shellcheck disable=SC2016 # The remote shell expands its positional argument.
		n exec "$1" -- /bin/sh -c '/agnhost connect --timeout=3s "$1" && printf "%s\n" OKX_CONNECT_OK' sh "$2" </dev/null || rc=$?
	} > "$output.out" 2> "$output.err"; then
		echo error
		return
	fi
	if [ "$rc" = 0 ] && [ ! -s "$output.err" ] && cmp -s "$output.out" "$TMPD/success"; then echo ok
	elif [ "$rc" = 1 ] && [ ! -s "$output.out" ]; then
		# kubectl prints a trailer for the remote exit status.
		if cmp -s "$output.err" "$TMPD/timeout"; then echo timeout
		elif cmp -s "$output.err" "$TMPD/refused"; then echo refused
		else echo error; fi
	else echo error; fi
}
# agnhost resolves the name before dialing. A confirmed timeout or refusal
# also proves DNS worked, even if policy blocks reaching the API Service.
dns() {
	case "$(conn "$1" kubernetes.default.svc.cluster.local:443)" in
		ok|timeout|refused) echo ok ;;
		*) echo error ;;
	esac
}

# Jobs write only their result. The parent reports results in matrix order,
# and joins every outstanding probe before lib.sh's private workspace is removed.
pids=() wants=() descriptions=()
finish_batch() {
	local i got
	for ((i = 0; i < ${#pids[@]}; i++)); do
		if ! wait "${pids[i]}" || ! read -r got < "$TMPD/matrix-$i" || [[ ! "$got" =~ ^(ok|timeout|refused|error)$ ]]; then
			echo "FAIL  ${descriptions[i]} (missing or invalid probe result)" >&2
			exit 1
		fi
		res "${wants[i]}" "$got" "${descriptions[i]}"
	done
	pids=() wants=() descriptions=()
}
matrix_cleanup() {
	local rc=$?
	wait || true
	rm -rf -- "$TMPD"
	return "$rc"
}
check_probe() { # WANT FUNCTION DESCRIPTION [ARGS...]
	local want=$1 check=$2 description=$3 probe_slot=0
	shift 3
	if [ "$concurrency" = 1 ]; then
		res "$want" "$("$check" "$@")" "$description"
		return
	fi
	probe_slot=${#pids[@]}
	"$check" "$@" > "$TMPD/matrix-$probe_slot" &
	pids+=("$!")
	wants+=("$want")
	descriptions+=("$description")
	if [ "${#pids[@]}" = "$concurrency" ]; then finish_batch; fi
}

echo "== $(date -u +%FT%TZ) netcheck $mode"
n get pods -o wide --no-headers | awk '{print "  " $1, $3, $6, $7}'
echos=()
echo_rows=$(n get pods -l app=echo -o jsonpath='{range .items[*]}{.status.podIP} {.spec.nodeName}{"\n"}{end}' | LC_ALL=C sort -k2,2 -k1,1)
while read -r line; do [ -z "$line" ] || echos+=("$line"); done <<< "$echo_rows"
nodes=$(w get nodes --no-headers | wc -l | tr -d ' ')
[ "$nodes" -gt 0 ] && [ "${#echos[@]}" = "$nodes" ] || { echo "FAIL  ${#echos[@]} echo pods for $nodes nodes"; exit 1; }

case $mode in
open|policy|smoke)
	if [ "$concurrency" != 1 ]; then
		trap matrix_cleanup EXIT
		trap 'exit 130' INT
		trap 'exit 143' TERM
	fi
	if [ "$mode" = smoke ]; then
		focus=${2:?upgraded workload Node required}
		peer=""
		seen=0
		for e in "${echos[@]}"; do
			if [ "${e#* }" = "$focus" ]; then seen=$((seen + 1))
			elif [ -z "$peer" ]; then peer=${e#* }; fi
		done
		[ "$seen" = 1 ] && [ -n "$peer" ] ||
			{ echo "FAIL  smoke needs the exact upgraded Node and a distinct peer"; exit 1; }
	fi
	for role in allowed denied; do
		want=ok
		[ "$mode" != open ] && [ "$role" = denied ] && want=timeout
		clients=$(n get pods -l "app=client,role=$role" -o jsonpath='{range .items[*]}{.metadata.name} {.spec.nodeName}{"\n"}{end}' | LC_ALL=C sort -k2,2 -k1,1)
		[ -n "$clients" ] && [ "$(printf '%s\n' "$clients" | wc -l | tr -d ' ')" = "$nodes" ] ||
			{ echo "FAIL  expected one $role client per node"; exit 1; }
		while read -r pod node; do
			if [ "$mode" = smoke ] && [ "$node" != "$focus" ] && [ "$node" != "$peer" ]; then continue; fi
			for e in "${echos[@]}"; do
				ip=${e% *}
				check_probe "$want" conn "$role $node -> echo ${e#* } ($ip)" "$pod" "$ip:8080"
			done
			check_probe "$want" conn "$role $node -> Service" "$pod" "$SVC"
			# DNS stays allowed for every client (CNP egress rule to kube-dns)
			check_probe ok dns "$role $node -> DNS" "$pod"
		done <<< "$clients"
	done
	if [ "$concurrency" != 1 ]; then finish_batch; fi
	if [ "$mode" = smoke ] && [ "$total" != 16 ]; then echo "FAIL  incomplete smoke coverage"; exit 1; fi
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
esac
echo "RESULT: $((total - fails))/$total passed"
[ "$fails" = 0 ]
