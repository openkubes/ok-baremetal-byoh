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

if kind get clusters 2>/dev/null | grep -qx "$MGMT_CLUSTER"; then
  kind export kubeconfig --name "$MGMT_CLUSTER" --kubeconfig "$KUBECONFIG"
  guard_protected
  if ! k get cm "$OWNER_CM" -n kube-system >/dev/null 2>&1; then
    die "kind cluster '$MGMT_CLUSTER' exists but carries no spike ownership marker; refusing to touch it"
  fi
  [ "$REUSE" = 1 ] || die "spike cluster '$MGMT_CLUSTER' already exists; re-run with --reuse to adopt it (a new RUN_ID will invalidate prior approvals)"
  log "adopting existing spike-owned cluster (new RUN_ID will invalidate prior approvals)"
else
  kind create cluster --name "$MGMT_CLUSTER" --kubeconfig "$KUBECONFIG" --config "$KGEN"
fi
guard_protected
k cluster-info >/dev/null 2>&1 || die "mgmt cluster unreachable"

# P0-2 foundation: prove the apiserver audit log (authoritative DELETE record) is ACTIVE, end to
# end, or fail closed. Version-sensitive kubeadm patch above must produce this.
k create configmap ok-audit-probe -n default --from-literal=x=1 >/dev/null
k delete configmap ok-audit-probe -n default >/dev/null
sleep 3
collect_audit "$AUDIT_DIR/audit.log"
grep -q 'ok-audit-probe' "$AUDIT_DIR/audit.log" \
  || die "apiserver audit logging NOT active — adjust kubeadmConfigPatches extraArgs form for your kind/kubeadm version (v1beta3 map vs v1beta4 list)"
log "apiserver audit logging verified active"

# --- 2. cert-manager: pinned version + verified checksum (no 'latest') ---
cmf="$STATE_DIR/cert-manager-$CERT_MANAGER_VERSION.yaml"
curl -fsSL "https://github.com/cert-manager/cert-manager/releases/download/$CERT_MANAGER_VERSION/cert-manager.yaml" -o "$cmf" \
  || die "failed to download pinned cert-manager $CERT_MANAGER_VERSION"
echo "$CERT_MANAGER_SHA256  $cmf" | shasum -a 256 -c - || die "cert-manager manifest checksum mismatch (supply-chain guard)"
k apply -f "$cmf"
k -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s || die "cert-manager not ready"

# --- 3. CAPI init with in-place gates ON, then verify them on the running controllers ---
export EXP_RUNTIME_SDK=true EXP_IN_PLACE_UPDATES=true
cctl init --core "cluster-api:$CAPI_VERSION" --bootstrap "kubeadm:$CAPI_VERSION" \
          --control-plane "kubeadm:$CAPI_VERSION" --infrastructure "docker:$CAPI_VERSION"
k -n capi-system get deploy capi-controller-manager -o yaml | grep -q "InPlaceUpdates=true" || die "InPlaceUpdates gate missing (core)"
k -n capi-kubeadm-control-plane-system get deploy -o yaml | grep -q "InPlaceUpdates=true" || die "InPlaceUpdates gate missing (KCP)"
k -n capi-system get deploy capi-controller-manager -o yaml | grep -q "RuntimeSDK=true" || die "RuntimeSDK gate missing (CAPI Core)"

# --- 4. test-extension image: validated source version + verified image identity ---
if docker image inspect "$EXT_IMAGE" >/dev/null 2>&1; then
  log "using existing image $EXT_IMAGE"
elif [ -n "$CAPI_SRC" ] && [ -d "$CAPI_SRC/test/extension" ]; then
  srcver="$(git -C "$CAPI_SRC" describe --tags --exact-match 2>/dev/null || true)"
  [ "$srcver" = "$CAPI_VERSION" ] || die "CAPI_SRC is not at $CAPI_VERSION (git describe='$srcver')"
  ( cd "$CAPI_SRC" && docker build -t "$EXT_IMAGE" -f test/extension/Dockerfile . ) || die "extension image build failed"
else
  die "test-extension image '$EXT_IMAGE' absent and CAPI_SRC not a cluster-api $CAPI_VERSION checkout"
fi
EXT_IMAGE_ID="$(docker image inspect --format '{{.Id}}' "$EXT_IMAGE")"
[ -z "${EXT_IMAGE_DIGEST:-}" ] || [ "$EXT_IMAGE_DIGEST" = "$EXT_IMAGE_ID" ] || die "extension image identity mismatch (want $EXT_IMAGE_DIGEST got $EXT_IMAGE_ID)"
kind load docker-image "$EXT_IMAGE" --name "$MGMT_CLUSTER" || die "kind load failed"

# --- 5. Deploy extension + ExtensionConfig; wait for discovery (fail closed) ---
EXT_RENDERED="$STATE_DIR/test-extension-rendered.yaml"

