#!/usr/bin/env bash
# Offline unit tests for classify.sh (r4). Pure logic, NO cluster. bash + jq only.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLS="$HERE/../classify.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
PASS=0; FAIL=0
ok(){ echo "ok   - $1"; PASS=$((PASS+1)); }
no(){ echo "FAIL - $1"; FAIL=$((FAIL+1)); }
eq(){ [ "$2" = "$3" ] && ok "$1 (=$2)" || no "$1 (got '$2' want '$3')"; }

mk(){ local f="$1"; shift; printf '%s\n' "$@" > "$f"; }   # mk FILE lines...
S(){ local d="$2"; [ "$d" = null ] || d="\"$d\""; echo "{\"t\":$1,\"cp\":[{\"name\":\"cp0\",\"uid\":\"u1\",\"deletionTimestamp\":$d}]}"; }
ERR(){ echo "{\"t\":$1,\"error\":\"query-failed\"}"; }
DONE='{"t":999,"status":"complete"}'
OBS="$TMP/obs"; AUD="$TMP/aud"; EXT="$TMP/ext"; KCP="$TMP/kcp"

# ================= delete_evidence (polling corroboration) =================
mk "$OBS" "$(S 1 null)" "$(S 2 null)" "$(S 3 null)" "$DONE"
eq "poll RETENTION_OBSERVED" "$(bash "$CLS" delete_evidence "$OBS")" "RETENTION_OBSERVED"
mk "$OBS" "$(S 1 null)" "$(S 2 null)" "$(S 3 2026-01-01T00:00:00Z)" "$DONE"
eq "poll DELETED(dts)" "$(bash "$CLS" delete_evidence "$OBS")" "DELETED"
mk "$OBS" "$(S 1 null)" "$(S 2 null)" '{"t":3,"cp":[]}' "$DONE"
eq "poll DELETED(gone)" "$(bash "$CLS" delete_evidence "$OBS")" "DELETED"
mk "$OBS" "$(S 1 null)" "$(S 2 null)" "$(S 3 null)"
eq "poll INCOMPLETE(interrupted/no sentinel)" "$(bash "$CLS" delete_evidence "$OBS")" "INCOMPLETE"
mk "$OBS" "$(S 1 null)" "$DONE"
eq "poll INCOMPLETE(<min samples)" "$(bash "$CLS" delete_evidence "$OBS")" "INCOMPLETE"
mk "$OBS" "$(S 1 null)" "$(ERR 2)" "$(S 3 null)" "$DONE"
eq "poll INCOMPLETE(API-failure error sample)" "$(bash "$CLS" delete_evidence "$OBS")" "INCOMPLETE"
: > "$OBS"
eq "poll INCOMPLETE(empty)" "$(bash "$CLS" delete_evidence "$OBS")" "INCOMPLETE"
printf '%s\n' '{bad json' > "$OBS"
eq "poll INCOMPLETE(unparseable)" "$(bash "$CLS" delete_evidence "$OBS")" "INCOMPLETE"

# ================= audit_delete_evidence (authoritative) =================
AEV(){ echo "{\"verb\":\"$1\",\"objectRef\":{\"resource\":\"$2\",\"name\":\"$3\"},\"stage\":\"ResponseComplete\",\"responseStatus\":{\"code\":$4}}"; }
AEVS(){ echo "{\"verb\":\"$1\",\"objectRef\":{\"resource\":\"$2\",\"name\":\"$3\"},\"stage\":\"$4\"}"; }   # no responseStatus
mk "$AUD" "$(AEV list machines cp0 200)"
eq "audit NO_REQUEST" "$(bash "$CLS" audit_delete_evidence "$AUD" cp0)" "NO_REQUEST"
mk "$AUD" "$(AEV delete machines cp0 200)"
eq "audit REQUESTED_SUCCEEDED" "$(bash "$CLS" audit_delete_evidence "$AUD" cp0)" "REQUESTED_SUCCEEDED"
mk "$AUD" "$(AEV delete machines cp0 403)"
eq "audit REQUESTED_DENIED(403)" "$(bash "$CLS" audit_delete_evidence "$AUD" cp0)" "REQUESTED_DENIED"
mk "$AUD" "$(AEV delete machines cp0 409)"
eq "audit REQUESTED_OTHER(409 conflict)" "$(bash "$CLS" audit_delete_evidence "$AUD" cp0)" "REQUESTED_OTHER"
mk "$AUD" "$(AEVS delete machines cp0 RequestReceived)"
eq "audit REQUESTED_OTHER(in-flight, no ResponseComplete)" "$(bash "$CLS" audit_delete_evidence "$AUD" cp0)" "REQUESTED_OTHER"
mk "$AUD" "$(AEVS delete machines cp0 RequestReceived)" "$(AEV delete machines cp0 200)"
eq "audit REQUESTED_SUCCEEDED(req+complete)" "$(bash "$CLS" audit_delete_evidence "$AUD" cp0)" "REQUESTED_SUCCEEDED"
mk "$AUD" "$(AEV delete machines other-wl 200)"
eq "audit NO_REQUEST(foreign machine filtered)" "$(bash "$CLS" audit_delete_evidence "$AUD" cp0)" "NO_REQUEST"
: > "$AUD"
eq "audit INCOMPLETE(empty)" "$(bash "$CLS" audit_delete_evidence "$AUD" cp0)" "INCOMPLETE"
printf '%s\n' 'not json' > "$AUD"
eq "audit INCOMPLETE(unparseable)" "$(bash "$CLS" audit_delete_evidence "$AUD" cp0)" "INCOMPLETE"

