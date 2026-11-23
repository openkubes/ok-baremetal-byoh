#!/usr/bin/env bash
# Shared helpers for the in-place spike. Strictly isolated: never touches the operator's
# global kubeconfig or any cluster other than this spike's own. Source this from each script.
set -euo pipefail

SPIKE_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_DIR="$SPIKE_HOME/.state"
mkdir -p "$STATE_DIR/kube"

# ---- Identity of everything this spike owns (nothing else may be touched) ----
MGMT_CLUSTER="ok-byoh-inplace-spike"          # dedicated kind cluster (disposable)
CAPI_VERSION="${CAPI_VERSION:-v1.14.4}"
EXT_NS="test-extension-system"
EXT_IMAGE="${EXT_IMAGE:-gcr.io/k8s-staging-cluster-api/test-extension:${CAPI_VERSION}}" # or locally built tag
CAPI_SRC="${CAPI_SRC:-}"                       # path to a cluster-api checkout @ CAPI_VERSION (for image build)
# Vendored, pre-rendered upstream test-extension manifest (no CAPI checkout needed).
# Path + expected SHA are keyed by CAPI_VERSION so a manifest only pairs with its own version.
EXT_MANIFEST="${EXT_MANIFEST:-$SPIKE_HOME/vendor/test-extension-${CAPI_VERSION}.yaml}"
case "$CAPI_VERSION" in
  v1.14.4) _EXT_MANIFEST_SHA256_DEFAULT="f5eae26658b9f62edc09e1ea726210d73fc34aa9dc9004f7561d89c3c37484ff" ;;
  *)       _EXT_MANIFEST_SHA256_DEFAULT="" ;;
esac
EXT_MANIFEST_SHA256="${EXT_MANIFEST_SHA256:-$_EXT_MANIFEST_SHA256_DEFAULT}"  # committed default; missing/wrong => fail-closed
BASE_VERSION="${BASE_VERSION:-v1.33.0}"
TARGET_VERSION="${TARGET_VERSION:-v1.34.0}"
# Protected names this spike must NEVER create, use, or delete:
PROTECTED_CLUSTERS=("ok-byoh-make-test" "ok-byoh-dev" "ok-byoh-scw1" "ok-capd-dev")

# ---- Isolated kubeconfig: everything uses THIS file; ~/.kube/config is never read or written ----
export KUBECONFIG="$STATE_DIR/kube/mgmt.kubeconfig"
MGMT_CTX="kind-$MGMT_CLUSTER"

die(){ echo "STOP: $*" >&2; exit 1; }
log(){ echo "[$(date -u +%H:%M:%S)] $*" >&2; }
need(){ command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }

guard_protected() {
  for p in "${PROTECTED_CLUSTERS[@]}"; do
    [ "$MGMT_CLUSTER" = "$p" ] && die "refusing: spike cluster name collides with protected cluster $p"
  done
  # Never operate against a context that isn't our isolated kind cluster.
  local ctx; ctx="$(kubectl config current-context 2>/dev/null || true)"
  if [ -n "$ctx" ] && [ "$ctx" != "$MGMT_CTX" ]; then
    die "isolated KUBECONFIG points at unexpected context '$ctx' (want $MGMT_CTX); aborting to protect other clusters"
  fi
}

# kubectl/clusterctl wrappers that ALWAYS use the isolated kubeconfig and never mutate global state.
k(){ kubectl --kubeconfig "$KUBECONFIG" "$@"; }
cctl(){ KUBECONFIG="$KUBECONFIG" clusterctl "$@"; }

require_bootstrapped() {
  k cluster-info >/dev/null 2>&1 || die "mgmt cluster not reachable via isolated kubeconfig; run ./bootstrap.sh"
  guard_protected
}

# Name of the test-extension deployment (rollout status needs a name, not a selector).
ext_deploy(){
  local name="test-extension-controller-manager"
  k -n "$EXT_NS" get deployment "$name" -o name 2>/dev/null
}

# Verify an ExtensionConfig has been discovered and is ready (fail closed).
assert_extension_ready() {
  local name="${1:-test-extension}" dep
  local disc
  disc="$(k get extensionconfig "$name" -o jsonpath='{.status.conditions[?(@.type=="Discovered")].status}' 2>/dev/null || true)"
  [ "$disc" = "True" ] || die "ExtensionConfig/$name not Discovered (status='$disc'); extension not ready"
  dep="$(ext_deploy)"; [ -n "$dep" ] || die "test-extension deployment not found in $EXT_NS"
  k -n "$EXT_NS" rollout status "$dep" --timeout=10s >/dev/null 2>&1 || die "test-extension deployment not ready"
}

# ---- Ownership + run provenance ------------------------------------------------------------
OWNER_KEY="ok-byoh-spike/run-id"          # label stamped on every object this run creates
OWNER_CM="ok-byoh-spike-owner"            # ConfigMap in kube-system marking a spike-owned kind cluster
RUN_FILE="$STATE_DIR/run.json"

