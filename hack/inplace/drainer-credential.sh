#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# The least-privilege drainer identity of the in-place extension
# (config/inplace/workload/drainer.yaml) and its management Secret.
#   drainer-credential.sh plan              read-only: what apply does, current state
#   drainer-credential.sh apply [DURATION]  (mutation, separate approval) apply the workload
#       manifest, request a token (default 48h) and write the Secret <cluster>-inplace-drainer
#       (key value: kubeconfig with the API endpoint and CA of the cluster, never the admin key)
#   drainer-credential.sh verify            read-only: the rights of the token (can-i matrix)
#   drainer-credential.sh revoke            (mutation) delete the Secret and the ServiceAccount;
#       the token stops working with the ServiceAccount.
. "$(dirname "$0")/lib.sh"
SA=okx-inplace-drainer
SECRET="$CLUSTER-inplace-drainer"
MANIFEST="$HERE/../../config/inplace/workload/drainer.yaml"
mode=${1:?usage: drainer-credential.sh plan|apply [DURATION]|verify|revoke}

drainer_kubeconfig() { # writes the kubeconfig of the token in $1 to stdout
	local server ca
	server=$(kubectl --kubeconfig "$TMPD/workload.kubeconfig" config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')
	ca=$(kubectl --kubeconfig "$TMPD/workload.kubeconfig" config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
	cat <<KC
apiVersion: v1
kind: Config
clusters: [{name: $CLUSTER, cluster: {server: "$server", certificate-authority-data: "$ca"}}]
users: [{name: $SA, user: {token: "$1"}}]
contexts: [{name: $SA@$CLUSTER, context: {cluster: $CLUSTER, user: $SA}}]
current-context: $SA@$CLUSTER
KC
}

case $mode in
plan)
	w get --raw /version >/dev/null
	echo "workload: apply $MANIFEST (ServiceAccount kube-system/$SA, ClusterRole nodes get/patch, pods list, pods/eviction create, cordon-only admission policy)"
	echo "workload ServiceAccount: $(w -n kube-system get sa "$SA" -o name 2>/dev/null || echo absent)"
	echo "management Secret $NS/$SECRET: $(k -n "$NS" get secret "$SECRET" -o name 2>/dev/null || echo absent)"
	;;
apply)
	duration=${2:-48h}
	w apply --server-side -f "$MANIFEST"
	token=$(w -n kube-system create token "$SA" --duration="$duration")
	drainer_kubeconfig "$token" > "$TMPD/drainer.kubeconfig"
	k -n "$NS" create secret generic "$SECRET" --from-file=value="$TMPD/drainer.kubeconfig" --dry-run=client -o yaml |
		k label --local -f - "cluster.x-k8s.io/cluster-name=$CLUSTER" -o yaml | k apply --server-side -f -
	echo "Secret $NS/$SECRET written (token for $duration)"
	;;
verify)
	k -n "$NS" get secret "$SECRET" -o jsonpath='{.data.value}' | base64 -d > "$TMPD/drainer.kubeconfig"
	chmod 600 "$TMPD/drainer.kubeconfig"
	d() { kubectl --kubeconfig "$TMPD/drainer.kubeconfig" --request-timeout=20s "$@"; }
	fails=0
	for c in "yes get nodes" "yes patch nodes" "yes list pods -A" "yes create pods/eviction -A" \
		"no get secrets -A" "no delete pods -A" "no delete nodes" "no create pods -A" "no update nodes/status" "no '*' '*'"; do
		want=${c%% *}
		got=$(eval d auth can-i "${c#* }" 2>/dev/null || true)
		if [ "$got" = "$want" ]; then echo "PASS can-i ${c#* } = $got"; else echo "FAIL can-i ${c#* } = $got (want $want)"; fails=$((fails + 1)); fi
	done
	[ "$fails" -eq 0 ] && echo "VERIFY PASS" || { echo "VERIFY FAIL"; exit 1; }
	;;
revoke)
	k -n "$NS" delete secret "$SECRET" --ignore-not-found
	w -n kube-system delete sa "$SA" --ignore-not-found
	;;
*)
	echo "unknown mode $mode" >&2
	exit 1
	;;
esac
