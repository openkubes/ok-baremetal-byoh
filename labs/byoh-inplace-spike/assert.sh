#!/usr/bin/env bash
# Complete, evidence-based assertions. Uses before/after snapshots + LIVE queries (logs,
# conditions, events). Emits a human summary + a machine-readable result JSON. Observed facts
# only; the stub's capability boundary (no real node upgrade) is asserted, not assumed away.
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
SCENARIO="${1:?scenario}"; TARGET="${2:?target}"; NS="${3:-default}"
WORKLOAD="ipw-$SCENARIO"
B="$STATE_DIR/snap-before-$SCENARIO.json"; A="$STATE_DIR/snap-after-$SCENARIO.json"
[ -f "$B" ] && [ -f "$A" ] || die "missing snapshots for $SCENARIO"
RESULT="$STATE_DIR/result-$SCENARIO.json"; FAILED=0
pass(){ echo "PASS  $1"; }
fail(){ echo "FAIL  $1"; FAILED=1; }
uids(){ jq -r --arg k "$1" '[.items[]|select(.kind==$k)|.metadata.uid]|sort|join(",")' "$2"; }
names(){ jq -r --arg k "$1" '[.items[]|select(.kind==$k)|.metadata.name]|sort|join(",")' "$2"; }
cp_count(){ jq -r '[.items[]|select(.kind=="Machine" and (.metadata.labels["cluster.x-k8s.io/control-plane"]!=null))]|length' "$1"; }

echo "=========== ASSERTIONS: $SCENARIO (target $TARGET) ==========="

# #1 identity preserved (Machine + infra-machine UIDs)
[ "$(uids Machine "$B")" = "$(uids Machine "$A")" ] && pass "#1 Machine UIDs unchanged" || fail "#1 Machine UIDs changed"
[ "$(uids DockerMachine "$B")" = "$(uids DockerMachine "$A")" ] && pass "#1 infra-machine(DockerMachine) UIDs unchanged" || fail "#1 infra-machine UIDs changed"

# #4 no unexpected create/delete (names/count + events)
[ "$(names Machine "$B")" = "$(names Machine "$A")" ] && pass "#4 Machine set identical" || fail "#4 Machine set changed: [$(names Machine "$B")]->[$(names Machine "$A")]"
created="$(k get events -n "$NS" --field-selector reason=SuccessfulCreate -o json 2>/dev/null | jq -r --arg w "$WORKLOAD" '[.items[]|select(.involvedObject.name|startswith($w))]|length')"
deleted="$(k get events -n "$NS" --field-selector reason=SuccessfulDelete -o json 2>/dev/null | jq -r --arg w "$WORKLOAD" '[.items[]|select(.involvedObject.name|startswith($w))]|length')"
echo "    events: SuccessfulCreate=$created SuccessfulDelete=$deleted (workload $WORKLOAD)"

# #6 unpaused final state
paused="$(k get cluster "$WORKLOAD" -n "$NS" -o jsonpath='{.spec.paused}' 2>/dev/null)"
[ -z "$paused" ] || [ "$paused" = "false" ] && pass "#6 cluster not paused" || fail "#6 cluster paused=$paused"

# #7 capability boundary: did workload NODE kubelet actually change? (stub: expected NO)
nb="$STATE_DIR/nodes-before-$SCENARIO.json"; na="$STATE_DIR/nodes-after-$SCENARIO.json"
if [ -f "$nb" ] && [ -f "$na" ]; then
  nv_b="$(jq -r '[.items[].status.nodeInfo.kubeletVersion]|unique|join(",")' "$nb")"
  nv_a="$(jq -r '[.items[].status.nodeInfo.kubeletVersion]|unique|join(",")' "$na")"
  echo "    Node kubeletVersion: before=[$nv_b] after=[$nv_a]"
  echo "    CAPABILITY BOUNDARY: the stub FAKES the update — real node kubelet is NOT upgraded."
  echo "    => Do NOT read Machine.spec.version==TARGET as Kubernetes convergence on the node."
fi

