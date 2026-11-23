#!/usr/bin/env bash
# Pure, file-driven classification + provenance. No cluster access; every function takes file
# paths so it is unit-tested offline (test/offline-test.sh). Dispatch: classify.sh <fn> <args...>
#
# Evidence model (r4):
#   * AUTHORITATIVE DELETE-request evidence = the apiserver AUDIT LOG (records every DELETE
#     request, including denied ones). Polling can NEVER prove "no DELETE was requested".
#   * Polling of Machine state is CORROBORATION only: it can positively detect a deletion
#     (deletionTimestamp / vanished uid) but its "retention observed" is a weak signal.
#   * Any gap in either source => INCOMPLETE => fail closed.
#   * This spike can evidence "CAPI did not REQUEST a CP delete in this run"; it does NOT prove
#     "deletion cannot occur" (only an admission webhook with failurePolicy=Fail prevents it).
set -uo pipefail
MIN_SAMPLES="${MIN_SAMPLES:-3}"

# ---- polling corroboration: RETENTION_OBSERVED | DELETED | INCOMPLETE ------------------------
# observer.ndjson lines: {"t":<epoch>,"cp":[{name,uid,deletionTimestamp}]} | {"t":..,"error":".."}
#                  final: {"t":..,"status":"complete"} written ONLY on clean intentional stop.
delete_evidence() {
  local obs="${1:?observer ndjson}"
  [ -s "$obs" ] || { echo INCOMPLETE; return; }
  jq -s . "$obs" >/dev/null 2>&1 || { echo INCOMPLETE; return; }      # any unparseable line
  grep -q '"error"' "$obs" && { echo INCOMPLETE; return; }            # any failed observation
  grep -q '"status":"complete"' "$obs" || { echo INCOMPLETE; return; } # observer not cleanly stopped
  local samples; samples="$(grep -c '"cp":' "$obs" || true)"
  [ "${samples:-0}" -ge "$MIN_SAMPLES" ] || { echo INCOMPLETE; return; }
  if jq -s -e 'any(.[]?|.cp//[]|.[]?; (.deletionTimestamp//null)!=null)' "$obs" >/dev/null 2>&1; then
    echo DELETED; return
  fi
  local first last gone
  first="$(jq -s '[.[]?|select(.cp!=null)][0].cp//[]|map(.uid)' "$obs" 2>/dev/null || echo '[]')"
  last="$(jq -s '[.[]?|select(.cp!=null)][-1].cp//[]|map(.uid)' "$obs" 2>/dev/null || echo '[]')"
  gone="$(jq -n --argjson f "$first" --argjson l "$last" '[$f[]|select(. as $u|($l|index($u))|not)]|length' 2>/dev/null || echo 0)"
  [ "${gone:-0}" -gt 0 ] && { echo DELETED; return; }
  echo RETENTION_OBSERVED
}

