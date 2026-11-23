#!/usr/bin/env bash
# Guarded CAPD E2E — identity + helpers. REUSES ../lib.sh (never modifies it) and
# overrides the identity so this runs on a DISTINCT, disposable kind cluster with
# its OWN isolated state + kubeconfig, never touching the vanilla spike or any
# other cluster. Source this from each guarded script.
set -euo pipefail
GUARDED_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SPIKE_HOME="$(cd "$GUARDED_HOME/.." && pwd)"

# Reuse the vanilla lab's helpers (die/log/need/k/cctl/guard_protected/ownership/
# observer/audit/snapshot). We then OVERRIDE identity globals; bash functions read
# these at call time, so the reused functions operate on the guarded cluster.
# shellcheck source=../lib.sh
source "$SPIKE_HOME/lib.sh"

# ---- Distinct guarded identity (unique new name; never the vanilla spike) ----
GUARDED_CLUSTER="${GUARDED_CLUSTER:-ok-byoh-guarded}"
MGMT_CLUSTER="$GUARDED_CLUSTER"
MGMT_CTX="kind-$MGMT_CLUSTER"
MGMT_NODE="${MGMT_CLUSTER}-control-plane"
STATE_DIR="$GUARDED_HOME/.state-guarded"
mkdir -p "$STATE_DIR/kube" "$STATE_DIR/audit" "$STATE_DIR/evidence"
export KUBECONFIG="$STATE_DIR/kube/mgmt.kubeconfig"
RUN_FILE="$STATE_DIR/run.json"
# The vanilla spike cluster and any OpenKubes/dev cluster are protected names we must never touch.
PROTECTED_CLUSTERS+=("ok-byoh-inplace-spike")

# ---- My images (built from this repo; never upstream) ----
GUARD_IMAGE="${GUARD_IMAGE:-okx/no-replacement-guard:e2e}"
OKX_EXT_IMAGE="${OKX_EXT_IMAGE:-okx/inplace-extension:e2e}"
OKX_EXT_NS="inplace-extension-system"
GUARD_NS="no-replacement-guard-system"

# Protected workload namespace + cluster identity bound in the guard registry.
WL_NS="${WL_NS:-default}"
PROTECT_LABEL="safety.openkubes.org/in-place-only"
GRANT_LABEL="safety.openkubes.org/grant"

# Grant actor identities (ServiceAccounts) the guarded-ConfigMap webhook trusts.
APPROVER_SA="system:serviceaccount:${GUARD_NS}:okx-approver"
INITIATOR_SA="system:serviceaccount:${GUARD_NS}:okx-initiator"

REPO_ROOT="$(cd "$SPIKE_HOME/../.." && pwd)"   # -> ok-baremetal-byoh

# ---- Workload API access ----
# The CAPD workload API (LB on the kind docker network) is not routable from the
# macOS host under colima, so workload kubectl runs INSIDE the mgmt kind node, which
# shares that docker network. The kubeconfig is piped in (kind's /tmp is a tmpfs).
wk() { # $1 workload $2 ns ; rest = kubectl args (stdin is forwarded)
  local wl="$1" ns="$2"; shift 2
  cctl get kubeconfig "$wl" -n "$ns" 2>/dev/null \
    | docker exec -i "$MGMT_NODE" sh -c 'mkdir -p /root/okx && cat > /root/okx/wl.kubeconfig' || return 1
  docker exec -i "$MGMT_NODE" kubectl --kubeconfig /root/okx/wl.kubeconfig "$@"
}

# ---- UID capture (Machine / InfraMachine / Node) before+after ----
capture_uids() { # $1 label $2 workload-name $3 ns
  local label="$1" wl="$2" ns="${3:-$WL_NS}"
  local out="$STATE_DIR/evidence/uids-$label.json"   # separate: `local` expands all args before assigning
  local machines infra nodes
  machines="$(k get machines -n "$ns" -l "cluster.x-k8s.io/cluster-name=$wl" -o json 2>/dev/null \
    | jq -c '[.items[]|{name:.metadata.name,uid:.metadata.uid,version:.spec.version,
             kubeletVersion:.status.nodeInfo.kubeletVersion,
             infraKind:.spec.infrastructureRef.kind,infraName:.spec.infrastructureRef.name}]' 2>/dev/null || echo '[]')"
  infra="$(k get dockermachines -n "$ns" -o json 2>/dev/null \
    | jq -c '[.items[]|{name:.metadata.name,uid:.metadata.uid}]' 2>/dev/null || echo '[]')"
  # Fail closed: an unreachable workload API yields nodes=null (never '[]'), so the
  # Node UID comparison cannot pass vacuously.
  nodes="$(wk "$wl" "$ns" get nodes -o json 2>/dev/null \
    | jq -c '[.items[]|{name:.metadata.name,uid:.metadata.uid,kubeletVersion:.status.nodeInfo.kubeletVersion}]' 2>/dev/null)"
  [ -n "$nodes" ] || nodes='null'
  jq -n --arg l "$label" --argjson m "$machines" --argjson i "$infra" --argjson n "$nodes" \
     '{label:$l,machines:$m,infraMachines:$i,nodes:$n}' > "$out"
  log "UID snapshot '$label' -> $out"
}

# Compare two UID snapshots; writes a verdict file. Machine/InfraMachine/Node UIDs
# MUST be identical before and after an in-place accept (no replacement).
assert_uids_preserved() { # $1 before-label $2 after-label $3 verdict-out
  local b="$STATE_DIR/evidence/uids-$1.json" a="$STATE_DIR/evidence/uids-$2.json" out="$3"
  python3 "$GUARDED_HOME/uidcmp.py" "$b" "$a" > "$out" 2>&1
  grep -q '"verdict": "PASS"' "$out"
}