# ================= fallback_exercised (bound to workload + CP name) =================
mk "$EXT" 'INFO CanUpdateMachine is called  Machine="default/cp0"'
echo '{"metadata":{"generation":2,"labels":{"cluster.x-k8s.io/cluster-name":"ipw-decline"}},"status":{"observedGeneration":2,"updatedReplicas":0,"replicas":1}}' > "$KCP"
eq "fallback YES(bound)" "$(bash "$CLS" fallback_exercised "$EXT" "$KCP" ipw-decline cp0)" "YES"
eq "fallback NO(name not referenced)" "$(bash "$CLS" fallback_exercised "$EXT" "$KCP" ipw-decline other)" "NO"
echo '{"metadata":{"generation":2,"labels":{"cluster.x-k8s.io/cluster-name":"someone-else"}},"status":{"observedGeneration":2,"updatedReplicas":0,"replicas":1}}' > "$TMP/kcpf"
eq "fallback NO(foreign kcp)" "$(bash "$CLS" fallback_exercised "$EXT" "$TMP/kcpf" ipw-decline cp0)" "NO"
mk "$EXT" 'nothing relevant'
eq "fallback NO(no hook line)" "$(bash "$CLS" fallback_exercised "$EXT" "$KCP" ipw-decline cp0)" "NO"
echo '{"metadata":{"generation":2,"labels":{"cluster.x-k8s.io/cluster-name":"ipw-decline"}},"status":{"observedGeneration":1,"updatedReplicas":0,"replicas":1}}' > "$TMP/kcpg"
mk "$EXT" 'CanUpdateMachine is called  Machine="default/cp0"'
eq "fallback NO(gen behind)" "$(bash "$CLS" fallback_exercised "$EXT" "$TMP/kcpg" ipw-decline cp0)" "NO"

# ================= provenance (full identity incl image + extcfg) =================
echo '{"run_id":"R1","mgmt_uid":"M1","capi_version":"v1.14.4","ext_image_id":"sha256:img","extcfg_hash":"H1"}' > "$TMP/run"
good(){ echo '{"safety":"PASS","run_id":"R1","mgmt_uid":"M1","capi_version":"v1.14.4","ext_image_id":"sha256:img","extcfg_hash":"H1","workload_uid":"W1","machine_uid":"MU1"}'; }
good > "$TMP/res"; bash "$CLS" provenance "$TMP/res" "$TMP/run" 2>/dev/null && ok "prov match" || no "prov match"
good | jq '.run_id="R0"'                 > "$TMP/res"; bash "$CLS" provenance "$TMP/res" "$TMP/run" 2>/dev/null && no "prov stale run_id" || ok "prov stale run_id rejected"
good | jq '.ext_image_id="sha256:other"' > "$TMP/res"; bash "$CLS" provenance "$TMP/res" "$TMP/run" 2>/dev/null && no "prov image mismatch" || ok "prov image mismatch rejected"
good | jq '.extcfg_hash="HX"'            > "$TMP/res"; bash "$CLS" provenance "$TMP/res" "$TMP/run" 2>/dev/null && no "prov extcfg mismatch" || ok "prov extcfg mismatch rejected"
good | jq '.safety="FAIL"'               > "$TMP/res"; bash "$CLS" provenance "$TMP/res" "$TMP/run" 2>/dev/null && no "prov non-PASS" || ok "prov non-PASS rejected"
good | jq 'del(.machine_uid)'            > "$TMP/res"; bash "$CLS" provenance "$TMP/res" "$TMP/run" 2>/dev/null && no "prov missing machine_uid" || ok "prov missing machine_uid rejected"

# ================= pure guards (ownership / teardown) =================
bash "$CLS" owns R1 R1 && ok "owns match" || no "owns match"
bash "$CLS" owns R1 R2 && no "owns foreign" || ok "owns foreign rejected"
bash "$CLS" owns "" R1  && no "owns empty-label" || ok "owns empty-label rejected"
bash "$CLS" can_delete_cluster 1 1 && ok "can_delete when api+owner ok" || no "can_delete ok"
bash "$CLS" can_delete_cluster 0 1 && no "can_delete api-unreachable" || ok "can_delete refused (API unreachable)"
bash "$CLS" can_delete_cluster 1 0 && no "can_delete no-owner" || ok "can_delete refused (no ownership)"

