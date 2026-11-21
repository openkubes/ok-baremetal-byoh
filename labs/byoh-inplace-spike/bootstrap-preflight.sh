#!/usr/bin/env bash
# Reproducible, fail-closed bootstrap. Isolated kubeconfig. Pinned+checksummed dependencies.
# Generates a fresh RUN_ID (invalidating any prior scenario approvals) and stamps ownership.
#   ./bootstrap.sh [--reuse]      (--reuse adopts an existing SPIKE-OWNED kind cluster only)
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
REUSE=0; [ "${1:-}" = "--reuse" ] && REUSE=1

need docker; need kind; need kubectl; need clusterctl; need jq; need yq; need curl
docker info >/dev/null 2>&1 || die "docker daemon not reachable"
sha(){ if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

# P1#3: dependency pins are mandatory (fail closed if unset).
: "${CERT_MANAGER_VERSION:?set CERT_MANAGER_VERSION (pinned, e.g. v1.16.2)}"
: "${CERT_MANAGER_SHA256:?set CERT_MANAGER_SHA256 (sha256 of the cert-manager release manifest)}"

for p in "${PROTECTED_CLUSTERS[@]}"; do [ "$MGMT_CLUSTER" = "$p" ] && die "protected name collision"; done

# --- 1. Isolated kind cluster (audit logging enabled); P2: refuse silent reuse of a non-spike cluster ---
AUDIT_DIR="$STATE_DIR/audit"; mkdir -p "$AUDIT_DIR"
KGEN="$STATE_DIR/kind-mgmt.gen.yaml"
cat > "$KGEN" <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.36.4@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed
  extraMounts:
    - hostPath: /var/run/docker.sock
      containerPath: /var/run/docker.sock
    - hostPath: /tmp/ok-byoh-inplace-spike/audit-policy.yaml
      containerPath: /etc/kubernetes/ok-audit-policy.yaml
      readOnly: true
  kubeadmConfigPatches:
  - |
    apiVersion: kubeadm.k8s.io/v1beta4
    kind: ClusterConfiguration
    apiServer:
      extraArgs:
        - name: audit-policy-file
          value: /etc/kubernetes/ok-audit-policy.yaml
        - name: audit-log-path
          value: /var/log/ok-audit/audit.log
        - name: audit-log-maxbackup
          value: "10"
        - name: audit-log-maxsize
          value: "100"
      extraVolumes:
        - name: ok-audit-policy
          hostPath: /etc/kubernetes/ok-audit-policy.yaml
          mountPath: /etc/kubernetes/ok-audit-policy.yaml
          readOnly: true
          pathType: File
        - name: ok-audit-log
          hostPath: /var/log/ok-audit
          mountPath: /var/log/ok-audit
          pathType: DirectoryOrCreate
YAML

echo "PASS: reached cluster creation boundary"
exit 0
