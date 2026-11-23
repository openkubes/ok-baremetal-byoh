#!/usr/bin/env bash
# Guarded CAPD E2E scenarios. Proves the GUARDED in-place lifecycle end to end.
# Heavy evidence capture; each phase writes to .state-guarded/evidence so a failure
# still yields diagnostics. Verdicts are recorded per phase (PASS/FAIL/PENDING).
#
# Targeted re-run: PHASES="kcp md" ./scenario-guarded.sh  (verdicts merge into result.json)
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib-guarded.sh"
set +e   # lib-guarded.sh enables -e; phases record FAIL verdicts instead of aborting the run
require_bootstrapped; load_run
EV="$STATE_DIR/evidence"; RESULT="$EV/result.json"
declare -A VERDICT
rec(){ VERDICT["$1"]="$2"; log "PHASE $1 => $2"; }
guard_logs(){ k -n "$GUARD_NS" logs deploy/no-replacement-guard --tail=400 > "$EV/guard-$1.log" 2>&1 || true; }
# A negative only counts if rejected BY THE GUARD webhook (not RBAC / another validator).
guard_denied(){ grep -q 'admission webhook ".*no-replacement.openkubes.org" denied' "$1"; }
guard_denied_with(){ guard_denied "$1" && grep -qF "$2" "$1"; }   # $1 file $2 expected reason fragment
# Privileged non-actor: cluster-admin (system:masters) passes RBAC, so only the guard can deny.
ADMIN_USER="kubernetes-admin"; ADMIN_GROUP="system:masters"

WL="${WL:-ipw-guarded}"
MD="${MD:-worker-md-0}"   # MachineDeployment name as defined in capd-classic-template.yaml
CP_REPLICAS="${CP_REPLICAS:-3}"
PHASES="${PHASES:-provision activate kcp md negatives}"
CP_SEL="cluster.x-k8s.io/control-plane,cluster.x-k8s.io/cluster-name=$WL"
MD_SEL="cluster.x-k8s.io/cluster-name=$WL,cluster.x-k8s.io/deployment-name=$MD"
A_INPROG="in-place-updates.internal.cluster.x-k8s.io/update-in-progress"
A_HOOKS="runtime.cluster.x-k8s.io/pending-hooks"
A_PACK="in-place-updates.internal.cluster.x-k8s.io/pending-acknowledge-move"
A_ACK="in-place-updates.internal.cluster.x-k8s.io/acknowledged-move"

# In-place timeline observer: samples Machine identity/owner/version/in-place annotations
# every 2s. Corroborates hook start (in-progress / pending-hooks seen) and completion.
OBS_PID=""
start_obs(){ # $1 selector $2 outfile
  local sel="$1" out="$2"; : > "$out"
  { while :; do
      k get machines -n "$WL_NS" -l "$sel" -o json 2>/dev/null | jq -c --arg t "$(date +%s)" '{t:($t|tonumber),m:[.items[]|{
          name:.metadata.name,uid:.metadata.uid,version:.spec.version,
          owner:([.metadata.ownerReferences[]?|select(.controller==true)|.kind+"/"+.name]|first),
          inprog:(.metadata.annotations["'"$A_INPROG"'"]!=null),
          hooks:(.metadata.annotations["'"$A_HOOKS"'"]//""),
          pack:(.metadata.annotations["'"$A_PACK"'"]!=null),
          deleting:(.metadata.deletionTimestamp!=null)}]}' >> "$out" 2>/dev/null
      sleep 2
    done; } >/dev/null 2>&1 &
  OBS_PID=$!
}
stop_obs(){ [ -n "$OBS_PID" ] && kill "$OBS_PID" 2>/dev/null; wait "$OBS_PID" 2>/dev/null; OBS_PID=""; }

