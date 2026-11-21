#!/usr/bin/env bash
# One scenario from a FRESH, ownership-stamped workload Cluster. Decline is the safety gate and
# is bound to the current run (provenance). Deletes are ownership-verified and fail closed.
#   ./scenario.sh decline | accept | update-failure | controller-restart
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_bootstrapped
load_run; export RUN_ID
assert_extension_ready test-extension

SCENARIO="${1:?scenario: decline|accept|update-failure|controller-restart}"
NS="default"; WORKLOAD="ipw-$SCENARIO"; export WORKLOAD

# Requirement P1#2 + #6: non-decline scenarios require a decline approval bound to THIS run.
if [ "$SCENARIO" != "decline" ]; then
  bash "$SPIKE_HOME/classify.sh" provenance "$STATE_DIR/result-decline.json" "$RUN_FILE" \
    || die "no valid decline approval for the current run (run_id=$RUN_ID); run './scenario.sh decline' first"
fi

ext_set(){ k patch extensionconfig test-extension --type merge -p "{\"spec\":{\"settings\":{\"disableInPlaceUpdates\":\"$1\"}}}"; assert_extension_ready test-extension; }

delete_owned_workload() { # ownership-verified, fail-closed (no '|| true')
  if k get cluster "$WORKLOAD" -n "$NS" >/dev/null 2>&1; then
    assert_owned_cluster "$WORKLOAD" "$NS"
    k delete cluster "$WORKLOAD" -n "$NS" --wait --timeout=300s
    ! k get cluster "$WORKLOAD" -n "$NS" >/dev/null 2>&1 || die "incomplete cleanup: $WORKLOAD still present"
  fi
}

fresh_workload() {
  delete_owned_workload
  # Render the pinned classic CAPD template locally (no GitHub release download).
  local template="$SPIKE_HOME/capd-classic-template.yaml"
  [ -f "$template" ] || die "classic CAPD template missing"

  python3 - "$template" "$STATE_DIR/$WORKLOAD.yaml" "$WORKLOAD" "$BASE_VERSION" <<'PYRENDER'
from pathlib import Path
import sys

src, dst, workload, version = sys.argv[1:]
s = Path(src).read_text()

assert s.count("ipw-decline") >= 1
assert s.count("v1.33.0") == 2
assert version == "v1.33.0"

s = s.replace("ipw-decline", workload)
s = s.replace("v1.33.0", version)

Path(dst).write_text(s)
PYRENDER
  yq -i '(select(.kind=="KubeadmControlPlane").spec.rollout.strategy.rollingUpdate.maxSurge)=0' "$STATE_DIR/$WORKLOAD.yaml"
  yq -i '(select(.kind=="MachineDeployment").spec.strategy.type)="OnDelete"' "$STATE_DIR/$WORKLOAD.yaml"
  yq -i '(select(.kind=="Cluster").metadata.labels["ok-byoh-spike/run-id"])=strenv(RUN_ID)' "$STATE_DIR/$WORKLOAD.yaml"
  k apply -f "$STATE_DIR/$WORKLOAD.yaml"
  k wait cluster "$WORKLOAD" -n "$NS" --for=condition=ControlPlaneInitialized --timeout=900s || die "$WORKLOAD did not initialize"
  k wait kubeadmcontrolplane/controlplane -n "$NS" \
    --for=jsonpath='{.status.readyReplicas}'=1 --timeout=600s || die "$WORKLOAD KCP not 1/1"
}

bump_version() {
  yq -i "(select(.kind==\"KubeadmControlPlane\").spec.version)=\"$TARGET_VERSION\"" "$STATE_DIR/$WORKLOAD.yaml"
  yq -i "(select(.kind==\"MachineDeployment\").spec.template.spec.version)=\"$TARGET_VERSION\"" "$STATE_DIR/$WORKLOAD.yaml"
  k apply -f "$STATE_DIR/$WORKLOAD.yaml"
}