load_run() { # populate RUN_ID / MGMT_UID / etc. from run.json (fail closed)
  [ -s "$RUN_FILE" ] || die "no $RUN_FILE; run ./bootstrap.sh first"
  RUN_ID="$(jq -r '.run_id' "$RUN_FILE")"; MGMT_UID="$(jq -r '.mgmt_uid' "$RUN_FILE")"
  [ -n "$RUN_ID" ] && [ "$RUN_ID" != null ] || die "run.json missing run_id"
}

# Prove a Cluster object belongs to THIS run before any destructive action. Fail closed.
assert_owned_cluster() { # $1 name $2 ns
  local name="$1" ns="$2" owner
  owner="$(k get cluster "$name" -n "$ns" -o jsonpath="{.metadata.labels.$(echo "$OWNER_KEY" | sed 's/[.\/]/\\&/g')}" 2>/dev/null || true)"
  [ -n "$owner" ] || die "refusing to touch Cluster $ns/$name: no ownership label (not created by this spike)"
  [ "$owner" = "$RUN_ID" ] || die "refusing to touch Cluster $ns/$name: owned by run '$owner', not current run '$RUN_ID'"
}

# Portable read of the apiserver audit log (works on Linux and Docker Desktop).
MGMT_NODE="${MGMT_CLUSTER}-control-plane"
collect_audit() { # $1 outfile ; reads the current log AND any rotated backups so rotation cannot
                  # drop a DELETE record. Failure => empty file => classifier INCOMPLETE (fail closed).
  docker exec "$MGMT_NODE" sh -c 'cat /var/log/ok-audit/audit.log* 2>/dev/null' > "$1" 2>/dev/null || : > "$1"
}

# Continuous control-plane-Machine observer. CORROBORATION only (the audit log is authoritative).
# A failed query is written as an error sample (never '[]'), so an API blip classifies INCOMPLETE.
#
# Lifecycle (P0-1): the observer is started DIRECTLY as a child of the caller's shell and its PID
# is returned via the global OBSERVER_PID — NOT through command substitution (which would hang,
# because the backgrounded child would keep the $() pipe open). The child's stdout/stderr are
# detached to /dev/null; it writes only to $out. Because it is a direct child, `wait` is reliable.
OBSERVER_PID=""
start_cp_observer() { # $1 ns $2 workload $3 outfile ; sets global OBSERVER_PID
  local ns="$1" wl="$2" out="$3"; : > "$out"
  { while :; do
      ts="$(date +%s)"
      if raw="$(k get machines -n "$ns" -l "cluster.x-k8s.io/control-plane,cluster.x-k8s.io/cluster-name=$wl" -o json 2>/dev/null)"; then
        if cp="$(printf '%s' "$raw" | jq -c '[.items[]|{name:.metadata.name,uid:.metadata.uid,deletionTimestamp:.metadata.deletionTimestamp}]' 2>/dev/null)"; then
          echo "{\"t\":$ts,\"cp\":$cp}" >> "$out"
        else
          echo "{\"t\":$ts,\"error\":\"jq-parse\"}" >> "$out"
        fi
      else
        echo "{\"t\":$ts,\"error\":\"kubectl-failed\"}" >> "$out"
      fi
      sleep 3
    done; } >/dev/null 2>&1 &
  OBSERVER_PID=$!
}
stop_cp_observer() { # $1 pid $2 outfile ; sentinel ONLY if the observer was still alive (else fail closed)
  local pid="$1" out="$2"
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    echo "{\"t\":$(date +%s),\"status\":\"complete\"}" >> "$out"
  else
    echo "{\"t\":$(date +%s),\"error\":\"observer-died\"}" >> "$out"   # no sentinel -> INCOMPLETE
  fi
}

snapshot() { # $1 = label ; captures CAPI objects + workload Node versions + recent events
  local label="${1:?label}" ns="${2:-default}"
  k get machine,dockermachine,kubeadmcontrolplane,machinedeployment,machineset \
    -n "$ns" -o json > "$STATE_DIR/snap-$label.json"
  k get events -n "$ns" --sort-by=.lastTimestamp -o json > "$STATE_DIR/events-$label.json" 2>/dev/null || true
  # Workload Node versions (capability-boundary evidence) via an isolated workload kubeconfig.
  local wkc="$STATE_DIR/kube/workload-$label.kubeconfig"
  if cctl get kubeconfig "${WORKLOAD:-}" -n "$ns" > "$wkc" 2>/dev/null; then
    kubectl --kubeconfig "$wkc" get nodes -o json > "$STATE_DIR/nodes-$label.json" 2>/dev/null || true
  fi
  log "snapshot '$label' written"
}
