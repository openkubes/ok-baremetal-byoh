#!/usr/bin/env bash
# Strict, READ-ONLY preflight for the guarded CAPD E2E. Creates nothing; it only
# verifies the safety preconditions and records baselines. Fail-closed on any check.
#
#   DOCKER_CONTEXT=colima ./preflight.sh
#
# Checks (all must PASS):
#   * explicit docker context (no accidental default/remote daemon)
#   * docker endpoint is LOCAL (never a remote/production host); context name may be 'default'
#   * unique NEW kind name — refuses if a cluster of that name already exists
#   * no protected / OpenKubes cluster names touched
#   * isolated kubeconfig; records ~/.kube/config checksum to prove it is untouched
#   * no external-infrastructure credentials in the environment (provider-neutral)
#   * required tools present; docker daemon reachable
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib-guarded.sh"

FAIL=0
ok(){ echo "  PASS  $*"; }
bad(){ echo "  FAIL  $*"; FAIL=1; }
sha(){ if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

echo "== Guarded CAPD E2E preflight =="
echo "guarded cluster name : $GUARDED_CLUSTER"
echo "isolated KUBECONFIG  : $KUBECONFIG"
echo "state dir            : $STATE_DIR"
echo

# 1. required tools
for t in docker kind kubectl clusterctl jq yq python3 curl; do
  command -v "$t" >/dev/null 2>&1 && ok "tool present: $t" || bad "missing tool: $t"
done

# 2+3. Docker endpoint must be an EXPLICITLY SELECTED, LOCAL endpoint.
# The isolation guarantee is the ENDPOINT being local (unix socket / localhost); the
# context NAME is irrelevant, so a context named 'default' is accepted when its endpoint
# is local. DOCKER_HOST, when set, overrides the context endpoint.
DCTX="$(docker context show 2>/dev/null || true)"
if [ -n "${DOCKER_HOST:-}" ]; then
  EP="$DOCKER_HOST"; SEL="DOCKER_HOST"
else
  EP="$(docker context inspect "${DCTX:-default}" --format '{{ .Endpoints.docker.Host }}' 2>/dev/null || true)"; SEL="context '${DCTX:-default}'"
fi
echo "  docker selection   : ${DOCKER_CONTEXT:+DOCKER_CONTEXT=$DOCKER_CONTEXT; }active context='${DCTX:-}'${DOCKER_HOST:+; DOCKER_HOST set}"
echo "  docker endpoint    : $EP  (via $SEL)"
# Explicit selection: DOCKER_CONTEXT or DOCKER_HOST set, or an active context is present.
if [ -n "${DOCKER_CONTEXT:-}" ] || [ -n "${DOCKER_HOST:-}" ] || [ -n "$DCTX" ]; then
  ok "docker endpoint explicitly selected via $SEL"
else
  bad "no docker context or DOCKER_HOST selected — select one explicitly"
fi
# If DOCKER_CONTEXT is set it must match the active context (no silent mismatch), unless
# DOCKER_HOST overrides the endpoint entirely.
if [ -n "${DOCKER_CONTEXT:-}" ] && [ -z "${DOCKER_HOST:-}" ] && [ "$DOCKER_CONTEXT" != "$DCTX" ]; then
  bad "DOCKER_CONTEXT=$DOCKER_CONTEXT but active context is '$DCTX'"
fi
case "$EP" in
  unix://*|npipe://*|*://localhost|*://localhost:*|*://127.0.0.1|*://127.0.0.1:*|*://0.0.0.0|*://0.0.0.0:*)
    ok "docker endpoint is local (isolation guarantee)" ;;
  "") bad "could not read docker endpoint" ;;
  *) bad "docker endpoint is REMOTE ($EP) — refusing; isolation requires a local endpoint (no production/remote daemon)" ;;
esac

# 4. docker daemon reachable
docker info >/dev/null 2>&1 && ok "docker daemon reachable" || bad "docker daemon not reachable via context $DCTX"

# 4b. inotify instances on the daemon's kernel (read-only). The workload runs 3 CP + 1
# worker kind-style nodes; with the common default of 128, systemd in later node
# containers exits with "Too many open files" and the Machine never provisions.
INO="$(docker run --rm busybox cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null || true)"
if [ -n "$INO" ] && [ "$INO" -ge 512 ]; then ok "fs.inotify.max_user_instances=$INO (>=512)"
else bad "fs.inotify.max_user_instances=${INO:-unknown} (<512) — raise it on the dedicated VM, e.g. colima ssh -p okx-guarded -- sudo sysctl -w fs.inotify.max_user_instances=512"; fi

# 5. unique NEW kind name — must NOT already exist (never overwrite a cluster)
EXISTING="$(kind get clusters 2>/dev/null || true)"
echo "  existing kind clusters: $(echo "$EXISTING" | tr '\n' ' ')"
if echo "$EXISTING" | grep -qx "$GUARDED_CLUSTER"; then
  bad "a kind cluster named '$GUARDED_CLUSTER' ALREADY EXISTS — refusing to overwrite; pick a new GUARDED_CLUSTER or teardown first"