capture_cp_identity() { # names (comma-sep) + first CP Machine UID, captured before the bump
  k get machines -n "$NS" -l "cluster.x-k8s.io/control-plane,cluster.x-k8s.io/cluster-name=$WORKLOAD" \
    -o jsonpath='{range .items[*]}{.metadata.name}{","}{end}' > "$STATE_DIR/cpnames-$SCENARIO.txt"
  k get machines -n "$NS" -l "cluster.x-k8s.io/control-plane,cluster.x-k8s.io/cluster-name=$WORKLOAD" \
    -o jsonpath='{.items[0].metadata.uid}' > "$STATE_DIR/machineuid-$SCENARIO.txt"
}
capture_decline_inputs() {
  k get kubeadmcontrolplane/controlplane -n "$NS" -o json > "$STATE_DIR/kcp-$SCENARIO.json"
  local dep; dep="$(ext_deploy)"; [ -n "$dep" ] && k -n "$EXT_NS" logs "$dep" --tail=-1 > "$STATE_DIR/extlog-$SCENARIO.txt" 2>/dev/null || true
  collect_audit "$STATE_DIR/audit-$SCENARIO.log"   # authoritative DELETE-request record
}

# ---- Step-0 guard augmentation (opt-in: GUARD=1; reuses the native decline flow verbatim) ----
GUARD="${GUARD:-0}"
GDIR="${GDIR:-$SPIKE_HOME/../byoh-decline-guard-e2e/guard-isolated}"
GNS="byoh-decline-guard-system"
GUARD_IMAGE="${GUARD_IMAGE:-localhost/ok-byoh-no-replacement-guard:cc4715f-arm64}"
WEBHOOK="byoh-decline-no-replacement-guard"
REG_MOUNT="/etc/openkubes/no-replacement"                       # the guard binary's HARDCODED RegistryPath
GUARD_SA="system:serviceaccount:$GNS:no-replacement-guard"
GENUINE_REASON="Machine DELETE is forbidden during in-place-only BYOH lifecycle"
KCP_NS="capi-kubeadm-control-plane-system"
AUDIT_CUTOFF=""

cleanup_guard() { # idempotent; MUST precede any ownership-verified workload teardown (failurePolicy:Fail)
  k delete validatingwebhookconfiguration "$WEBHOOK" --ignore-not-found >/dev/null 2>&1 || true
  k -n "$GNS" delete deploy/no-replacement-guard --ignore-not-found >/dev/null 2>&1 || true
  k -n "$GNS" delete configmap no-replacement-registry --ignore-not-found >/dev/null 2>&1 || true
  sed "s/byoh-decline-e2e/$NS/g" "$GDIR/guard-infrastructure.yaml" 2>/dev/null \
    | k delete -f - --ignore-not-found >/dev/null 2>&1 || true
}

verify_guard_gone() { # point 2: three-way — present=STOP, confirmed-NotFound=PASS, API/RBAC/net error=STOP
  local out rc err="$STATE_DIR/vgg-err-$SCENARIO.txt"
  # --ignore-not-found: exit 0 + empty output ONLY on a confirmed NotFound; any other API error exits non-zero.
  if out="$(k get validatingwebhookconfiguration "$WEBHOOK" --ignore-not-found -o name 2>"$err")"; then rc=0; else rc=$?; fi
  if [ "$rc" -ne 0 ]; then
    die "cannot confirm guard webhook $WEBHOOK removal: API/RBAC/network error (rc=$rc): $(cat "$err" 2>/dev/null). \
Refusing ownership-verified workload teardown. RECOVERY: restore API access, then \
kubectl --kubeconfig $KUBECONFIG delete validatingwebhookconfiguration $WEBHOOK ; \
kubectl --kubeconfig $KUBECONFIG -n $GNS delete deploy/no-replacement-guard configmap/no-replacement-registry ; then ./teardown.sh"
  fi
  [ -z "$out" ] || die "guard webhook $WEBHOOK STILL PRESENT after cleanup: refusing workload teardown. RECOVERY: \
kubectl --kubeconfig $KUBECONFIG delete validatingwebhookconfiguration $WEBHOOK ; \
kubectl --kubeconfig $KUBECONFIG -n $GNS delete deploy/no-replacement-guard configmap/no-replacement-registry ; then re-run ./teardown.sh"
  # rc==0 AND empty output => API explicitly confirmed NotFound => safe to free the teardown
}