# ================= decline_verdict =================
mk "$OBS" "$(S 1 null)" "$(S 2 null)" "$(S 3 null)" "$DONE"
mk "$AUD" "$(AEV list machines cp0 200)"
mk "$EXT" 'CanUpdateMachine is called  Machine="default/cp0"'
echo '{"metadata":{"generation":2,"labels":{"cluster.x-k8s.io/cluster-name":"ipw-decline"}},"status":{"observedGeneration":2,"updatedReplicas":0,"replicas":1}}' > "$KCP"
eq "verdict PASS_NO_DELETE_REQUESTED" "$(bash "$CLS" decline_verdict "$AUD" "$OBS" "$EXT" "$KCP" ipw-decline cp0)" "PASS_NO_DELETE_REQUESTED"
mk "$AUD" "$(AEV delete machines cp0 200)"
eq "verdict FAIL_DESTRUCTIVE(audit)" "$(bash "$CLS" decline_verdict "$AUD" "$OBS" "$EXT" "$KCP" ipw-decline cp0)" "FAIL_DESTRUCTIVE"
mk "$AUD" "$(AEV delete machines cp0 409)"
eq "verdict FAIL_DELETE_ATTEMPTED(409)" "$(bash "$CLS" decline_verdict "$AUD" "$OBS" "$EXT" "$KCP" ipw-decline cp0)" "FAIL_DELETE_ATTEMPTED"
mk "$AUD" "$(AEV delete machines cp0 403)"
eq "verdict PASS_DENIED" "$(bash "$CLS" decline_verdict "$AUD" "$OBS" "$EXT" "$KCP" ipw-decline cp0)" "PASS_DENIED"
mk "$AUD" "$(AEV list machines cp0 200)"; mk "$EXT" 'nothing'
eq "verdict FAIL_NOT_EXERCISED" "$(bash "$CLS" decline_verdict "$AUD" "$OBS" "$EXT" "$KCP" ipw-decline cp0)" "FAIL_NOT_EXERCISED"
mk "$EXT" 'CanUpdateMachine is called  Machine="default/cp0"'; mk "$OBS" "$(S 1 null)"
eq "verdict FAIL_INCOMPLETE(poll gap)" "$(bash "$CLS" decline_verdict "$AUD" "$OBS" "$EXT" "$KCP" ipw-decline cp0)" "FAIL_INCOMPLETE"
mk "$OBS" "$(S 1 null)" "$(S 2 null)" "$(S 3 null)" "$DONE"; : > "$AUD"
eq "verdict FAIL_INCOMPLETE(audit gap)" "$(bash "$CLS" decline_verdict "$AUD" "$OBS" "$EXT" "$KCP" ipw-decline cp0)" "FAIL_INCOMPLETE"

# ================= ext_manifest_select (vendored manifest gating) =================
VMAN="$TMP/vendor.yaml"
printf '%s\n' '# capi-test-extension-version: v1.14.4' 'apiVersion: v1' 'kind: Namespace' > "$VMAN"
VSHA="$(shasum -a 256 "$VMAN" | cut -d' ' -f1)"
eq "ext select USE_VENDORED"     "$(bash "$CLS" ext_manifest_select "$VMAN" "$VSHA" v1.14.4)" "USE_VENDORED"
eq "ext select FAIL_SHA_MISSING" "$(bash "$CLS" ext_manifest_select "$VMAN" "" v1.14.4)" "FAIL_SHA_MISSING"
eq "ext select FAIL_SHA_MISMATCH" "$(bash "$CLS" ext_manifest_select "$VMAN" deadbeef v1.14.4)" "FAIL_SHA_MISMATCH"
eq "ext select FAIL_VERSION"     "$(bash "$CLS" ext_manifest_select "$VMAN" "$VSHA" v9.9.9)" "FAIL_VERSION"
SRCD="$TMP/capi"; mkdir -p "$SRCD/test/extension/config/default"
eq "ext select USE_CAPI_SRC"     "$(bash "$CLS" ext_manifest_select "$TMP/none.yaml" "" v1.14.4 "$SRCD")" "USE_CAPI_SRC"
eq "ext select FAIL_NO_SOURCE"   "$(bash "$CLS" ext_manifest_select "$TMP/none.yaml" "" v1.14.4 "$TMP/missing")" "FAIL_NO_SOURCE"
bash "$CLS" ext_manifest_select "$VMAN" "$VSHA" v1.14.4 >/dev/null && ok "ext rc USE_VENDORED=0" || no "ext rc USE_VENDORED=0"
bash "$CLS" ext_manifest_select "$VMAN" deadbeef v1.14.4 >/dev/null && no "ext rc mismatch nonzero" || ok "ext rc mismatch nonzero"
bash "$CLS" ext_manifest_select "$TMP/none.yaml" "" v1.14.4 "$TMP/missing" >/dev/null && no "ext rc no-source nonzero" || ok "ext rc no-source nonzero"

echo "-----"; echo "PASS=$PASS FAIL=$FAIL"; [ "$FAIL" -eq 0 ]