else
  ok "kind name '$GUARDED_CLUSTER' is new (no overwrite)"
fi

# 5b. The target Docker daemon must host NO FOREIGN kind clusters. This enforces a
# DEDICATED, EMPTY lab profile (its own colima profile / docker daemon) and makes it
# impossible to operate on a daemon that hosts pre-existing clusters — so existing
# profiles and clusters are never touched. Override: REQUIRE_DEDICATED_DAEMON=0 (warn only).
FOREIGN="$(echo "$EXISTING" | grep -v '^$' | grep -vx "$GUARDED_CLUSTER" || true)"
if [ -n "$FOREIGN" ]; then
  if [ "${REQUIRE_DEDICATED_DAEMON:-1}" = "0" ]; then
    echo "  WARN  foreign kind clusters present on this daemon: $(echo $FOREIGN | tr '\n' ' ')"
  else
    bad "this Docker daemon hosts other kind clusters: $(echo $FOREIGN | tr '\n' ' ') — refusing. Use a DEDICATED EMPTY colima profile (own daemon) for the guarded E2E; existing profiles/clusters are never touched. (set REQUIRE_DEDICATED_DAEMON=0 only if you accept a shared daemon)"
  fi
else
  ok "no foreign kind clusters on this daemon (dedicated, empty lab profile)"
fi

# 6. protected names never collide
COLL=0
for p in "${PROTECTED_CLUSTERS[@]}"; do [ "$GUARDED_CLUSTER" = "$p" ] && COLL=1; done
[ "$COLL" = 0 ] && ok "guarded name does not collide with protected clusters (${PROTECTED_CLUSTERS[*]})" \
  || bad "guarded name collides with a protected cluster"

# 7. isolated kubeconfig; record ~/.kube/config checksum baseline (prove untouched later)
case "$KUBECONFIG" in
  "$STATE_DIR"/*) ok "KUBECONFIG is isolated under the guarded state dir" ;;
  *) bad "KUBECONFIG is not isolated: $KUBECONFIG" ;;
esac
GLOBAL_KUBE="$HOME/.kube/config"
BASE="$STATE_DIR/evidence/global-kubeconfig.baseline"
if [ -f "$GLOBAL_KUBE" ]; then
  sha "$GLOBAL_KUBE" | awk '{print $1}' > "$BASE"
  ok "recorded ~/.kube/config checksum baseline ($(cat "$BASE" | cut -c1-12)…); e2e asserts it is unchanged"
else
  echo "ABSENT" > "$BASE"; ok "no ~/.kube/config present (nothing global to protect)"
fi

# 8. No EXTERNAL INFRASTRUCTURE CREDENTIALS in the environment (provider-neutral).
# The CAPD lab must be fully local and must not be able to reach ANY external/cloud
# infrastructure. The pattern below is a NON-EXHAUSTIVE list of KNOWN credential
# examples across providers (Scaleway is only one recognized example) — it is a
# convenience recognizer, NOT a provider-specific security contract. The contract is
# simply: no external-infrastructure credentials present.
EXT_CRED_RE='^(SCW_SECRET_KEY|SCW_ACCESS_KEY|SCALEWAY_API_TOKEN|HCLOUD_TOKEN|HETZNER_API_TOKEN|AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY|AWS_SESSION_TOKEN|GOOGLE_APPLICATION_CREDENTIALS|GCLOUD_SERVICE_KEY|AZURE_CLIENT_SECRET|ARM_CLIENT_SECRET|OS_PASSWORD|OS_APPLICATION_CREDENTIAL_SECRET|DIGITALOCEAN_TOKEN|DO_API_TOKEN|LINODE_TOKEN|VULTR_API_KEY|OVH_APPLICATION_SECRET|OK_SCW_SECRET|BYOH_SSH_KEY|BYOH_SSH_PRIVATE_KEY)'
EXT_CRED_HIT="$(env | grep -iE "$EXT_CRED_RE" | cut -d= -f1 | tr '\n' ' ' || true)"
[ -z "$EXT_CRED_HIT" ] && ok "no external infrastructure credentials in environment" \
  || bad "external infrastructure credentials present ($EXT_CRED_HIT) — refusing (CAPD lab must be fully local)"

# 9. repo build inputs present
[ -f "$REPO_ROOT/cmd/no-replacement-guard/Dockerfile" ] && ok "guard Dockerfile present" || bad "guard Dockerfile missing"
[ -f "$REPO_ROOT/cmd/inplace-extension/Dockerfile" ] && ok "extension Dockerfile present" || bad "extension Dockerfile missing"

echo
if [ "$FAIL" = 0 ]; then
  echo "PREFLIGHT: PASS — safe to run ./e2e.sh"
  exit 0
else
  echo "PREFLIGHT: FAIL — fix the FAIL lines above; nothing was created."
  exit 1
fi