deploy_guard() { # deploy + VERIFY the guard bound to the LIVE workload; every precondition fails closed
  trap 'cleanup_guard || true' EXIT                              # remove guard on ANY failure during deploy
  need kind; need jq
  kind load docker-image "$GUARD_IMAGE" --name "$MGMT_CLUSTER" \
    || die "guard image $GUARD_IMAGE not in local docker (build PR#23 guard first)"
  local uid; uid="$(k get cluster "$WORKLOAD" -n "$NS" -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
  [ -n "$uid" ] || die "no live clusterUID for $WORKLOAD"
  # (+) RBAC retarget ns byoh-decline-e2e -> $NS so the guard can genuinely READ the workload Cluster
  sed "s/byoh-decline-e2e/$NS/g" "$GDIR/guard-infrastructure.yaml" | k apply -f -
  k -n "$GNS" wait --for=condition=Ready certificate/no-replacement-serving-cert --timeout=120s \
    || die "guard serving cert not Ready"
  # registry.json: EXACTLY {namespace,clusterName,clusterUID} (no 'protected' field: DisallowUnknownFields)
  k create configmap no-replacement-registry -n "$GNS" --dry-run=client -o yaml \
     --from-literal=registry.json="$(jq -n --arg ns "$NS" --arg cn "$WORKLOAD" --arg uid "$uid" \
        '{registrations:[{namespace:$ns,clusterName:$cn,clusterUID:$uid}]}')" | k apply -f -
  # (!) the binary reads the HARDCODED RegistryPath, NOT the deployment env: mount the CM at $REG_MOUNT
  sed -e "s#mountPath: /registry#mountPath: $REG_MOUNT#" \
      -e "s#/registry/registry.json#$REG_MOUNT/registry.json#" "$GDIR/guard-deployment.yaml" | k apply -f -
  k -n "$GNS" rollout status deploy/no-replacement-guard --timeout=120s \
    || die "guard not Ready (crashloop? wrong registry mount path = degenerate fail-closed)"
  if k -n "$GNS" logs deploy/no-replacement-guard --tail=-1 2>/dev/null \
       | grep -qiE 'configure guard|read No-Replacement registry|must be explicitly true|handler is unavailable'; then
    die "guard logged a STARTUP ERROR (registry/env misconfig) = degenerate fail-closed, not a real policy"
  fi
  # PRECONDITION A (rules out (b) missing-RBAC): the guard SA can actually read clusters in $NS
  local can; can="$(k auth can-i get clusters.cluster.x-k8s.io -n "$NS" --as="$GUARD_SA" 2>/dev/null || true)"
  echo "$can" > "$STATE_DIR/guard-rbac-$SCENARIO.txt"
  [ "$can" = yes ] || die "guard SA cannot read clusters in $NS (RBAC) = FALSE fail-closed denial"
  # PRECONDITION B (rules out (c) no-entry / (d) wrong-UID): registry UID == live cluster UID
  local reg_uid; reg_uid="$(k -n "$GNS" get configmap no-replacement-registry \
     -o jsonpath='{.data.registry\.json}' | jq -r '.registrations[0].clusterUID')"
  echo "$reg_uid" > "$STATE_DIR/guard-reguid-$SCENARIO.txt"
  [ "$reg_uid" = "$uid" ] || die "registry clusterUID ($reg_uid) != live clusterUID ($uid)"
  # derive the EXPECTED KCP-controller ServiceAccount from the ACTUAL deployment (point 1)
  local kcp_sa; kcp_sa="$(k -n "$KCP_NS" get deploy \
     -o jsonpath='{.items[0].spec.template.spec.serviceAccountName}' 2>/dev/null || true)"
  [ -n "$kcp_sa" ] || die "could not resolve KCP controller ServiceAccount from $KCP_NS"
  echo "system:serviceaccount:$KCP_NS:$kcp_sa" > "$STATE_DIR/kcp-sa-$SCENARIO.txt"
  # webhook scoped to THIS workload ns (matchPolicy already Equivalent)
  sed "s/request.namespace == 'byoh-decline-e2e'/request.namespace == '$NS'/" \
     "$GDIR/guard-validating-webhook.yaml" | k apply -f -
  local i                                                        # wait for cert-manager CA injection
  for i in $(seq 1 30); do
    if [ -n "$(k get validatingwebhookconfiguration "$WEBHOOK" \
          -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null)" ]; then break; fi
    if [ "$i" = 30 ]; then die "guard caBundle not injected"; fi
    sleep 2
  done
  # POSITIVE CONTROL (readiness): protected-CP DELETE must be DENIED now (server dry-run, no mutation),
  # as a GENUINE policy match (preconditions above already proved read+registry+UID).
  local cp out; cp="$(head -1 "$STATE_DIR/cpnames-$SCENARIO.txt" | cut -d, -f1)"
  [ -n "$cp" ] || die "no CP machine name captured"
  if out="$(k delete machine "$cp" -n "$NS" --dry-run=server 2>&1)"; then
    die "guard did NOT deny a protected CP delete (dry-run succeeded) - abort before bump"
  fi
  printf '%s\n' "$out" | grep -qF "$GENUINE_REASON" \
    || die "dry-run denial is not the GENUINE policy reason (got: $out) - abort"
  # AUDIT CUTOFF (point 2): everything so far (incl. the dry-run 403) is PRE-bump; the fallback
  # evidence window is strictly AFTER this, derived from the apiserver's OWN timestamps.
  collect_audit "$STATE_DIR/audit-preguard-$SCENARIO.log"
  AUDIT_CUTOFF="$(jq -r -s 'map(.requestReceivedTimestamp // empty)|max // ""' \
                   "$STATE_DIR/audit-preguard-$SCENARIO.log" 2>/dev/null || true)"
  [ -n "$AUDIT_CUTOFF" ] || die "could not establish audit cutoff"
  log "guard: ready+verified; audit cutoff=$AUDIT_CUTOFF; expected KCP SA=$(cat "$STATE_DIR/kcp-sa-$SCENARIO.txt")"
}

