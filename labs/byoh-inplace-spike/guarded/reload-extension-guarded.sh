#!/usr/bin/env bash
# reload-extension-guarded.sh
#
# Targeted rebuild + reload of the OpenKubes RuntimeSDK in-place extension into the
# ALREADY-RUNNING guarded CAPD management cluster. Does NOT (re)create the cluster,
# touch other colima profiles/kind clusters, run the scenario, clean up, or commit.
#
# Steps: go fmt/build/unit-tests -> docker build extension -> kind load into the
# existing cluster -> apply (adds probes) + rollout restart -> wait discovery +
# readiness -> dump evidence.
#
# Prereqs (same as the E2E): dedicated colima profile selected as the docker daemon
# that hosts the guarded kind cluster, e.g.  DOCKER_CONTEXT=colima-okx-guarded
set -euo pipefail
GUARDED_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-guarded.sh
source "$GUARDED_HOME/lib-guarded.sh"

EV="$STATE_DIR/evidence/reload"; mkdir -p "$EV"
export GOTOOLCHAIN=auto

# --- 0. Preconditions: reload INTO an existing cluster, never create one ----------
kind get clusters 2>/dev/null | grep -qx "$MGMT_CLUSTER" \
  || die "kind cluster '$MGMT_CLUSTER' not found — this script reloads an existing cluster, it does not create one (run ./e2e.sh / bootstrap first)"
[ -n "${DOCKER_CONTEXT:-}" ] || [ -n "${DOCKER_HOST:-}" ] || [ -n "$(docker context show 2>/dev/null)" ] \
  || die "no docker context/DOCKER_HOST selected — select the guarded colima daemon explicitly (e.g. DOCKER_CONTEXT=colima-okx-guarded)"
log "reload target: cluster=$MGMT_CLUSTER  ns=$OKX_EXT_NS  image=$OKX_EXT_IMAGE  docker=${DOCKER_CONTEXT:-$(docker context show 2>/dev/null)}"

# --- 1. go fmt / build / unit tests (go 1.26 via GOTOOLCHAIN=auto) ----------------
cd "$REPO_ROOT"
go version > "$EV/go-version.txt" 2>&1 || true
log "gofmt -l (must be empty)"
fmtout="$(gofmt -l cmd/inplace-extension internal/inplace internal/safety/noreplacement 2>&1 || true)"
printf '%s\n' "$fmtout" > "$EV/gofmt.txt"
[ -z "$fmtout" ] || die "gofmt reported unformatted files (see $EV/gofmt.txt): $fmtout"
log "go build ./..."
go build ./... > "$EV/go-build.txt" 2>&1 || { cat "$EV/go-build.txt"; die "go build failed (see $EV/go-build.txt)"; }
log "go test ./internal/safety/noreplacement/... ./internal/inplace/..."
go test ./internal/safety/noreplacement/... ./internal/inplace/... > "$EV/go-test.txt" 2>&1 \
  || { cat "$EV/go-test.txt"; die "unit tests failed (see $EV/go-test.txt)"; }
log "go build + unit tests PASS"

# --- 2. build extension image only + load into the existing cluster ---------------
log "docker build $OKX_EXT_IMAGE"
docker build -t "$OKX_EXT_IMAGE" -f cmd/inplace-extension/Dockerfile . > "$EV/docker-build.txt" 2>&1 \
  || { tail -40 "$EV/docker-build.txt"; die "extension image build failed (see $EV/docker-build.txt)"; }
log "kind load $OKX_EXT_IMAGE -> $MGMT_CLUSTER"
kind load docker-image "$OKX_EXT_IMAGE" --name "$MGMT_CLUSTER" > "$EV/kind-load.txt" 2>&1 \
  || { cat "$EV/kind-load.txt"; die "kind load failed (see $EV/kind-load.txt)"; }

# --- 3. apply (adds readiness/liveness probes) + force a fresh pod -----------------
cd "$GUARDED_HOME"
EXT_RENDERED="$STATE_DIR/okx-extension.reload.yaml"
OKX_EXT_IMAGE="$OKX_EXT_IMAGE" yq '
  (select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name=="manager") | .image) = strenv(OKX_EXT_IMAGE)
' "$GUARDED_HOME/manifests/extension.yaml" > "$EXT_RENDERED"
k apply -f "$EXT_RENDERED" > "$EV/apply.txt" 2>&1
k -n "$OKX_EXT_NS" rollout restart deploy/inplace-extension >> "$EV/apply.txt" 2>&1
log "waiting for extension rollout (new pod must pass the HTTPS readiness check)"
k -n "$OKX_EXT_NS" rollout status deploy/inplace-extension --timeout=180s > "$EV/rollout.txt" 2>&1 \
  || { k -n "$OKX_EXT_NS" get pods -o wide > "$EV/pods.txt" 2>&1 || true
       k -n "$OKX_EXT_NS" describe deploy/inplace-extension > "$EV/deploy-describe.txt" 2>&1 || true
       k -n "$OKX_EXT_NS" logs deploy/inplace-extension --all-containers --tail=200 > "$EV/ext-logs.txt" 2>&1 || true
       die "extension deploy not ready (see $EV/rollout.txt, pods.txt, ext-logs.txt)"; }

# --- 4. re-check ExtensionConfig discovery (fail closed) ---------------------------
disc=""
for i in $(seq 1 30); do
  disc="$(k get extensionconfig okx-inplace-extension -o jsonpath='{.status.conditions[?(@.type=="Discovered")].status}' 2>/dev/null || true)"
  [ "$disc" = "True" ] && break; sleep 4
done

# --- 5. evidence ------------------------------------------------------------------
k -n "$OKX_EXT_NS" get pods -o wide            > "$EV/pods.txt" 2>&1 || true
k -n "$OKX_EXT_NS" logs deploy/inplace-extension --all-containers --tail=200 > "$EV/ext-logs.txt" 2>&1 || true
k get extensionconfig okx-inplace-extension -o yaml > "$EV/extensionconfig.yaml" 2>&1 || true
k get extensionconfig okx-inplace-extension -o jsonpath='{range .status.handlers[*]}{.name}{"\n"}{end}' > "$EV/discovered-handlers.txt" 2>&1 || true

if [ "$disc" = "True" ]; then
  log "RELOAD OK — extension rebuilt, loaded, running, and ExtensionConfig Discovered=True"
  echo "DISCOVERED HANDLERS:"; cat "$EV/discovered-handlers.txt" 2>/dev/null || true
  echo "RELOAD: PASS"
else
  echo "---- extension logs (tail) ----"; cat "$EV/ext-logs.txt" 2>/dev/null || true
  die "ExtensionConfig not Discovered=True after reload (status='$disc') — see $EV/"
fi
