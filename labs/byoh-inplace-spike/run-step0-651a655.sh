#!/usr/bin/env bash
# Step-0 runner — RUN ON THE OPERATOR'S MAC (Docker host). Prepared for review; not executed by the assistant.
# Builds the guard image from EXACT commit 651a655, loads it into the existing isolated kind cluster,
# runs preflight, then GUARD=1 ./scenario.sh decline (deploy+verify guard, single-event PASS_DENIED check,
# cleanup_guard + verify_guard_gone, then the ownership-verified workload teardown).
# STOP on any error (set -e) or discrepancy. No unprotected retry. No automatic docker-context switch.
# No Scaleway/OpenTofu/firewall/prod-mgmt changes. No commits/pushes.
set -euo pipefail

# ---- config ----
COMMIT="651a655"
IMG="localhost/ok-byoh-no-replacement-guard:${COMMIT}-arm64"
IMG_TAG="${COMMIT}-arm64"
MGMT="ok-byoh-inplace-spike"
SPIKE="$(cd "$(dirname "$0")" && pwd)"
REPO="${REPO:?set REPO=/absolute/path/to/ok-baremetal-byoh}"
KC="$SPIKE/.state/kube/mgmt.kubeconfig"
SCEN_SHA="4e94656feaac638b786d8813910d7b0531d45ce43ab2b91f8846004463d52591"
k(){ kubectl --kubeconfig "$KC" "$@"; }
stop(){ echo "STOP: $*" >&2; exit 1; }
BUILD=""; GDIR_R=""
trap 'rm -rf "${BUILD:-}" "${GDIR_R:-}" 2>/dev/null || true' EXIT

# ---- 0: integrity of the approved harness (byte-identical to the reviewed file) ----
echo "== 0: scenario.sh integrity =="
H="$(shasum -a 256 "$SPIKE/scenario.sh" | cut -d' ' -f1)"
[ "$H" = "$SCEN_SHA" ] || stop "scenario.sh hash $H != approved $SCEN_SHA"
bash -n "$SPIKE/scenario.sh" || stop "scenario.sh failed bash -n"
grep -q 'REG_MOUNT="/etc/openkubes/no-replacement"' "$SPIKE/scenario.sh" || stop "scenario.sh missing /etc registry mount"
grep -q 's#mountPath: /registry#mountPath: \$REG_MOUNT#' "$SPIKE/scenario.sh" || stop "scenario.sh missing registry mount rewrite"
echo "scenario.sh OK ($H)"

# ---- DOCKER ENV PREFLIGHT: pin ONE daemon; no automatic context switch (Colima/DOCKER_HOST safety) ----
echo "== docker-env preflight =="
for t in docker kind kubectl git; do command -v "$t" >/dev/null || stop "missing tool: $t"; done
DCTX="$(docker context show 2>/dev/null || true)"
CTX_HOST="$(docker context inspect "$DCTX" --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true)"
EFF_HOST="${DOCKER_HOST:-$CTX_HOST}"
echo "docker context : ${DCTX:-<none>}"
echo "context host   : ${CTX_HOST:-<unknown>}"
echo "DOCKER_HOST    : ${DOCKER_HOST:-<unset>}"
echo "effective host : ${EFF_HOST:-<unknown>}"
# (1)/(5) DOCKER_HOST overriding the context is the exact Colima multi-profile trap -> STOP, do not switch.
if [ -n "${DOCKER_HOST:-}" ] && [ -n "$CTX_HOST" ] && [ "$DOCKER_HOST" != "$CTX_HOST" ]; then
  stop "DOCKER_HOST ($DOCKER_HOST) overrides docker context '${DCTX}' ($CTX_HOST) - ambiguous daemon. Unset DOCKER_HOST or align the context yourself, then retry. No automatic switch."
fi
docker info >/dev/null 2>&1 || stop "docker daemon not reachable at ${EFF_HOST:-<unknown>}"
# (2) daemon identity (distinct per Colima profile)
DAEMON_ID="$(docker info --format '{{.ID}}')"
echo "daemon id      : $DAEMON_ID"
echo "daemon name    : $(docker info --format '{{.Name}}')"
echo "daemon root    : $(docker info --format '{{.DockerRootDir}}')"
[ -n "$DAEMON_ID" ] || stop "could not read docker daemon id"
# (3) the kind cluster must live on THIS daemon: every node is a container inspectable here, labeled for $MGMT
kind get clusters | grep -qx "$MGMT" || stop "kind cluster $MGMT not present on this daemon (run ./bootstrap.sh first)"
NODES="$(kind get nodes --name "$MGMT" 2>/dev/null || true)"
[ -n "$NODES" ] || stop "kind reports no nodes for $MGMT on this daemon"
for n in $NODES; do
  docker inspect "$n" >/dev/null 2>&1 || stop "kind node '$n' of $MGMT is NOT a container on daemon $DAEMON_ID (kind and docker point at different daemons)"
  lbl="$(docker inspect "$n" --format '{{index .Config.Labels "io.x-k8s.io/cluster"}}' 2>/dev/null || true)"
  [ "$lbl" = "$MGMT" ] || stop "node '$n' missing kind cluster label '$MGMT' on this daemon (got '$lbl')"
