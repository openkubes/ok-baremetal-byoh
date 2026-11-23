#!/usr/bin/env bash
# Guarded CAPD E2E bootstrap. Reuses the vanilla lab's patterns (lib.sh helpers,
# audit kubeadm patch, cert-manager pinning, clusterctl init gates) but on a
# DISTINCT disposable cluster, deploying MY guard image + MY RuntimeSDK extension.
# The guard is NOT activated here — scenario-guarded.sh enables it after the
# workload is provisioned, so it never blocks normal provisioning.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib-guarded.sh"

need docker; need kind; need kubectl; need clusterctl; need jq; need yq; need curl; need python3
docker info >/dev/null 2>&1 || die "docker daemon not reachable"
sha(){ if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
: "${CERT_MANAGER_VERSION:?set CERT_MANAGER_VERSION (pinned, e.g. v1.16.2)}"
: "${CERT_MANAGER_SHA256:?set CERT_MANAGER_SHA256 (sha256 of the cert-manager release manifest)}"

# never collide with a protected/existing cluster
for p in "${PROTECTED_CLUSTERS[@]}"; do [ "$MGMT_CLUSTER" = "$p" ] && die "protected name collision: $p"; done
if kind get clusters 2>/dev/null | grep -qx "$MGMT_CLUSTER"; then
  die "kind cluster '$MGMT_CLUSTER' already exists — refusing to overwrite (teardown-guarded.sh first, or set GUARDED_CLUSTER)"
fi

# --- isolated kind cluster with apiserver audit logging (authoritative DELETE record) ---
# The audit policy is mounted into the kind node via hostPath, so it MUST live under a
# path the colima VM actually mounts. A freshly created colima profile mounts $HOME by
# default but NOT the Mac's /tmp — a /tmp hostPath would bind an empty dir and crash the
# kube-apiserver (--audit-policy-file pointing at a directory). STATE_DIR is under $HOME.
AUDIT_HOST="$STATE_DIR/audit-host"; mkdir -p "$AUDIT_HOST"
cp "$SPIKE_HOME/audit-policy.yaml" "$AUDIT_HOST/audit-policy.yaml"
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
    - hostPath: ${AUDIT_HOST}/audit-policy.yaml
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
# --retain keeps the node on failure so we can capture diagnostics BEFORE deletion.
if ! kind create cluster --name "$MGMT_CLUSTER" --kubeconfig "$KUBECONFIG" --config "$KGEN" --retain; then
  log "kind create FAILED — capturing diagnostics before deleting the retained node"
  DIAG="$STATE_DIR/evidence/bootstrap-fail"; mkdir -p "$DIAG"
  node="${MGMT_CLUSTER}-control-plane"
  docker ps -a --filter "name=$node" > "$DIAG/docker-ps.txt" 2>&1 || true
  # Is the audit policy a FILE (correct) or a DIRECTORY (empty-dir bind = the mount bug)?
  docker exec "$node" ls -la /etc/kubernetes/ok-audit-policy.yaml > "$DIAG/audit-mount.txt" 2>&1 || true
  docker exec "$node" head -5 /etc/kubernetes/ok-audit-policy.yaml >> "$DIAG/audit-mount.txt" 2>&1 || true
  docker exec "$node" cat /etc/kubernetes/manifests/kube-apiserver.yaml > "$DIAG/kube-apiserver.yaml" 2>&1 || true
  docker exec "$node" sh -c 'command -v crictl >/dev/null && crictl ps -a' > "$DIAG/crictl-ps.txt" 2>&1 || true
  docker exec "$node" sh -c 'command -v crictl >/dev/null && for pod in $(crictl ps -aq 2>/dev/null); do echo "== container $pod =="; crictl logs "$pod" 2>&1 | tail -80; done' > "$DIAG/crictl-logs.txt" 2>&1 || true
  docker exec "$node" journalctl -u kubelet --no-pager > "$DIAG/kubelet.log" 2>&1 || true
  kind export logs "$DIAG/kind-export" --name "$MGMT_CLUSTER" >/dev/null 2>&1 || true
  kind delete cluster --name "$MGMT_CLUSTER" >/dev/null 2>&1 || true
  die "kind bootstrap failed — diagnostics in $DIAG (audit-mount.txt: a directory instead of a file confirms the hostPath mount bug; crictl-logs.txt shows the apiserver crash reason)"
fi
guard_protected
k cluster-info >/dev/null 2>&1 || die "mgmt cluster unreachable"

# prove audit active (fail closed)
k create configmap ok-audit-probe -n default --from-literal=x=1 >/dev/null
k delete configmap ok-audit-probe -n default >/dev/null
sleep 3
collect_audit "$STATE_DIR/audit/audit.log"
grep -q 'ok-audit-probe' "$STATE_DIR/audit/audit.log" || die "apiserver audit logging NOT active"
log "audit logging verified active"

# --- cert-manager (pinned + checksummed) ---
cmf="$STATE_DIR/cert-manager-$CERT_MANAGER_VERSION.yaml"
curl -fsSL "https://github.com/cert-manager/cert-manager/releases/download/$CERT_MANAGER_VERSION/cert-manager.yaml" -o "$cmf" \
  || die "download cert-manager failed"
echo "$CERT_MANAGER_SHA256  $cmf" | shasum -a 256 -c - || die "cert-manager checksum mismatch"
k apply -f "$cmf"
k -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s || die "cert-manager not ready"

# --- CAPI init with in-place gates ON ---
export EXP_RUNTIME_SDK=true EXP_IN_PLACE_UPDATES=true
cctl init --core "cluster-api:$CAPI_VERSION" --bootstrap "kubeadm:$CAPI_VERSION" \
          --control-plane "kubeadm:$CAPI_VERSION" --infrastructure "docker:$CAPI_VERSION"
k -n capi-system get deploy capi-controller-manager -o yaml | grep -q "InPlaceUpdates=true" || die "InPlaceUpdates gate missing (core)"
k -n capi-kubeadm-control-plane-system get deploy -o yaml | grep -q "InPlaceUpdates=true" || die "InPlaceUpdates gate missing (KCP)"
k -n capi-system get deploy capi-controller-manager -o yaml | grep -q "RuntimeSDK=true" || die "RuntimeSDK gate missing"

# --- Build + load MY images from the repo (Dockerfiles use golang:1.26 builder) ---
log "building $GUARD_IMAGE and $OKX_EXT_IMAGE from $REPO_ROOT"
( cd "$REPO_ROOT" && docker build -t "$GUARD_IMAGE"   -f cmd/no-replacement-guard/Dockerfile . ) || die "guard image build failed"
( cd "$REPO_ROOT" && docker build -t "$OKX_EXT_IMAGE" -f cmd/inplace-extension/Dockerfile   . ) || die "extension image build failed"
kind load docker-image "$GUARD_IMAGE"   --name "$MGMT_CLUSTER" || die "kind load guard failed"
kind load docker-image "$OKX_EXT_IMAGE" --name "$MGMT_CLUSTER" || die "kind load extension failed"

# --- Deploy MY RuntimeSDK extension + ExtensionConfig; wait discovery (fail closed) ---
EXT_RENDERED="$STATE_DIR/okx-extension.yaml"
OKX_EXT_IMAGE="$OKX_EXT_IMAGE" yq '
  (select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name=="manager") | .image) = strenv(OKX_EXT_IMAGE)
