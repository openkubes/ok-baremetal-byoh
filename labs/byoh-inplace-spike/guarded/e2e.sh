#!/usr/bin/env bash
# Guarded CAPD E2E entrypoint: preflight -> bootstrap -> scenarios -> evidence report.
# Fully isolated (see lib-guarded.sh / preflight.sh). Creates only its own disposable
# kind cluster. Never touches other clusters, ~/.kube/config, or any external/production infrastructure.
#
#   DOCKER_CONTEXT=colima CERT_MANAGER_VERSION=v1.16.2 CERT_MANAGER_SHA256=<sha> ./e2e.sh
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$HERE/lib-guarded.sh"

echo "=== [1/4] preflight ==="
bash "$HERE/preflight.sh" || { echo "ABORT: preflight failed"; exit 1; }

echo "=== [2/4] bootstrap (kind + CAPI + cert-manager + guard/extension images) ==="
bash "$HERE/bootstrap-guarded.sh" || { echo "ABORT: bootstrap failed"; exit 1; }

echo "=== [3/4] scenarios (activate guard, grant+claim, KCP/MD accept, negatives) ==="
bash "$HERE/scenario-guarded.sh" || true   # scenario records per-phase verdicts even on failure

echo "=== [4/4] evidence report ==="
REP="$STATE_DIR/evidence/EVIDENCE.md"
{
  echo "# Guarded CAPD E2E — Evidence Report"
  echo; echo "- cluster: \`$MGMT_CLUSTER\`  run: \`$(jq -r .run_id "$RUN_FILE" 2>/dev/null)\`"
  echo "- CAPI: \`$CAPI_VERSION\`  guard: \`$GUARD_IMAGE\`  extension: \`$OKX_EXT_IMAGE\`"
  echo; echo "## Phase verdicts"; echo '```json'; cat "$STATE_DIR/evidence/result.json" 2>/dev/null; echo '```'
  for u in kcp md; do
    echo; echo "## UID preservation ($u)"; echo '```json'; cat "$STATE_DIR/evidence/uidcmp-$u.json" 2>/dev/null || echo "(not produced)"; echo '```'
  done
  echo; echo "## Evidence files"; ( cd "$STATE_DIR/evidence" && ls -1 ) 2>/dev/null | sed 's/^/- /'
} > "$REP"
echo "evidence report: $REP"

# Safety assertion: the global kubeconfig must be byte-identical to the preflight baseline.
GLOBAL_KUBE="$HOME/.kube/config"; BASE="$STATE_DIR/evidence/global-kubeconfig.baseline"
if [ -f "$GLOBAL_KUBE" ] && [ -f "$BASE" ]; then
  cur="$( (command -v sha256sum >/dev/null && sha256sum "$GLOBAL_KUBE" || shasum -a 256 "$GLOBAL_KUBE") | awk '{print $1}')"
  [ "$cur" = "$(cat "$BASE")" ] && echo "SAFE: ~/.kube/config unchanged" || echo "WARNING: ~/.kube/config changed since preflight!"
fi
echo "Done. Review $REP ; teardown with ./teardown-guarded.sh"
