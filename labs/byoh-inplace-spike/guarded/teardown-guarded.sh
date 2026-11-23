#!/usr/bin/env bash
# Ownership-verified teardown. Deletes ONLY the guarded kind cluster this run created
# (proven via the spike ownership ConfigMap == our RUN_ID). Never deletes anything else.
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib-guarded.sh"
if ! kind get clusters 2>/dev/null | grep -qx "$MGMT_CLUSTER"; then echo "no cluster '$MGMT_CLUSTER'"; exit 0; fi
kind export kubeconfig --name "$MGMT_CLUSTER" --kubeconfig "$KUBECONFIG" 2>/dev/null || true
guard_protected
owner="$(k get cm "$OWNER_CM" -n kube-system -o jsonpath='{.data.run-id}' 2>/dev/null || true)"
[ -n "$owner" ] || die "cluster '$MGMT_CLUSTER' has no spike ownership marker; refusing to delete"
if [ -s "$RUN_FILE" ]; then
  want="$(jq -r .run_id "$RUN_FILE")"
  [ "$owner" = "$want" ] || die "ownership mismatch (cluster owned by '$owner', run.json '$want'); refusing"
fi
log "deleting spike-owned cluster $MGMT_CLUSTER (owner=$owner)"
kind delete cluster --name "$MGMT_CLUSTER"
rm -rf "/tmp/${MGMT_CLUSTER}" 2>/dev/null || true
echo "done. state kept in $STATE_DIR (delete manually if desired)."
