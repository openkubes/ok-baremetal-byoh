#!/usr/bin/env bash
# OpenKubes BYOH E2E evidence capture.
# Read-only Kubernetes operations. Never collect Secrets or kubeconfigs.
set -euo pipefail
umask 077

MGMT_KUBECONFIG="${MGMT_KUBECONFIG:-$HOME/.config/ok/byoh-make-test/kubeconfig}"
WORKLOAD_KUBECONFIG="${WORKLOAD_KUBECONFIG:-$HOME/.config/ok/byoh-scw1/kubeconfig-tunnel}"
CLUSTER="${CLUSTER:-ok-byoh-scw1}"
NAMESPACE="${NAMESPACE:-default}"
HOST_CP="${HOST_CP:-ok-bm-lab-01}"
HOST_WORKER="${HOST_WORKER:-ok-bm-lab-02}"

OUT_BASE="${1:-/tmp/ok-byoh-scaleway-e2e-evidence}"

# Create a unique evidence directory without overwriting prior runs.
mkdir -p "$OUT_BASE"
chmod 700 "$OUT_BASE"
OUT=$(mktemp -d "$OUT_BASE/run-XXXXXXXX")

printf 'Capture started; not yet complete.\n' > "$OUT/INCOMPLETE"

finish() {
  rc=$?
  if [ "$rc" -eq 0 ]; then
    printf 'Capture completed successfully.\n' > "$OUT/COMPLETE"
    rm -f "$OUT/INCOMPLETE"
  else
    printf 'ERROR: incomplete evidence in %s (exit %s)\n'       "$OUT" "$rc" >&2
  fi
}

trap finish EXIT

say() { printf '%s\n' "$*"; }
fail() { say "ERROR: $*" >&2; exit 1; }

command -v kubectl >/dev/null 2>&1 ||
  fail 'kubectl missing'

[ -s "$MGMT_KUBECONFIG" ] ||
  fail 'management kubeconfig missing'

[ -s "$WORKLOAD_KUBECONFIG" ] ||
  fail 'workload kubeconfig missing'

mgmt() {
  kubectl --kubeconfig "$MGMT_KUBECONFIG" \
    -n "$NAMESPACE" "$@"
}

workload() {
  kubectl --kubeconfig "$WORKLOAD_KUBECONFIG" "$@"
}

# Only explicitly selected non-secret resource summaries.
mgmt get cluster "$CLUSTER" -o wide \
  > "$OUT/cluster.txt"

mgmt get machinedeployment "${CLUSTER}-md-0" -o wide \
  > "$OUT/machinedeployment.txt"

mgmt get machines \
  -l "cluster.x-k8s.io/cluster-name=$CLUSTER" \
  -o wide > "$OUT/machines.txt"

mgmt get byohost "$HOST_CP" "$HOST_WORKER" \
  -o custom-columns='NAME:.metadata.name,MACHINE:.spec.byoMachineRef.name,AGENT:.status.agentInfo.version' \
  > "$OUT/hosts.txt"

workload get nodes -o wide \
  > "$OUT/nodes.txt"

workload -n kube-system get daemonset cilium -o wide \
  > "$OUT/cilium.txt"

{
  printf 'cluster=%s\n' "$CLUSTER"
  printf 'namespace=%s\n' "$NAMESPACE"
  printf 'collected_at_utc=%s\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "$OUT/context.txt"

say "PASS: evidence captured in $OUT"
say "Review hostnames, addresses and metadata before sharing."