# ---- AUTHORITATIVE DELETE-request evidence --------------------------------------------------
# NO_REQUEST | REQUESTED_SUCCEEDED | REQUESTED_DENIED | REQUESTED_OTHER | INCOMPLETE
#   * A "request" is any audit event with verb=delete on a matching Machine, at ANY stage
#     (so an in-flight / RequestReceived-only delete can never read as NO_REQUEST).
#   * Outcome is taken from ResponseComplete events: 2xx=SUCCEEDED, 403/Forbidden=DENIED,
#     anything else that was requested but not cleanly succeeded/denied (409, 5xx, in-flight)
#     = REQUESTED_OTHER (still a requested CP deletion).
# audit.log = apiserver audit events (JSON per line). names = comma-sep CP Machine names (""=any).
audit_delete_evidence() {
  local log="${1:?audit log}" names="${2:-}"
  [ -s "$log" ] || { echo INCOMPLETE; return; }
  jq -s . "$log" >/dev/null 2>&1 || { echo INCOMPLETE; return; }
  local cpjson verdict
  cpjson="$(printf '%s' "$names" | jq -R 'split(",")|map(select(length>0))' 2>/dev/null)" || { echo INCOMPLETE; return; }
  verdict="$(jq -r -s --argjson cp "$cpjson" '
      def mine: select(($cp|length)==0 or (.objectRef.name as $x | ($cp|index($x))!=null));
      def del: select((.verb//"")=="delete" and (.objectRef.resource//"")=="machines") | mine;
      [ .[] | del ] as $req
      | [ $req[] | select((.stage//"")=="ResponseComplete") ] as $done
      | if   ($req|length)==0 then "NO_REQUEST"
        elif ([$done[]|select((.responseStatus.code//0)>=200 and (.responseStatus.code//0)<300)]|length>0) then "REQUESTED_SUCCEEDED"
        elif ([$done[]|select((.responseStatus.code//0)==403 or (.responseStatus.reason//"")=="Forbidden")]|length>0) then "REQUESTED_DENIED"
        else "REQUESTED_OTHER" end' "$log" 2>/dev/null)" || { echo INCOMPLETE; return; }
  [ -n "$verdict" ] || { echo INCOMPLETE; return; }
  echo "$verdict"
}

# ---- fallback actually exercised, bound to THIS workload + its CP Machine --------------------
fallback_exercised() {
  local extlog="${1:?ext log}" kcp="${2:?kcp json}" wl="${3:?workload}" names="${4:-}"
  # kcp.json must belong to this workload
  local owner; owner="$(jq -r '.metadata.labels["cluster.x-k8s.io/cluster-name"] // ""' "$kcp" 2>/dev/null)"
  [ "$owner" = "$wl" ] || { echo NO; return; }
  # CanUpdateMachine must have been called FOR one of this workload's CP machines (same log line)
  local hit=1 name
  IFS=',' read -ra arr <<< "$names"
  for name in "${arr[@]}"; do
    [ -n "$name" ] || continue
    grep -F "CanUpdateMachine is called" "$extlog" 2>/dev/null | grep -qF "$name" && { hit=0; break; }
  done
  [ "$hit" = 0 ] || { echo NO; return; }
  local gen obs upd rep
  gen="$(jq -r '.metadata.generation // 0' "$kcp" 2>/dev/null)"
  obs="$(jq -r '.status.observedGeneration // -1' "$kcp" 2>/dev/null)"
  [ "$obs" -ge "$gen" ] 2>/dev/null || { echo NO; return; }
  upd="$(jq -r '.status.updatedReplicas // 0' "$kcp" 2>/dev/null)"
  rep="$(jq -r '.status.replicas // 0' "$kcp" 2>/dev/null)"
  if [ "$upd" -lt "$rep" ] 2>/dev/null; then echo YES; else echo NO; fi
}

# ---- provenance: PASS result must be bound to the CURRENT run on ALL identity fields ---------
provenance() {
  local res="${1:?result json}" run="${2:?run json}"
  [ -s "$res" ] && [ -s "$run" ] || { echo "provenance: missing file" >&2; return 1; }
  local ok; ok="$(jq -n --slurpfile r "$res" --slurpfile n "$run" '
    ($r[0]) as $x | ($n[0]) as $y |
    (($x.safety//"")=="PASS")
      and ($x.run_id      == $y.run_id)
      and ($x.mgmt_uid    == $y.mgmt_uid)
      and ($x.capi_version== $y.capi_version)
      and ($x.ext_image_id== $y.ext_image_id)
      and ($x.extcfg_hash == $y.extcfg_hash)
      and (($x.workload_uid//"")|length>0)
      and (($x.machine_uid//"")|length>0)
  ' 2>/dev/null)"
  [ "$ok" = "true" ] || { echo "provenance: result not bound to current run (stale/incomplete/not PASS)" >&2; return 1; }
  return 0
}

# ---- overall decline verdict ----------------------------------------------------------------
# PASS_NO_DELETE_REQUESTED | PASS_DENIED | FAIL_DESTRUCTIVE | FAIL_NOT_EXERCISED | FAIL_INCOMPLETE
decline_verdict() {
  local log="$1" obs="$2" extlog="$3" kcp="$4" wl="$5" names="${6:-}"
  local au po fx
  au="$(audit_delete_evidence "$log" "$names")"
  po="$(delete_evidence "$obs")"
  fx="$(fallback_exercised "$extlog" "$kcp" "$wl" "$names")"
  { [ "$au" = INCOMPLETE ] || [ "$po" = INCOMPLETE ]; } && { echo FAIL_INCOMPLETE; return; }
  { [ "$au" = REQUESTED_SUCCEEDED ] || [ "$po" = DELETED ]; } && { echo FAIL_DESTRUCTIVE; return; }
  # A CP delete was REQUESTED but neither cleanly succeeded nor was admission-denied (409, 5xx,
  # in-flight): the "no delete requested" property is violated.
  [ "$au" = REQUESTED_OTHER ] && { echo FAIL_DELETE_ATTEMPTED; return; }
  if [ "$au" = REQUESTED_DENIED ]; then
    [ "$fx" = YES ] && echo PASS_DENIED || echo FAIL_NOT_EXERCISED; return
  fi
  # au = NO_REQUEST, po = RETENTION_OBSERVED
  [ "$fx" = YES ] || { echo FAIL_NOT_EXERCISED; return; }
  echo PASS_NO_DELETE_REQUESTED
}

# ---- tiny pure guards (used by teardown/scenario; unit-tested) -------------------------------
owns() { [ -n "${1:-}" ] && [ "${1:-}" = "${2:-}" ]; }          # $1 object-label-runid  $2 current-runid
can_delete_cluster() { [ "${1:-}" = 1 ] && [ "${2:-}" = 1 ]; }  # $1 api_reachable(0/1)  $2 owner_marker_present(0/1)

# ---- vendored test-extension manifest selection (pure; unit-tested) --------------------------
# Decides which manifest source bootstrap uses, fail-closed. Prints one verdict token:
#   USE_VENDORED | USE_CAPI_SRC | FAIL_SHA_MISSING | FAIL_SHA_MISMATCH | FAIL_VERSION | FAIL_NO_SOURCE
# $1 manifest_path  $2 expected_sha256  $3 capi_version  $4 capi_src_dir(optional)
ext_manifest_select() {
  local man="${1:-}" sha="${2:-}" ver="${3:-}" src="${4:-}"
  if [ -f "$man" ]; then
    [ -n "$sha" ] || { echo FAIL_SHA_MISSING; return 1; }
    printf '%s  %s\n' "$sha" "$man" | shasum -a 256 -c - >/dev/null 2>&1 \
      || { echo FAIL_SHA_MISMATCH; return 1; }
    grep -Fxq "# capi-test-extension-version: $ver" "$man" \
      || { echo FAIL_VERSION; return 1; }
    echo USE_VENDORED; return 0
  elif [ -n "$src" ] && [ -d "$src/test/extension/config/default" ]; then
    echo USE_CAPI_SRC; return 0
  fi
  echo FAIL_NO_SOURCE; return 1
}

"${1:?fn}" "${@:2}"