# Pick the extension-manifest source, fail-closed. The pure decision lives in classify.sh
# (unit-tested offline). Vendored manifest is verified with `shasum -a 256 -c -` + version header;
# a missing/wrong SHA or version mismatch aborts. CAPI_SRC render is kept as a fallback.
EXT_SEL="$(bash "$SPIKE_HOME/classify.sh" ext_manifest_select "$EXT_MANIFEST" "$EXT_MANIFEST_SHA256" "$CAPI_VERSION" "$CAPI_SRC" || true)"
case "$EXT_SEL" in
  USE_VENDORED)
    EXT_MANIFEST_SHA256_USED="$EXT_MANIFEST_SHA256"
    cp "$EXT_MANIFEST" "$EXT_RENDERED" ;;
  USE_CAPI_SRC)
    EXT_MANIFEST_SHA256_USED="capi-src-render"
    kubectl kustomize "$CAPI_SRC/test/extension/config/default" > "$EXT_RENDERED" \
      || die "extension render failed" ;;
  FAIL_SHA_MISSING)  die "EXT_MANIFEST_SHA256 required for vendored manifest $EXT_MANIFEST" ;;
  FAIL_SHA_MISMATCH) die "vendored extension manifest sha256 mismatch ($EXT_MANIFEST)" ;;
  FAIL_VERSION)      die "vendored manifest version != $CAPI_VERSION ($EXT_MANIFEST)" ;;
  *)                 die "no vendored manifest ($EXT_MANIFEST) and no CAPI_SRC checkout at $CAPI_VERSION" ;;
esac

EXT_IMAGE="$EXT_IMAGE" yq -i '
  (select(.kind == "Deployment") |
   .spec.template.spec.containers[] |
   select(.name == "manager") |
   .image) = strenv(EXT_IMAGE) |
  (select(.kind == "Deployment") |
   .spec.template.spec.containers[] |
   select(.name == "manager") |
   .imagePullPolicy) = "IfNotPresent"
' "$EXT_RENDERED" || die "extension image patch failed"


# Resolve upstream test-extension placeholders before deployment.
# Kubernetes does not evaluate shell parameter expansions in container args.
EXT_RENDERED="$EXT_RENDERED" python3 - <<'PYARGS'
from pathlib import Path
import os

p = Path(os.environ["EXT_RENDERED"])
s = p.read_text()

replacements = {
    "--diagnostics-address=${CAPI_DIAGNOSTICS_ADDRESS:=:8443}":
        "--diagnostics-address=:8443",
    "--insecure-diagnostics=${CAPI_INSECURE_DIAGNOSTICS:=false}":
        "--insecure-diagnostics=false",
    "--feature-gates=PriorityQueue=${EXP_PRIORITY_QUEUE:=true}":
        "--feature-gates=PriorityQueue=true",
}

for old, new in replacements.items():
    assert s.count(old) == 1, f"Expected exactly one occurrence of {old}"
    s = s.replace(old, new)

p.write_text(s)
PYARGS

if grep -Fq '${' "$EXT_RENDERED"; then
  die "unresolved placeholders in extension manifest"
fi

grep -Fq "image: $EXT_IMAGE" "$EXT_RENDERED" \
  || die "rendered extension does not reference expected image"

k apply -f "$EXT_RENDERED" || die "extension deployment failed"
EXT_DEP="$(ext_deploy)"; [ -n "$EXT_DEP" ] || die "test-extension deployment not found"
k -n "$EXT_NS" rollout status "$EXT_DEP" --timeout=180s || die "extension deploy not ready"
k apply -f "$SPIKE_HOME/extensionconfig.yaml"
for i in $(seq 1 30); do
  [ "$(k get extensionconfig test-extension -o jsonpath='{.status.conditions[?(@.type=="Discovered")].status}' 2>/dev/null)" = "True" ] && break; sleep 4
done
assert_extension_ready test-extension

# --- 6. Provenance: fresh RUN_ID + mgmt UID + versions; invalidate prior approvals ---
RUN_ID="run-$(date +%s)-$(head -c6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
MGMT_UID="$(k get ns kube-system -o jsonpath='{.metadata.uid}')"
[ -n "$MGMT_UID" ] || die "could not read mgmt kube-system UID"
EXTCFG_HASH="$(k get extensionconfig test-extension -o jsonpath='{.spec.settings}' | sha | awk '{print $1}')"
jq -n --arg r "$RUN_ID" --arg m "$MGMT_UID" --arg c "$CAPI_VERSION" --arg i "$EXT_IMAGE_ID" \
      --arg e "$EXTCFG_HASH" --arg cm "$CERT_MANAGER_VERSION" --arg xm "${EXT_MANIFEST_SHA256_USED:-}" \
   '{run_id:$r,mgmt_uid:$m,capi_version:$c,ext_image_id:$i,extcfg_hash:$e,cert_manager:$cm,ext_manifest_sha256:$xm}' > "$RUN_FILE"
k create cm "$OWNER_CM" -n kube-system --from-literal=run-id="$RUN_ID" --dry-run=client -o yaml | k apply -f -
rm -f "$STATE_DIR"/result-*.json "$STATE_DIR"/report-*.txt 2>/dev/null || true   # prior approvals invalidated
log "BOOTSTRAP OK  run_id=$RUN_ID  mgmt_uid=$MGMT_UID  capi=$CAPI_VERSION"
log "Next: ./scenario.sh decline   (safety gate — run FIRST)"