verify_denial() { # points 1+2+3: window to post-bump, then VERIFY all discriminators on the SAME audit event
  local full="$STATE_DIR/audit-$SCENARIO.full.log" live="$STATE_DIR/audit-$SCENARIO.log"
  cp "$live" "$full"
  jq -c -s --arg c "$AUDIT_CUTOFF" '.[]|select((.requestReceivedTimestamp//"")>$c)' "$full" >"$live" 2>/dev/null || : >"$live"
  local cp expect_user genuine succ rbac reguid
  cp="$(head -1 "$STATE_DIR/cpnames-$SCENARIO.txt" | cut -d, -f1)"
  expect_user="$(cat "$STATE_DIR/kcp-sa-$SCENARIO.txt" 2>/dev/null || true)"
  rbac="$(cat "$STATE_DIR/guard-rbac-$SCENARIO.txt" 2>/dev/null || true)"
  reguid="$(cat "$STATE_DIR/guard-reguid-$SCENARIO.txt" 2>/dev/null || true)"
  # ALL discriminators must hold on the SAME audit event (single-event conjunction; code==403 explicit):
  genuine="$(jq -s --arg cp "$cp" --arg ns "$NS" --arg u "$expect_user" --arg g "$GENUINE_REASON" '
      [ .[] | select(
          (.verb//"")               == "delete"
          and (.objectRef.resource//"")   == "machines"
          and (.objectRef.name//"")       == $cp
          and (.objectRef.namespace//"")  == $ns
          and (.stage//"")                == "ResponseComplete"
          and (.user.username//"")        == $u
          and (.responseStatus.code//0)   == 403
          and ( ((.responseStatus.message//"")|contains($g))
                or ((.responseObject.message//"")|contains($g)) )
        ) ] | length' "$live" 2>/dev/null || echo 0)"
  # Explicitly exclude any successful (2xx) DELETE on the SAME CP machine:
  succ="$(jq -s --arg cp "$cp" --arg ns "$NS" '
      [ .[] | select(
          (.verb//"")               == "delete"
          and (.objectRef.resource//"")   == "machines"
          and (.objectRef.name//"")       == $cp
          and (.objectRef.namespace//"")  == $ns
          and (.stage//"")                == "ResponseComplete"
          and (.responseStatus.code//0)   >= 200
          and (.responseStatus.code//0)   <  300
        ) ] | length' "$live" 2>/dev/null || echo 0)"
  log "verify: genuine-denial-events=$genuine successful-delete-events=$succ (user=$expect_user rbac=$rbac reguid=$reguid)"
  [ "$succ" = 0 ]     || die "FAIL_DESTRUCTIVE: a CP DELETE SUCCEEDED (2xx) in-window on $cp"
  [ "$genuine" != 0 ] || die "FAIL: no SINGLE audit event proves {name=$cp ns=$NS user=$expect_user verb=delete ResponseComplete code=403 genuine-reason}; rbac=$rbac reguid=$reguid - not a valid PASS_DENIED"
}

case "$SCENARIO" in
  decline)
    ext_set true
    fresh_workload
    capture_cp_identity
    snapshot "before-$SCENARIO" "$NS"
    if [ "$GUARD" = 1 ]; then deploy_guard; fi
    OBS="$STATE_DIR/observer-$SCENARIO.ndjson"
    start_cp_observer "$NS" "$WORKLOAD" "$OBS"; OPID="$OBSERVER_PID"   # direct child; PID via global
    [ -n "$OPID" ] || die "failed to start observer"
    if [ "$GUARD" = 1 ]; then
      trap 'cleanup_guard || true; kill "$OPID" 2>/dev/null || true' EXIT
    else
      trap 'kill "$OPID" 2>/dev/null || true' EXIT   # abort => NO sentinel => INCOMPLETE
    fi
    bump_version
    log "DECLINE: extension returns no patches; CP must NOT be deleted. Observing continuously..."
    sleep 120
    capture_decline_inputs
    if [ "$GUARD" = 1 ]; then verify_denial; fi
    snapshot "after-$SCENARIO" "$NS"
    stop_cp_observer "$OPID" "$OBS"; trap - EXIT   # normal stop writes the sentinel
    if [ "$GUARD" = 1 ]; then cleanup_guard; verify_guard_gone; fi
    ext_set false
    ;;
  accept)
    ext_set false; fresh_workload; snapshot "before-$SCENARIO" "$NS"; bump_version
    k wait machine -n "$NS" -l "cluster.x-k8s.io/cluster-name=$WORKLOAD" --for=jsonpath='{.spec.version}'="$TARGET_VERSION" --timeout=600s || true
    sleep 30; capture_decline_inputs; snapshot "after-$SCENARIO" "$NS"
    ;;
  update-failure)
    ext_set false; fresh_workload; snapshot "before-$SCENARIO" "$NS"; bump_version; sleep 20
    log "UPDATE-FAILURE: making extension UNAVAILABLE mid-update."
    k -n "$EXT_NS" scale deploy -l app=test-extension-manager --replicas=0
    sleep 60; snapshot "after-$SCENARIO" "$NS"
    k -n "$EXT_NS" scale deploy -l app=test-extension-manager --replicas=1
    _d="$(ext_deploy)"; [ -n "$_d" ] && k -n "$EXT_NS" rollout status "$_d" --timeout=120s || true
    ;;
  controller-restart)
    ext_set false; fresh_workload; snapshot "before-$SCENARIO" "$NS"; bump_version; sleep 20
    log "CONTROLLER-RESTART: deleting KCP controller pod mid-update; expect idempotent resume."
    k -n capi-kubeadm-control-plane-system delete pod -l cluster.x-k8s.io/provider=control-plane-kubeadm --wait=false
    sleep 90; snapshot "after-$SCENARIO" "$NS"
    ;;
  *) die "unknown scenario $SCENARIO";;
esac

"$SPIKE_HOME/assert.sh" "$SCENARIO" "$TARGET_VERSION" "$NS" | tee "$STATE_DIR/report-$SCENARIO.txt"
AST=${PIPESTATUS[0]}
delete_owned_workload
log "scenario $SCENARIO complete (assert exit=$AST); report: $STATE_DIR/report-$SCENARIO.txt"
exit "$AST"