' "$GUARDED_HOME/manifests/extension.yaml" > "$EXT_RENDERED"
k apply -f "$EXT_RENDERED"
k -n "$OKX_EXT_NS" rollout status deploy/inplace-extension --timeout=180s || die "extension deploy not ready"
for i in $(seq 1 30); do
  [ "$(k get extensionconfig okx-inplace-extension -o jsonpath='{.status.conditions[?(@.type=="Discovered")].status}' 2>/dev/null)" = "True" ] && break; sleep 4
done
[ "$(k get extensionconfig okx-inplace-extension -o jsonpath='{.status.conditions[?(@.type=="Discovered")].status}' 2>/dev/null)" = "True" ] \
  || die "okx-inplace-extension ExtensionConfig not Discovered"
log "MY extension discovered"

# --- provenance: fresh RUN_ID + ownership marker ---
RUN_ID="guarded-$(date +%s)-$(head -c6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
MGMT_UID="$(k get ns kube-system -o jsonpath='{.metadata.uid}')"
jq -n --arg r "$RUN_ID" --arg m "$MGMT_UID" --arg c "$CAPI_VERSION" --arg g "$GUARD_IMAGE" --arg e "$OKX_EXT_IMAGE" \
   '{run_id:$r,mgmt_uid:$m,capi_version:$c,guard_image:$g,ext_image:$e}' > "$RUN_FILE"
k create cm "$OWNER_CM" -n kube-system --from-literal=run-id="$RUN_ID" --dry-run=client -o yaml | k apply -f -
log "GUARDED BOOTSTRAP OK  run_id=$RUN_ID  cluster=$MGMT_CLUSTER"
log "Next: ./scenario-guarded.sh (run by ./e2e.sh)"