done
echo "kind cluster $MGMT confirmed on daemon $DAEMON_ID (nodes: $(echo $NODES))"

# ---- B: build + load from EXACT 651a655 on the SAME pinned daemon (no working-tree changes) ----
echo "== B: build guard image from $COMMIT =="
case "$(uname -m)" in arm64|aarch64) :;; *) stop "host arch $(uname -m) != arm64 (tag is -arm64)";; esac
[ "$(docker info --format '{{.ID}}')" = "$DAEMON_ID" ] || stop "docker daemon changed since preflight"
git -C "$REPO" rev-parse --verify "${COMMIT}^{commit}" >/dev/null 2>&1 || stop "commit $COMMIT not in $REPO"
BUILD="$(mktemp -d)"
git -C "$REPO" archive "$COMMIT" | tar -x -C "$BUILD"
[ -f "$BUILD/cmd/no-replacement-guard/Dockerfile" ] || stop "guard Dockerfile absent in $COMMIT build context"
docker build --platform linux/arm64 --build-arg TARGETOS=linux --build-arg TARGETARCH=arm64 \
  -f "$BUILD/cmd/no-replacement-guard/Dockerfile" -t "$IMG" "$BUILD"
IID="$(docker image inspect "$IMG" --format '{{.Id}}')"
IARCH="$(docker image inspect "$IMG" --format '{{.Architecture}}/{{.Os}}')"
echo "built: $IMG"; echo "  image-id : $IID"; echo "  arch/os  : $IARCH"; echo "  source   : $COMMIT ($(git -C "$REPO" rev-parse "$COMMIT"))"; echo "  daemon   : $DAEMON_ID"
[ "${IARCH%%/*}" = "arm64" ] || stop "image arch $IARCH != arm64"
# (4) load into the SAME daemon's cluster
[ "$(docker info --format '{{.ID}}')" = "$DAEMON_ID" ] || stop "docker daemon changed before kind load"
kind load docker-image "$IMG" --name "$MGMT"
# definitive same-daemon proof: image present INSIDE EVERY kind node (inspect + label + crictl)
LOADED_NODES="$(kind get nodes --name "$MGMT" 2>/dev/null || true)"
[ -n "$LOADED_NODES" ] || stop "kind reports no nodes for $MGMT after load"
for n in $LOADED_NODES; do
  docker inspect "$n" >/dev/null 2>&1 || stop "node $n not a container on daemon $DAEMON_ID after load"
  lbl="$(docker inspect "$n" --format '{{index .Config.Labels "io.x-k8s.io/cluster"}}' 2>/dev/null || true)"
  [ "$lbl" = "$MGMT" ] || stop "node $n missing kind cluster label '$MGMT' on this daemon (got '$lbl')"
  docker exec "$n" crictl images 2>/dev/null | grep -q "$IMG_TAG" \
    || stop "image tag $IMG_TAG not present on kind node $n after kind load - build/load daemon != cluster daemon"
done
echo "verified: $IMG present on ALL $MGMT nodes ($(echo $LOADED_NODES)) - inspect+label+crictl OK, daemon $DAEMON_ID"