case "$SCENARIO" in
  decline)
    load_run
    CLS="$SPIKE_HOME/classify.sh"
    OBS="$STATE_DIR/observer-$SCENARIO.ndjson"; AUD="$STATE_DIR/audit-$SCENARIO.log"
    EXTLOG="$STATE_DIR/extlog-$SCENARIO.txt"; KCPJ="$STATE_DIR/kcp-$SCENARIO.json"
    CPN="$(cat "$STATE_DIR/cpnames-$SCENARIO.txt" 2>/dev/null)"
    wl_uid="$(k get cluster "$WORKLOAD" -n "$NS" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
    m_uid="$(cat "$STATE_DIR/machineuid-$SCENARIO.txt" 2>/dev/null)"
    # Authoritative DELETE-request evidence = apiserver AUDIT LOG; polling only corroborates.
    au="$(bash "$CLS" audit_delete_evidence "$AUD" "$CPN")"
    po="$(bash "$CLS" delete_evidence "$OBS")"
    fx="$(bash "$CLS" fallback_exercised "$EXTLOG" "$KCPJ" "$WORKLOAD" "$CPN")"
    verdict="$(bash "$CLS" decline_verdict "$AUD" "$OBS" "$EXTLOG" "$KCPJ" "$WORKLOAD" "$CPN")"
    echo "    audit(DELETE-request)=$au  poll(corroboration)=$po  fallback=$fx  ->  verdict=$verdict"
    echo "    audit states: NO_REQUEST · REQUESTED_DENIED · REQUESTED_SUCCEEDED · REQUESTED_OTHER · INCOMPLETE"
    case "$verdict" in
      PASS_NO_DELETE_REQUESTED) pass "#5 SAFETY: audit shows NO CP-Machine DELETE was requested, and the fallback WAS exercised (observed behavior — NOT a prevention guarantee)"; SAFETY=PASS;;
      PASS_DENIED)              pass "#5 SAFETY: a CP DELETE was requested but ADMISSION-DENIED (webhook), fallback exercised"; SAFETY=PASS;;
      FAIL_DESTRUCTIVE)         fail "#5 SAFETY VIOLATION: a CP-Machine DELETE succeeded/completed on decline"; SAFETY=FAIL;;
      FAIL_DELETE_ATTEMPTED)    fail "#5 SAFETY VIOLATION: a CP-Machine DELETE was REQUESTED (not denied, not completed — e.g. 409/in-flight)"; SAFETY=FAIL;;
      FAIL_NOT_EXERCISED)       fail "#5 INVALID: KCP never engaged the rollout/fallback for this CP Machine — a pass would be meaningless"; SAFETY=FAIL;;
      FAIL_INCOMPLETE)          fail "#5 INCOMPLETE EVIDENCE (audit or observer gap) — failing closed"; SAFETY=FAIL;;
      *)                        fail "#5 unknown verdict '$verdict'"; SAFETY=FAIL;;
    esac
    echo "    KCP conditions: $(jq -c '.status.conditions // []' "$KCPJ" 2>/dev/null)"
    echo "    NOTE: this evidences 'CAPI did not request a CP delete in THIS run'. It does NOT prove"
    echo "          'deletion cannot occur' — only an admission webhook (failurePolicy=Fail) prevents it."
    jq -n --arg s "$SAFETY" --arg v "$verdict" --arg au "$au" --arg po "$po" --arg fx "$fx" \
          --slurpfile run "$RUN_FILE" --arg wl "$wl_uid" --arg mu "$m_uid" '{
            scenario:"decline", safety:$s, verdict:$v, audit_evidence:$au, poll_corroboration:$po,
            fallback_exercised:$fx, run_id:$run[0].run_id, mgmt_uid:$run[0].mgmt_uid,
            capi_version:$run[0].capi_version, ext_image_id:$run[0].ext_image_id,
            extcfg_hash:$run[0].extcfg_hash, workload_uid:$wl, machine_uid:$mu }' > "$RESULT"
    ;;
  accept)
    kcp_v="$(jq -r '[.items[]|select(.kind=="KubeadmControlPlane")][0].spec.version' "$A")"
    mach_v="$(jq -r '[.items[]|select(.kind=="Machine")|.spec.version]|unique|join(",")' "$A")"
    [ "$kcp_v" = "$TARGET" ] && pass "#2 KCP.spec.version=$TARGET" || fail "#2 KCP version=$kcp_v"
    [ "$mach_v" = "$TARGET" ] && pass "#2 all Machine.spec.version=$TARGET (metadata convergence)" || fail "#2 Machine versions=$mach_v"
    upd="$(k get kubeadmcontrolplane/controlplane -n "$NS" -o jsonpath='{.status.updatedReplicas}' 2>/dev/null)"
    [ "$upd" = "1" ] && pass "#2 KCP updatedReplicas=1" || fail "#2 KCP updatedReplicas=$upd"
    # #3 hook invocation + retry from extension logs
    logs="$(k -n "$EXT_NS" logs "$(ext_deploy)" --tail=-1 2>/dev/null || true)"
    echo "$logs" | grep -q "CanUpdateMachine is called" && pass "#3 CanUpdateMachine invoked" || fail "#3 CanUpdateMachine not seen in logs"
    echo "$logs" | grep -q "UpdateMachine is called" && pass "#3 UpdateMachine invoked" || fail "#3 UpdateMachine not seen in logs"
    echo "$logs" | grep -q "retryAfterSeconds.*5" && pass "#3 UpdateMachine retry (RetryAfterSeconds>0) observed" || echo "WARN #3 retry not found in retained logs"
    printf '{"scenario":"accept","kcp_version":"%s","machine_versions":"%s","updatedReplicas":"%s"}\n' "$kcp_v" "$mach_v" "$upd" > "$RESULT"
    ;;
  update-failure|controller-restart)
    [ "$(names Machine "$B")" = "$(names Machine "$A")" ] && pass "#5 Machine retained through disruption (no replacement)" || fail "#5 Machine changed under disruption"
    echo "    After recovery, re-check convergence/resume with a follow-up snapshot if needed."
    printf '{"scenario":"%s","machines_before":"%s","machines_after":"%s"}\n' "$SCENARIO" "$(names Machine "$B")" "$(names Machine "$A")" > "$RESULT"
    ;;
esac

echo "=== $( [ $FAILED = 0 ] && echo "$SCENARIO: ALL ASSERTIONS PASSED" || echo "$SCENARIO: ASSERTIONS FAILED" ) ==="
echo "result: $RESULT"
exit $FAILED
