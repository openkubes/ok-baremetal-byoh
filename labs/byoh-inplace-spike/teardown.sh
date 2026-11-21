#!/usr/bin/env bash
# Fail-closed, ownership-verified teardown. Deletes nothing it cannot prove it owns.
# Refuses entirely if the management API is unreachable or the ownership marker is absent.
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
CLS="$SPIKE_HOME/classify.sh"
for p in "${PROTECTED_CLUSTERS[@]}"; do [ "$MGMT_CLUSTER" = "$p" ] && die "refusing (protected name)"; done

if ! kind get clusters 2>/dev/null | grep -qx "$MGMT_CLUSTER"; then
  log "kind cluster $MGMT_CLUSTER not present; nothing to delete"; exit 0
fi

kind export kubeconfig --name "$MGMT_CLUSTER" --kubeconfig "$KUBECONFIG" 2>/dev/null || true
guard_protected

# P0-1: ownership + reachability must BOTH hold, or we refuse to delete anything.
api_ok=0; k cluster-info >/dev/null 2>&1 && api_ok=1
owner_present=0; owner_id=""
if [ "$api_ok" = 1 ] && owner_id="$(k get cm "$OWNER_CM" -n kube-system -o jsonpath='{.data.run-id}' 2>/dev/null)" && [ -n "$owner_id" ]; then
  owner_present=1
fi
bash "$CLS" can_delete_cluster "$api_ok" "$owner_present" \
  || die "refusing teardown: management API reachable=$api_ok, spike ownership marker present=$owner_present. Will not delete a cluster whose ownership cannot be proven."

# P1: the cluster inventory must be retrieved SUCCESSFULLY and parse cleanly before ANY delete.
# A failed/empty listing from an API error must never let us fall through to `kind delete`.
inv_json="$(k get clusters -A -o json 2>/dev/null)" || die "cluster inventory retrieval failed; refusing teardown"
echo "$inv_json" | jq -e 'has("items")' >/dev/null 2>&1 || die "cluster inventory not parseable/complete; refusing teardown"

# 1. Delete ONLY workload Clusters whose run-id label matches our ownership marker. Fail closed.
while IFS=$'\t' read -r ns name lbl; do
  [ -n "$name" ] || continue
  if ! bash "$CLS" owns "$lbl" "$owner_id"; then
    echo "WARNING: skipping Cluster $ns/$name — run-id label '$lbl' != owner '$owner_id' (not ours)" >&2
    continue
  fi
  log "deleting owned workload Cluster $ns/$name (CAPD reaps its containers by ownerRef)"
  k delete cluster "$name" -n "$ns" --wait --timeout=300s      # fail closed (no fallback)
  ! k get cluster "$name" -n "$ns" >/dev/null 2>&1 || die "incomplete deletion of $ns/$name; aborting before kind delete"
done < <(echo "$inv_json" | jq -r '.items[] | [.metadata.namespace, .metadata.name, (.metadata.labels["ok-byoh-spike/run-id"] // "")] | @tsv')

# 2. Delete the spike's own kind cluster (ownership already proven above).
kind delete cluster --name "$MGMT_CLUSTER"

# 3. Report (never force-delete) any CAPD containers still labelled for our workloads.
remaining="$(docker ps -a --filter "label=cluster.x-k8s.io/cluster-name" \
  --format '{{.ID}} {{.Names}} {{.Label "cluster.x-k8s.io/cluster-name"}}' 2>/dev/null | awk '$3 ~ /^ipw-/ {print}')"
if [ -n "$remaining" ]; then
  echo "WARNING: CAPD containers still present for spike workloads (NOT auto-removed):" >&2
  echo "$remaining" >&2
  echo "Confirm ownership, then remove manually: docker rm -f <ID>" >&2
else
  log "teardown complete; no spike-owned CAPD containers remain"
fi