# ---- image-reference coupling: render guard manifests from GUARD_IMAGE at runtime (scenario.sh untouched) ----
GDIR_SRC="$SPIKE/../byoh-decline-guard-e2e/guard-isolated"
GDIR_R="$(mktemp -d)"; cp "$GDIR_SRC"/*.yaml "$GDIR_R"/
sed -i.bak "s#image: localhost/ok-byoh-no-replacement-guard:cc4715f-arm64#image: $IMG#" "$GDIR_R/guard-deployment.yaml" && rm -f "$GDIR_R/guard-deployment.yaml.bak"
grep -q "image: $IMG" "$GDIR_R/guard-deployment.yaml" || stop "failed to render deployment image to $IMG"
echo "rendered guard-deployment image -> $IMG (on a copy; on-disk manifests unchanged)"

# ---- C: preflight (kube-level; isolated spike only) ----
echo "== C: preflight =="
CTX="$(k config current-context 2>/dev/null || true)"
[ "$CTX" = "kind-$MGMT" ] || stop "kubeconfig context '$CTX' != kind-$MGMT (refusing to touch any other cluster)"
k -n capi-system get deploy >/dev/null 2>&1 || stop "CAPI (capi-system) not installed"
CAPI_IMGS="$(k get deploy -A -o jsonpath='{range .items[*]}{.spec.template.spec.containers[*].image}{"\n"}{end}' | grep -E 'cluster-api|capi' || true)"
echo "$CAPI_IMGS" | grep -q ':v1.14.4' || stop "no CAPI component at v1.14.4 (found: $CAPI_IMGS)"
CM_YAML="$(k -n capi-system get deploy capi-controller-manager -o yaml 2>/dev/null || true)"
echo "$CM_YAML" | grep -q 'RuntimeSDK=true' || stop "feature gate RuntimeSDK=true not set on capi-controller-manager"
echo "$CM_YAML" | grep -q 'InPlaceUpdates=true' || stop "feature gate InPlaceUpdates=true not set on capi-controller-manager"
[ "$(k get extensionconfig test-extension -o jsonpath='{.status.conditions[?(@.type=="Discovered")].status}' 2>/dev/null)" = "True" ] \
  || stop "ExtensionConfig/test-extension not Discovered"
k get crd certificates.cert-manager.io >/dev/null 2>&1 || stop "cert-manager CRDs missing"
k -n cert-manager get deploy cert-manager cert-manager-webhook cert-manager-cainjector >/dev/null 2>&1 || stop "cert-manager not fully running"
echo "preflight OK (CAPI v1.14.4, gates on, extension Discovered, cert-manager up, context=$CTX)"

# ---- D: run the guarded decline; capture evidence even on failure; STOP on error (no retry) ----
echo "== D: GUARD=1 ./scenario.sh decline =="
set +e
( cd "$SPIKE" && GUARD=1 GUARD_IMAGE="$IMG" GDIR="$GDIR_R" ./scenario.sh decline )
RC=$?
set -e

# ---- evidence bundle + machine-readable manifest + protocol (run-specific; a failure is NEVER marked PASS) ----
RUN_ID="$(jq -r '.run_id // empty' "$SPIKE/.state/run.json" 2>/dev/null || true)"
TS="$(date -u +%Y%m%dT%H%M%SZ)"; [ -n "$RUN_ID" ] || RUN_ID="$TS"
EV="$SPIKE/.state/evidence-step0-$TS"; mkdir -p "$EV"
cp -p "$SPIKE"/.state/*decline* "$EV"/ 2>/dev/null || true
cp -p "$SPIKE"/.state/report-decline.txt "$EV"/ 2>/dev/null || true
# no secrets / kubeconfigs in the evidence bundle (defense in depth; the copy glob should already exclude them)
if find "$EV" -type f \( -iname '*kubeconfig*' -o -iname '*.pem' -o -iname '*.key' -o -iname '*secret*' -o -iname '*token*' \) | grep -q .; then
  find "$EV" -type f \( -iname '*kubeconfig*' -o -iname '*.pem' -o -iname '*.key' -o -iname '*secret*' -o -iname '*token*' \) -delete
  echo "WARNING: removed secret-like files from evidence bundle $EV"
fi
# code provenance (SHA-256 of the exact harness + runner used in THIS run)
SCEN_SHA256="$H"                                   # computed + verified in step 0
RUNNER_SHA256="$(shasum -a 256 "$0" | cut -d' ' -f1)"
LIB_SHA256="$(shasum -a 256 "$SPIKE/lib.sh" | cut -d' ' -f1)"   # scenario.sh sources lib.sh -> part of the tested codebase
# provenance + verdict (actual, from the run)
VERDICT="$(grep -oE 'verdict=[A-Z_]+' "$SPIKE/.state/report-decline.txt" 2>/dev/null | head -1 | cut -d= -f2 || true)"
GIT_FULL="$(git -C "$REPO" rev-parse "$COMMIT" 2>/dev/null || true)"
K8S_VER="$(jq -r '[.items[].status.nodeInfo.kubeletVersion]|unique|join(",")' "$SPIKE/.state/nodes-after-decline.json" 2>/dev/null || true)"
CAPI_VER="$(printf '%s' "${CAPI_IMGS:-}" | grep -oE ':v1\.[0-9.]+' | head -1 | tr -d ':' || true)"
# MANDATORY provenance: OVERALL=PASS requires ALL of these present (else FAIL, regardless of verdict)
MISSING=""
[ -n "$CAPI_VER" ]      || MISSING="$MISSING capi_version"
[ -n "$K8S_VER" ]       || MISSING="$MISSING kubernetes_version"
[ -n "$GIT_FULL" ]      || MISSING="$MISSING guard_source_commit"
[ -n "${IID:-}" ]       || MISSING="$MISSING guard_image_id"
[ -n "${DAEMON_ID:-}" ] || MISSING="$MISSING docker_daemon_id"
MISSING="${MISSING# }"
if [ "$RC" -eq 0 ] && [ "$VERDICT" = "PASS_DENIED" ] && [ -z "$MISSING" ]; then OVERALL=PASS; else OVERALL=FAIL; fi
jq -n --arg run_id "$RUN_ID" --arg ts "$TS" --arg overall "$OVERALL" --arg verdict "${VERDICT:-NONE}" \
      --argjson rc "$RC" --arg commit "${GIT_FULL:-}" --arg tag "$IMG_TAG" --arg img "$IMG" \
      --arg iid "${IID:-}" --arg iarch "${IARCH:-}" --arg capi "${CAPI_VER:-}" --arg k8s "${K8S_VER:-}" \
      --arg daemon "${DAEMON_ID:-}" --arg scen "$SCEN_SHA256" --arg runner "$RUNNER_SHA256" --arg lib "$LIB_SHA256" --arg missing "$MISSING" \
  '{test:"step0-decline-guard", run_id:$run_id, timestamp_utc:$ts, overall:$overall, verdict:$verdict,
    scenario_rc:$rc, guard_source_commit:$commit, guard_image:$img, guard_image_tag:$tag,
    guard_image_id:$iid, guard_image_arch:$iarch, capi_version:$capi, kubernetes_version:$k8s,
    docker_daemon_id:$daemon, scenario_sha256:$scen, runner_sha256:$runner, lib_sha256:$lib,
    missing_provenance:($missing|if .=="" then [] else split(" ") end)}' > "$EV/run-manifest.json"
{
  echo "# Step-0 decline-guard - test protocol"
  echo
  echo "- run_id:            $RUN_ID"
  echo "- timestamp (UTC):   $TS"
  echo "- OVERALL:           $OVERALL"
  echo "- verdict:           ${VERDICT:-NONE}"
  echo "- scenario exit:     $RC"
  echo "- missing provenance: ${MISSING:-none}"
  echo "- guard source:      ${GIT_FULL:-<MISSING>} (requested $COMMIT)"
  echo "- guard image:       $IMG"
  echo "- guard image id:    ${IID:-<MISSING>}  arch: ${IARCH:-<unknown>}"
  echo "- CAPI version:      ${CAPI_VER:-<MISSING>}"
  echo "- Kubernetes (node): ${K8S_VER:-<MISSING>}"
  echo "- docker daemon id:  ${DAEMON_ID:-<MISSING>}"
  echo "- scenario.sh sha256: $SCEN_SHA256"
  echo "- runner sha256:      $RUNNER_SHA256"
  echo "- lib.sh sha256:      $LIB_SHA256"
  echo
  echo "## Expected"
  echo "- at maxSurge:0 on the singleton CP, a declined in-place update makes CAPI attempt a CP Machine DELETE;"
  echo "- the No-Replacement Guard denies it as a genuine policy 403 -> verdict PASS_DENIED;"
  echo "- Machine UID + InfraMachine(DockerMachine) UID preserved; no successful (2xx) delete;"
  echo "- all mandatory provenance fields present."
  echo
  echo "## Actual"
  echo "- verdict=${VERDICT:-NONE}, scenario_rc=$RC, missing_provenance=[${MISSING}] -> OVERALL=$OVERALL"
  echo "- evidence: report-decline.txt, audit-decline.log (windowed), audit-decline.full.log,"
  echo "  observer-decline.ndjson, snap-before/after-decline.json, machineuid-decline.txt, result-decline.json"
  echo
  echo "## Evidence files"
  ls -1 "$EV"
} > "$EV/run-protocol.md"
echo "evidence + manifest + protocol in: $EV (OVERALL=$OVERALL)"

if [ "$OVERALL" != "PASS" ]; then
  stop "Step 0 OVERALL=$OVERALL (rc=$RC verdict=${VERDICT:-NONE} missing_provenance=[${MISSING}]). Evidence in $EV. No retry, no teardown. A failed run is recorded as FAIL, never PASS."
fi
echo "RESULT: PASS_DENIED  (protocol: $EV/run-protocol.md)"
echo "Guard already removed + absence verified inside scenario.sh (verify_guard_gone)."
echo "CAPD mgmt-cluster teardown is NOT performed here. To tear down the disposable mgmt cluster, run: ./teardown.sh"