# All machines for selector at TARGET and free of in-place markers (hook completion + cleanup).
converged(){ # $1 selector $2 expected count
  k get machines -n "$WL_NS" -l "$1" -o json 2>/dev/null | jq -e --arg v "$TARGET_VERSION" --argjson n "$2" '
    (.items|length)==$n and all(.items[];
      .spec.version==$v and .metadata.deletionTimestamp==null and
      (.metadata.annotations["'"$A_INPROG"'"]==null) and
      ((.metadata.annotations["'"$A_HOOKS"'"]//"")|test("UpdateMachine")|not) and
      (.metadata.annotations["'"$A_PACK"'"]==null))' >/dev/null
}

# Timeline verdict: hook started (in-progress or UpdateMachine pending-hook seen) for every machine.
hook_seen_all(){ # $1 timeline file $2 selector
  local names; names="$(k get machines -n "$WL_NS" -l "$2" -o jsonpath='{.items[*].name}')"
  for n in $names; do
    jq -e -s --arg n "$n" 'any(.[].m[]; .name==$n and (.inprog or (.hooks|test("UpdateMachine"))))' "$1" >/dev/null || return 1
  done
}

# ---------- Phase 0: provision workload (guard NOT yet active) ----------
phase_provision(){
  if k get cluster "$WL" -n "$WL_NS" >/dev/null 2>&1; then
    log "workload $WL already exists — reusing (no re-provision)"
  else
    sed -e "s/ipw-decline/$WL/g" "$SPIKE_HOME/capd-classic-template.yaml" \
      | sed -e "s/version: v1.33.0/version: $BASE_VERSION/g" > "$STATE_DIR/workload.yaml"
    # No surge: an upgrade must never scale up a NEW Machine, so CAPI can only converge
    # in-place. CAPI v1.14 KCP attempts in-place ONLY in its scale-down step (update.go
    # rollingUpdate) and rejects maxSurge=0 below 3 replicas — hence CP_REPLICAS=3.
    CP_REPLICAS="$CP_REPLICAS" yq -i '(select(.kind=="KubeadmControlPlane").spec.replicas)=(strenv(CP_REPLICAS)|tonumber)' "$STATE_DIR/workload.yaml"
    yq -i '(select(.kind=="KubeadmControlPlane").spec.rollout.strategy)={"type":"RollingUpdate","rollingUpdate":{"maxSurge":0}}' "$STATE_DIR/workload.yaml"
    yq -i '(select(.kind=="MachineDeployment").spec.rollout.strategy)={"type":"RollingUpdate","rollingUpdate":{"maxSurge":0,"maxUnavailable":1}}' "$STATE_DIR/workload.yaml"
    k apply -f "$STATE_DIR/workload.yaml" || { rec provision FAIL; return 1; }
  fi
  k label cluster "$WL" -n "$WL_NS" "$OWNER_KEY=$RUN_ID" --overwrite
  # The CAPD template ships no CNI; without one Nodes never become Ready. Install the
  # kindnet manifest pinned to CAPI's own e2e data at $CAPI_VERSION (checksum-verified).
  k wait --for=jsonpath='{.status.conditions[?(@.type=="ControlPlaneInitialized")].status}'=True "cluster/$WL" -n "$WL_NS" --timeout=900s || { rec provision FAIL; return 1; }
  local cni="$STATE_DIR/cni-kindnet-capi-$CAPI_VERSION.yaml"
  [ -s "$cni" ] || curl -fsSL "https://raw.githubusercontent.com/kubernetes-sigs/cluster-api/$CAPI_VERSION/test/e2e/data/cni/kindnet/kindnet.yaml" -o "$cni"
  echo "${CNI_SHA256:-01af8774780d5899f3e03136c004f628f7fbf75fbadad0010a6cec27a2c6ed5f}  $cni" | shasum -a 256 -c - >/dev/null || { log "CNI checksum mismatch"; rec provision FAIL; return 1; }
  sed "s#'\${DOCKER_POD_CIDRS},\${DOCKER_POD_IPV6_CIDRS}'#'192.168.0.0/16'#" "$cni" | wk "$WL" "$WL_NS" apply -f - || { rec provision FAIL; return 1; }
  log "waiting for control plane + workers to be available (up to 30m)…"
  # v1beta2 condition names (ControlPlaneReady / MD Ready do not exist in v1beta2). JSONPath
  # instead of --for=condition: the latter also requires observedGeneration==generation,
  # which a paused Cluster (spec.paused bumps generation) never reports.
  k wait --for=jsonpath='{.status.conditions[?(@.type=="ControlPlaneAvailable")].status}'=True "cluster/$WL" -n "$WL_NS" --timeout=1200s \
    && k wait kubeadmcontrolplane/controlplane -n "$WL_NS" --for=jsonpath='{.status.availableReplicas}'="$CP_REPLICAS" --timeout=900s \
    && k wait --for=jsonpath='{.status.conditions[?(@.type=="Available")].status}'=True "machinedeployment/$MD" -n "$WL_NS" --timeout=600s \
    && k wait machinedeployment/"$MD" -n "$WL_NS" --for=jsonpath='{.status.availableReplicas}'=1 --timeout=600s \
    && rec provision PASS || { rec provision FAIL; snapshot provision-fail "$WL_NS"; return 1; }
  WORKLOAD="$WL" capture_uids before "$WL" "$WL_NS"
  k get machines -n "$WL_NS" -l "$MD_SEL" -o json | jq '[.items[]|{name:.metadata.name,uid:.metadata.uid,owner:[.metadata.ownerReferences[]|select(.controller)|{kind,name,uid}]}]' > "$EV/md-owner-before.json"
  k get machinesets -n "$WL_NS" -l "$MD_SEL" -o json | jq '[.items[]|{name:.metadata.name,uid:.metadata.uid,replicas:.spec.replicas}]' > "$EV/ms-before.json"
}

# ---------- Phase 1: activate the guard (registry bound to workload UID) ----------
phase_activate_guard(){
  local cuid; cuid="$(k get cluster "$WL" -n "$WL_NS" -o jsonpath='{.metadata.uid}')"
  [ -n "$cuid" ] || { rec activate FAIL; return 1; }
  jq -n --arg ns "$WL_NS" --arg name "$WL" --arg uid "$cuid" \
     '{registrations:[{namespace:$ns,clusterName:$name,clusterUID:$uid}]}' > "$STATE_DIR/registry.json"
  OKX_GUARD_IMAGE="$GUARD_IMAGE" yq '
    (select(.kind=="Deployment") | .spec.template.spec.containers[] | select(.name=="guard") | .image) = strenv(OKX_GUARD_IMAGE)
  ' "$GUARDED_HOME/manifests/guard.yaml" > "$STATE_DIR/guard.yaml"
  k apply -f "$STATE_DIR/guard.yaml" || { rec activate FAIL; return 1; }
  k create cm no-replacement-registry -n "$GUARD_NS" \
     --from-file=registry.json="$STATE_DIR/registry.json" --dry-run=client -o yaml | k apply -f -
  k -n "$GUARD_NS" wait --for=condition=Ready certificate/no-replacement-guard-serving-cert --timeout=120s
  k -n "$GUARD_NS" rollout restart deploy/no-replacement-guard
  k -n "$GUARD_NS" rollout status deploy/no-replacement-guard --timeout=180s || { rec activate FAIL; guard_logs activate; return 1; }
  k apply -f "$GUARDED_HOME/manifests/webhook.yaml"
  for i in $(seq 1 30); do   # wait for cert-manager CA injection on the VWC
    [ -n "$(k get validatingwebhookconfiguration okx-no-replacement-guard -o jsonpath='{.webhooks[0].clientConfig.caBundle}')" ] && break; sleep 2
  done
  # activate protection on the workload namespace LAST
  k label ns "$WL_NS" "$PROTECT_LABEL=true" --overwrite
  # liveness of enforcement: an unauthorized Machine DELETE must now be rejected by the guard (dry-run)
  # Retried: right after a rollout restart the Service may still route to the terminating
  # pod (timeout / connection refused — still fail-closed). Only a real guard DENY counts.
  local any verdict=FAIL; any="$(k get machines -n "$WL_NS" -l "$CP_SEL" -o jsonpath='{.items[0].metadata.name}')"
  for i in $(seq 1 20); do
    if k delete machine "$any" -n "$WL_NS" --dry-run=server >"$EV/activate-probe.txt" 2>&1; then verdict=FAIL; break; fi
    guard_denied "$EV/activate-probe.txt" && { verdict=PASS; break; }
    sleep 3
  done
  rec activate "$verdict"
  guard_logs activate
}

# ---------- grant helpers (ADR-047: approver creates data, initiator sets claim) ----------
payload_digest(){ echo -n "okx-e2e-payload-$TARGET_VERSION" | shasum -a 256 | awk '{print "sha256:"$1}'; }
make_grant(){ # $1 grantID $2 targetKind $3 targetName $4 machineUID(or "") $5 transitions [$6 as-user [$7 as-group [$8 targetUID]]]
  local gid="$1" tk="$2" tn="$3" muid="$4" tr="$5" as="${6:-$APPROVER_SA}" grp="${7:-}" tuid="${8:-}"
  # $8 overrides the target UID (a synthetic, live-object-free target for protocol-only negatives)
  [ -n "$tuid" ] || tuid="$(k get "${tk,,}" "$tn" -n "$WL_NS" -o jsonpath='{.metadata.uid}' 2>/dev/null)"
  local now plus; now="$(date -u +%s)"; plus="$(date -u -d '@'"$((now+3600))" +%FT%TZ 2>/dev/null || date -u -r "$((now+3600))" +%FT%TZ)"
  cat > "$STATE_DIR/$gid.yaml" <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: okx-grant-$gid
  namespace: $WL_NS
  labels: { safety.openkubes.org/grant: "true" }
data:
  grantID: "$gid"
  clusterUID: "$(k get cluster "$WL" -n "$WL_NS" -o jsonpath='{.metadata.uid}')"
  targetKind: "$tk"
  targetName: "$tn"
  targetUID: "$tuid"
  $( [ -n "$muid" ] && echo "machineUID: \"$muid\"" )
  fromVersion: "$BASE_VERSION"
  toVersion: "$TARGET_VERSION"
  transitions: "$tr"
  payloadDigest: "$(payload_digest)"
  notAfter: "$plus"
  permittedUseCount: "1"
  requestDigest: "sha256:req-$gid"
  authorizationDigest: "sha256:auth-$gid"
YAML
  # approver creates (impersonated); the claim is a separate initiator write
  k --as="$as" ${grp:+--as-group="$grp"} create -f "$STATE_DIR/$gid.yaml"
}
claim_grant(){ # $1 grantID [$2 as-user [$3 as-group]] ; adds the single claim slot via a merge patch
  local gid="$1" as="${2:-$INITIATOR_SA}" grp="${3:-}"
  k --as="$as" ${grp:+--as-group="$grp"} patch cm "okx-grant-$gid" -n "$WL_NS" --type merge \
     -p "{\"data\":{\"claim\":\"claim-$gid-$(date +%s)\"}}"
}

# ---------- Phase 2: KCP in-place accept (one grant per CP Machine, bound by machineUID) ----------
phase_kcp(){
  local ok=0 uid i=0
  local cp1 cp1uid; cp1="$(k get machines -n "$WL_NS" -l "$CP_SEL" -o jsonpath='{.items[0].metadata.name}')"
  cp1uid="$(k get machine "$cp1" -n "$WL_NS" -o jsonpath='{.metadata.uid}')"
  local bump="{\"spec\":{\"version\":\"$TARGET_VERSION\"}}"
  # NEG missing grant: persistent version bump while NO grant exists for this Machine
  if [ -z "$(k get cm -n "$WL_NS" -o json | jq -r --arg u "$cp1uid" '.items[]|select(.data.machineUID==$u)|.metadata.name')" ]; then
    if k patch machine "$cp1" -n "$WL_NS" --type merge -p "$bump" >"$EV/neg-missing-grant.txt" 2>&1; then rec neg_missing_grant FAIL
    else guard_denied_with "$EV/neg-missing-grant.txt" "requires an authorized upgrade grant" && rec neg_missing_grant PASS || rec neg_missing_grant FAIL; fi
  fi
  for uid in $(k get machines -n "$WL_NS" -l "$CP_SEL" -o jsonpath='{.items[*].metadata.uid}'); do
    i=$((i+1))
    # Idempotent re-run: an existing grant must already be bound to THIS Machine and claimed.
    if k get cm "okx-grant-kcp-$i" -n "$WL_NS" >/dev/null 2>&1; then
      [ "$(k get cm "okx-grant-kcp-$i" -n "$WL_NS" -o jsonpath='{.data.machineUID}')" = "$uid" ] \
        && [ -n "$(k get cm "okx-grant-kcp-$i" -n "$WL_NS" -o jsonpath='{.data.claim}')" ] \
        && { log "reusing claimed grant kcp-$i for Machine $uid"; continue; }
      ok=1; continue
    fi
    make_grant "kcp-$i" KubeadmControlPlane controlplane "$uid" "KCP-1,KCP-2,KCP-3,KCP-C1,KCP-C2" || { ok=1; continue; }
    # NEG unclaimed grant: a matching but UNCLAIMED grant must not authorize a persistent write
    if [ "$uid" = "$cp1uid" ]; then
      if k patch machine "$cp1" -n "$WL_NS" --type merge -p "$bump" >"$EV/neg-unclaimed-grant.txt" 2>&1; then rec neg_unclaimed_grant FAIL
      else guard_denied_with "$EV/neg-unclaimed-grant.txt" "has not been claimed" && rec neg_unclaimed_grant PASS || rec neg_unclaimed_grant FAIL; fi
    fi
    claim_grant "kcp-$i" || ok=1
  done
  [ "$i" = "$CP_REPLICAS" ] && [ "$ok" = 0 ] && rec kcp_grant PASS || { rec kcp_grant FAIL; guard_logs kcp-grant; return 1; }
  start_obs "$CP_SEL" "$EV/timeline-kcp.jsonl"
  k patch kubeadmcontrolplane controlplane -n "$WL_NS" --type merge -p "{\"spec\":{\"version\":\"$TARGET_VERSION\"}}"
  # A paused workload (e.g. held while the guard was redeployed) resumes only now,
  # after the timeline observer runs, so every in-place step is recorded.
  k patch cluster "$WL" -n "$WL_NS" --type merge -p '{"spec":{"paused":false}}'
  ok=1
  for i in $(seq 1 90); do converged "$CP_SEL" "$CP_REPLICAS" && { ok=0; break; }; sleep 10; done
  sleep 6; stop_obs
  guard_logs kcp
  snapshot kcp-after "$WL_NS"
  WORKLOAD="$WL" capture_uids after-kcp "$WL" "$WL_NS"
  local hooks=1; hook_seen_all "$EV/timeline-kcp.jsonl" "$CP_SEL" && hooks=0
  k get kubeadmcontrolplane controlplane -n "$WL_NS" -o json | jq '{version:.spec.version,status:{replicas:.status.replicas,upToDate:.status.upToDateReplicas,available:.status.availableReplicas},conditions:[.status.conditions[]|{type,status,reason}]}' > "$EV/kcp-final.json"
  log "kcp: converged=$ok hook-observed=$hooks"
  if [ "$ok" = 0 ] && [ "$hooks" = 0 ] && assert_uids_preserved before after-kcp "$EV/uidcmp-kcp.json"; then rec kcp_accept PASS; else rec kcp_accept FAIL; fi
}

# ---------- Phase 3: MD in-place accept (real MachineSet source-move) ----------
phase_md(){
  make_grant md MachineDeployment "$MD" "" "MD-MOVE,MD-FIN-M,MD-TRIG,MD-C1,MD-C2,MD-ACK" \
    && claim_grant md || { rec md_grant FAIL; guard_logs md-grant; return 1; }
  rec md_grant PASS
  start_obs "$MD_SEL" "$EV/timeline-md.jsonl"
  k patch machinedeployment "$MD" -n "$WL_NS" --type merge -p "{\"spec\":{\"template\":{\"spec\":{\"version\":\"$TARGET_VERSION\"}}}}"
  local ok=1
  for i in $(seq 1 90); do converged "$MD_SEL" 1 && { ok=0; break; }; sleep 10; done
  sleep 6; stop_obs
  guard_logs md
  snapshot md-after "$WL_NS"
  WORKLOAD="$WL" capture_uids after-md "$WL" "$WL_NS"
  k get machines -n "$WL_NS" -l "$MD_SEL" -o json | jq '[.items[]|{name:.metadata.name,uid:.metadata.uid,owner:[.metadata.ownerReferences[]|select(.controller)|{kind,name,uid}]}]' > "$EV/md-owner-after.json"
  k get machinesets -n "$WL_NS" -l "$MD_SEL" -o json | jq '[.items[]|{name:.metadata.name,uid:.metadata.uid,replicas:.spec.replicas,annotations:(.metadata.annotations//{}|with_entries(select(.key|test("in-place"))))}]' > "$EV/ms-after.json"
  # Source-move proof: same Machine UID, controller owner moved to a DIFFERENT MachineSet.
  local moved=1
  jq -e -n --slurpfile b "$EV/md-owner-before.json" --slurpfile a "$EV/md-owner-after.json" '
    ($b[0]|length)==1 and ($a[0]|length)==1 and $b[0][0].uid==$a[0][0].uid and
    $b[0][0].owner[0].uid != $a[0][0].owner[0].uid' >/dev/null && moved=0
  local hooks=1; hook_seen_all "$EV/timeline-md.jsonl" "$MD_SEL" && hooks=0
  log "md: converged=$ok source-moved=$moved hook-observed=$hooks"
  if [ "$ok" = 0 ] && [ "$moved" = 0 ] && [ "$hooks" = 0 ] && assert_uids_preserved before after-md "$EV/uidcmp-md.json"; then rec md_accept PASS; else rec md_accept FAIL; fi
}

# ---------- Phase 4: PASS_DENIED + negative security cases ----------
# Each negative must be rejected BY THE GUARD (admission webhook "denied"), not by RBAC
# or another validator — otherwise it would be a false PASS.
phase_negatives(){
  local cpname cpuid; cpname="$(k get machines -n "$WL_NS" -l "$CP_SEL" -o jsonpath='{.items[0].metadata.name}')"
  cpuid="$(k get machine "$cpname" -n "$WL_NS" -o jsonpath='{.metadata.uid}')"
  # (a) PASS_DENIED (direct admin DELETE): denied by the guard AND exactly this request is in the
  #     audit log — correlated by time window, actor, target, ResponseComplete 403, NOT dry-run.
  local t0; t0="$(( $(date -u +%s) - 5 ))"   # small allowance for host/VM clock skew
  if k delete machine "$cpname" -n "$WL_NS" --wait=false >"$EV/neg-cp-delete.txt" 2>&1; then rec pass_denied FAIL
  else
    sleep 3; collect_audit "$EV/audit-final.log"
    jq -c --arg n "$cpname" --arg ns "$WL_NS" --argjson t0 "$t0" 'select(
        .stage=="ResponseComplete" and .verb=="delete" and .objectRef.resource=="machines"
        and .objectRef.namespace==$ns and .objectRef.name==$n and .user.username=="kubernetes-admin"
        and .responseStatus.code==403 and ((.requestObject.dryRun // [])|length)==0
        and ((.requestReceivedTimestamp[0:19]+"Z")|fromdateiso8601) >= $t0)
      | {auditID,ts:.requestReceivedTimestamp,user:.user.username,code:.responseStatus.code,msg:.responseStatus.message}' \
      "$EV/audit-final.log" > "$EV/pass-denied-audit.jsonl" 2>/dev/null
    if guard_denied_with "$EV/neg-cp-delete.txt" "Machine DELETE is forbidden" && [ "$(wc -l < "$EV/pass-denied-audit.jsonl")" -eq 1 ] \
       && grep -q 'no-replacement.openkubes.org' "$EV/pass-denied-audit.jsonl"; then rec pass_denied PASS
    else rec pass_denied FAIL; fi
  fi
  # The CAPI-controller fallback (a controller-initiated replacement DELETE/CREATE denied by the guard,
  # e.g. a singleton KCP surge) is NOT exercised by the 3-replica/maxSurge-0 workload -> separate verdict.
  rec pass_denied_controller_fallback PENDING
  # (b) version mismatch: a claimed grant exists (v1.33->v1.34) but the write goes to v1.35
  if k patch machine "$cpname" -n "$WL_NS" --type merge -p '{"spec":{"version":"v1.35.0"}}' >"$EV/neg-version-mismatch.txt" 2>&1; then rec neg_version_mismatch FAIL
  else guard_denied_with "$EV/neg-version-mismatch.txt" "version transition does not match" && rec neg_version_mismatch PASS || rec neg_version_mismatch FAIL; fi
  # Grants are undeletable during the protected lifecycle (by design) -> unique IDs per run.
  local sfx; sfx="$(date +%s)"
  # Protocol-only negatives bind a SYNTHETIC target/Machine UID: a second (claimed) grant on a live
  # Machine would make GrantFor ambiguous and, as grants are undeletable, block that Machine.
  local syn_t syn_m; syn_t="$(uuidgen | tr 'A-Z' 'a-z')"; syn_m="$(uuidgen | tr 'A-Z' 'a-z')"
  # (c) forged grant: created by a privileged NON-approver (cluster-admin, RBAC allows) must be denied by the guard
  if make_grant "forged-$sfx" KubeadmControlPlane neg-target "$syn_m" "KCP-2" "$ADMIN_USER" "$ADMIN_GROUP" "$syn_t" >"$EV/neg-forged.txt" 2>&1; then rec neg_forged_grant FAIL
  else guard_denied_with "$EV/neg-forged.txt" "only the approver may create" && rec neg_forged_grant PASS || rec neg_forged_grant FAIL; fi
  # (d) foreign requester: a valid grant claimed by a non-initiator must be denied by the guard —
  #     cluster-admin, and the approver itself (role separation), both with RBAC permitting the write
  make_grant "neg-$sfx" KubeadmControlPlane neg-target "$syn_m" "KCP-2" "" "" "$syn_t" >"$EV/neg-create.txt" 2>&1 || { rec neg_foreign_requester FAIL; rec neg_single_claim FAIL; guard_logs negatives; return 1; }
  local f1=1 f2=1
  claim_grant "neg-$sfx" "$ADMIN_USER" "$ADMIN_GROUP" >"$EV/neg-foreign.txt" 2>&1 || { guard_denied_with "$EV/neg-foreign.txt" "only the upgrade initiator" && f1=0; }
  claim_grant "neg-$sfx" "$APPROVER_SA" "$ADMIN_GROUP" >"$EV/neg-foreign-approver.txt" 2>&1 || { guard_denied_with "$EV/neg-foreign-approver.txt" "only the upgrade initiator" && f2=0; }
  [ "$f1" = 0 ] && [ "$f2" = 0 ] && rec neg_foreign_requester PASS || rec neg_foreign_requester FAIL
  # (e) two CONCURRENT initiator claims: exactly one wins, the other is rejected by the guard
  ( claim_grant "neg-$sfx" >"$EV/neg-claim1.txt" 2>&1; echo $? >"$EV/neg-claim1.rc" ) &
  ( claim_grant "neg-$sfx" >"$EV/neg-claim2.txt" 2>&1; echo $? >"$EV/neg-claim2.rc" ) &
  wait
  local wins=0 denied=0
  for n in 1 2; do
    [ "$(cat "$EV/neg-claim$n.rc")" = 0 ] && wins=$((wins+1))
    guard_denied_with "$EV/neg-claim$n.txt" "already been claimed" && denied=$((denied+1))
  done
  echo "wins=$wins guard-denied=$denied claim=$(k get cm "okx-grant-neg-$sfx" -n "$WL_NS" -o jsonpath='{.data.claim}')" > "$EV/neg-claim-summary.txt"
  [ "$wins" = 1 ] && [ "$denied" = 1 ] && rec neg_single_claim PASS || rec neg_single_claim FAIL
  # (f) no replacement after all negatives: CP Machine UIDs unchanged vs. post-KCP snapshot
  WORKLOAD="$WL" capture_uids after-neg "$WL" "$WL_NS"
  assert_uids_preserved after-kcp after-neg "$EV/uidcmp-neg.json" && rec neg_no_replacement PASS || rec neg_no_replacement FAIL
  guard_logs negatives
}

# ---------- run selected phases + write (merged) result ----------
for p in $PHASES; do
  case "$p" in
    provision) phase_provision || true ;;
    activate)  phase_activate_guard || true ;;
    kcp)       phase_kcp || true ;;
    md)        phase_md || true ;;
    negatives) phase_negatives || true ;;
    *) log "unknown phase $p" ;;
  esac
done

new="$(for key in "${!VERDICT[@]}"; do printf '%s\t%s\n' "$key" "${VERDICT[$key]}"; done | jq -R -s 'split("\n")|map(select(length>0)|split("\t")|{(.[0]):.[1]})|add // {}')"
old="$(cat "$RESULT" 2>/dev/null || echo '{}')"
jq -n --argjson o "$old" --argjson n "$new" '$o + $n' > "$RESULT"
log "RESULTS: $(jq -c . "$RESULT")"
