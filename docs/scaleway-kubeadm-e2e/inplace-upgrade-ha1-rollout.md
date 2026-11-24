# ha1 in-place upgrade: Scaleway integration and upgrade plan (v1.36.6 → v1.37.2)

> **Status: EXECUTED on 2026-11-24, result PASS** ([test protocol](inplace-upgrade-testprotocol.md)). The text
> below is the plan as it was written before the run (Gates B10.22/B10.23); what the run did differently is listed
> in [Deviations of the run](#deviations-of-the-run). Every numbered step was its own gate with explicit approval;
> stop on any failed check; no automatic retry, rollback or cleanup. Mechanism and recovery:
> [../inplace-upgrade.md](../inplace-upgrade.md). Tooling: `hack/inplace/`. Management context is always
> named explicitly (`kind-ok-lab-mgmt`); hosts only over the pinned SSH of ok-scaleway-lab.

Every `hack/inplace` script proves both cluster identities before its first request (`lib.sh`) and refuses
otherwise: `MGMT_KUBECONFIG` + `MGMT_CONTEXT` (the current-context is never used), `MGMT_SERVER` and
`MGMT_CA_SHA256` (the management API URL and the sha256 of the context's `certificate-authority-data`, as printed
by `remote.sh byoh-grants`), `NS`, `CLUSTER` and `CLUSTER_UID` (the UID of the workload Cluster). The workload
kubeconfig must point at the Cluster's `controlPlaneEndpoint` and trust its `<cluster>-ca` Secret.

Cluster `ha1` (namespace `ha1`): KCP 3 (cp-1..3, stacked etcd), MachineDeployment `ha1-md-0` 2 (w-1, w-2),
Cilium 1.20.5, CAPI v1.14.4, BYOH controller and agents `15b668d`.

## Phase A — artifacts (offline + publish approvals)
A1. Push the reviewed feature branches (approval); review and merge to `main` (approval). `publish-ghcr.yml` runs
    after CI on `main` and publishes `byoh-controller`, `no-replacement-guard` and `inplace-extension`
    (`:git-<sha12>`, linux/amd64+arm64) and reports their index digests.
A2. Lab locks (offline, per image): rebuild each binary from `git archive <commit>` in the locked
    `golang:1.26.10` image (GO_VERSION from `go.mod`, `-trimpath -buildvcs=false`, as the Dockerfiles), compare
    with the binary in the published linux/amd64 image (bit-identical → REPRODUCED, else stop), record index,
    amd64 and config digests and the base-image digests (`byoh-<commit>.lock` extended to the guard and the
    extension, `lib/byoh_bundle.py`). Agent: `lib/byoh_agent.py build` → `byoh-agent-<commit>.lock`.
    `tools.env`: `BYOH_LOCK`, `BYOH_AGENT_LOCK` (new), `BYOH_AGENT_FROM_LOCK=byoh-agent-15b668d.lock`, with sha256.
A3. Package lock re-verified against the signed index
    (`OKX_NETWORK_TESTS=1 go test ./agent/inplaceupgrade -run TestRealRepositoryIndex`).

Compatibility (checked in code): the new CRD only adds optional fields (`spec/status.inPlaceUpgrade`); the old
controller and old agents never set them. A new controller with old agents: unchanged behaviour (requests are only
written by the extension, which is deployed after all agents are new). New agents with the old CRD: the fields are
pruned, no request exists. The ByoHost webhook of the new controller validates host status writes as before plus
`status.inPlaceUpgrade`. Hence the order CRD → controller → agents → guard → extension.

## Phase B — management integration without a version change
Before B1: `byoh-grants ha1 verify` PASS and the grant watcher restarted with a fresh deadline (approval; the
B10.10a watcher ended at 21:24Z on 2026-11-23). Baseline before each step: `preflight.sh v1.37.2`, `progress.sh`,
`uids.py snapshot`, `negative-tests.sh --expect-unprotected`.
B1. **BYOH CRD + controller** (`kind-up.sh --byoh` with the new lock): CRD first, then the controller. Check: 5
    ByoHosts and 5 Machines with unchanged UIDs and attachments, controller 1/1, `byoh-grants ha1 verify` PASS,
    no CSR.
B2. **Agents**, one host at a time, workers first (w-2, w-1, cp-3, cp-2, cp-1):
    `remote.sh byoh-enroll ha1 <host> update-agent` (typed confirmation). Post-checks per host: running executable =
    locked agent, same flags, same `/root/.byoh/config`, same ByoHost UID and attachment, no new CSR, kubelet active;
    the Node stays Ready. Previous binary kept on the host.
B3. **KCP maxSurge 1 → 0** (resourceVersion-bound patch). No Machine change.
B4. **Drainer identity**: `drainer-credential.sh plan` → `apply 48h` → `verify` (can-i matrix PASS).
B5. **Guard and hardening**, in this order: namespace `okx-inplace-system`; the identities ConfigMap
    `okx-inplace-authorized-identities`; then the two admission policies with their bindings (they deny every
    matching request while the identities are missing); `render-registry.sh` → review → registry, guard
    Deployment, 6 webhooks (Fail). Then label `ha1` `safety.openkubes.org/in-place-only=true`.
    **`negative-tests.sh`: 16/16 denied by the guard**; S4/S5 checks as a non-listed identity (impersonation,
    dry-run): token minting for approver/initiator and registry writes denied. Guard outage drill: scale the guard
    to 0 (platform admin) → a dry-run Machine delete fails with `failed calling webhook` → scale back to 1.
B6. **Extension + gates**: extension Deployment with the package-lock ConfigMap; enable `RuntimeSDK=true` and
    `InPlaceUpdates=true` on the core and KCP controllers (args change, the controllers restart); ExtensionConfig
    `okx-inplace-extension` Discovered=True with 3 handlers.
B7. **Readiness check**: 5 Nodes Ready; `uids.py compare` PASS; all Machines UpToDate and no new MachineSet (no
    rollout); `negative-tests.sh` 16/16; `byoh-grants ha1 verify` PASS; etcd 3/3, HAProxy 3 active.
    **Stop and report readiness.** The Kubernetes version is unchanged.

### Recovery of a failed component rollout (never touches Machines, ByoHosts or VMs)
- B1 controller: the CRD change is additive; re-deploy the previous controller lock (`byoh-15b668d.lock`) with
  `kind-up.sh --byoh`; the CRD stays (unused fields). Machines are unaffected while the controller is down.
- B2 agent: the agent restarts without touching the kubelet. On a failed post-check stop; forward fix, or put the
  kept previous binary back (`byoh-hostagent.prev-<sha16>`) with the same compare-and-swap procedure as a separate
  approved step; never re-enroll.
- B3: patch maxSurge back to 1 only if no in-place work is planned (separate approval).
- B4: `drainer-credential.sh revoke`.
- B5 guard: a crashing guard with `failurePolicy: Fail` blocks lifecycle writes in `ha1` only (that is the
  protection). Break-glass by a platform admin: delete the ValidatingWebhookConfiguration
  `okx-no-replacement-guard` (audited), fix, re-apply. The admission policies: a platform admin fixes the
  identities ConfigMap; if it is lost, delete the two bindings (admission objects), restore it, re-apply.
- B6: remove the ExtensionConfig and set the gates back to false (controllers restart); no Machine is in an
  in-place update before the version patch.
## Phase C — prerequisites of the version change
C1. **Cilium** 1.21, own gate (`ok-scaleway-lab docs/cilium-upgrade.md`); `preflight.sh` passes the Cilium check.
    A stable release passes when `hack/inplace/cilium-compat.json` lists the target minor. A pre-release passes
    only when it is pinned there (agent and operator image digests, reviewed and committed) **and** the operator
    approves the run with `OKX_CILIUM_PRERELEASE=<version>`; any other running image fails the check.
C2. **etcd snapshot** (`etcd-snapshot.sh save`, off-host) and **restore verification** (`etcd-snapshot.sh verify`
    with `registry.k8s.io/etcd:3.6.8-0`, throw-away directory); sha256 in the evidence.
C3. `preflight.sh v1.37.2` READY.

## Phase D — upgrade, one host at a time (separate approval per host)
**Order.** Cluster API, not the operator, chooses the next Machine (CAPI v1.14.4): the KubeadmControlPlane takes the
**oldest** outdated Machine (`selectMachineForInPlaceUpdateOrScaleDown` → `MachineInFailureDomainWithMostMachines` →
`Oldest()`, ties by name; no failure domains in ha1), the MachineSet moves Machines by its deletion order (default
`Random` = equal priority for healthy Machines, ties by name). For ha1 (Machine creation times, read 2026-11-24):

| # | Host | Machine | Created | Role |
|---|---|---|---|---|
| 1 | ha1-cp-1 | ha1-control-plane-nt28b | 17:27:56Z | ControlPlaneFirst (`kubeadm upgrade apply`) |
| 2 | **ha1-cp-3** | ha1-control-plane-77sx8 | 18:38:51Z | ControlPlane (`kubeadm upgrade node`) |
| 3 | **ha1-cp-2** | ha1-control-plane-58967 | 18:41:43Z | ControlPlane |
| 4 | ha1-w-1 | ha1-md-0-frlf2-4725m | 18:58:52Z | Worker (name sorts first) |
| 5 | ha1-w-2 | ha1-md-0-frlf2-5dqc9 | 18:58:52Z | Worker |

The control planes therefore go cp-1 → **cp-3 → cp-2**. The order cp-1 → cp-2 → cp-3 cannot be imposed safely: the
only lever is the `cluster.x-k8s.io/delete-machine` annotation, which marks a Machine for deletion and would delete it
on a fallback — forbidden. An unhealthy Machine is preferred by both selections; `host-step.sh next` then reports a
STOP (the sequence no longer matches) instead of approving another host.

**Tools** (`hack/inplace`, all with the identity checks of `lib.sh`): `host-step.sh members|next|verify|approve`
(the per-host gate; `approve` is the only mutation — rv-bound annotation after a typed confirmation),
`render_grant.py` (prints a grant, never applies it), `netcheck.sh` with `netcheck/`, `progress.sh`, `monitor.sh`,
`uids.py`, `preflight.sh`, `etcd-snapshot.sh`. Environment for the whole phase:
`SEQUENCE=ha1-cp-1,ha1-cp-3,ha1-cp-2,ha1-w-1,ha1-w-2`, `UIDS_BASELINE=<D0 snapshot>`, `ETCD_MEMBERS=<D0 members>`,
`GW_SSH`, `OKX_CILIUM_PRERELEASE` if a pre-release Cilium runs.

D0. **Start of the window** (read-only + workload test pods; approval): `preflight.sh v1.37.2` READY; `uids.py snapshot`
    → `UIDS_BASELINE`; `host-step.sh members` → `ETCD_MEMBERS`; `host-step.sh next v1.37.2` = HOLD (no Machine
    updating); netcheck workload applied, `netcheck.sh open` and (policies applied) `netcheck.sh policy` PASS;
    `monitor.sh` running in its own terminal for the whole phase.
D1. **KCP grant and version patch** (approver, initiator; approval): `render_grant.py --kind KubeadmControlPlane …
    --not-after <end of window>` with the request and approval records of the gate → review → approver creates it;
    initiator adds the single claim; patch KCP `spec.version: v1.37.2` (rv-bound). One grant for the KCP **without**
    `machineUID` (requires the guard fix that accepts it; a Machine-bound KCP grant would be ambiguous for the second
    control plane).
D2–D4. **Per control plane, in SEQUENCE order** (cp-1, cp-3, cp-2), each its own approval:
    1. `host-step.sh next v1.37.2` must print `APPROVE: <host>` — exactly one Machine updating, it is the expected
       host, it waits for its approval, every earlier host is done, no other open approval. HOLD → wait; STOP → stop.
    2. `host-step.sh approve v1.37.2 <host>` (re-runs the gate and the health gate, then asks for
       `APPROVE <host> v1.37.2`). The extension drains the Node and requests the upgrade; the agent runs it.
    3. Watch `progress.sh` / `monitor.sh` (API `/readyz` continuous; etcd quorum never below 2 of 3).
    4. `host-step.sh verify v1.37.2 <host>` PASS: agent Succeeded, Node Ready at v1.37.2, Machine no longer updating,
       static pods at v1.37.2, Nodes all Ready, etcd 3/3 with the **same member IDs**, Cilium and CoreDNS ready,
       HAProxy 3 active, no Machine/ByoMachine/ByoHost/Node added, removed or replaced; then `netcheck.sh open`
       (and `policy` while the policies are applied) PASS.
    Only then the next host. The first host also proves `kubeadm upgrade apply` (cluster-wide manifests: kube-proxy,
    CoreDNS) — check their images after D2.
D5. **MD grant and template version** (approval): `render_grant.py --kind MachineDeployment …` → approver, initiator
    claim; patch the MD template `spec.template.spec.version: v1.37.2` (rv-bound). The extension answers "waiting for
    the control plane" until all three control planes report v1.37.2 — `next` shows HOLD until then.
D6–D7. **Per worker** (w-1, then w-2): the same four steps as D2–D4 (no static pods; workloads must reschedule after
    the drain: PodDisruptionBudgets respected).
D8. **Final**: `uids.py compare <baseline> <now> --expect-version v1.37.2` PASS; KCP and MD UpToDate, no new
    MachineSet left over; kube-proxy and CoreDNS images; Cilium 5/5; `byoh-grants verify`; `negative-tests.sh` 16/16;
    netcheck workload removed (approved); evidence sealed; fill the test protocol.

GO for each host only on `APPROVE: <host>` from `next` and a passed `verify` of the previous host. NO-GO / STOP:
`next` says STOP; any `verify` FAIL; a Machine, ByoMachine, ByoHost or Node created, deleted or replaced; an etcd
member ID changed or fewer than 2 healthy members; agent `Failed`; a drain that does not finish (PDB) — never force
it; the API endpoint unavailable beyond the single-host restart. On a failed host: no further approval; forward fix on
the same host per the runbook (`../inplace-upgrade.md`); never `kubeadm reset`, never Machine replacement.

Never: delete or replace a Machine, ByoMachine, ByoHost or VM; set `cluster.x-k8s.io/delete-machine`; remove CAPI
in-place annotations or hooks by hand; pre-approve a host that is not waiting; continue after a failed check without
a new approval.

## Deviations of the run
- **B2 order:** the agents were updated w-1, w-2, cp-3, cp-2, cp-1 (canary on a worker), and a second time during
  Phase D to the fixed agent `cdf534b` (cp-1 in the failed state, then cp-3, cp-2, w-1, w-2).
- **B4** (drainer identity) was created right before the version patch, because its token lives 48 h; revoked after
  the run. Its `verify` reported 9 of 10 (a defect of the check, fixed in #39).
- **B5:** `negative-tests.sh` reached 14 of 16 with the script of the run (two stale cases, fixed in #39: 16 of 16).
  The guard outage drill (scale to 0) was not run.
- **B6:** `RuntimeSDK` is only a gate of the core controller; the KCP controller has `InPlaceUpdates` alone.
- **C1:** Cilium 1.21.0-pre.4, a pre-release, accepted for this lab only.
- **D0:** the grant watcher of the host RBAC and `monitor.sh` ran for the whole phase (two windows of 4 h).
- **D1:** the first UpdateMachine calls failed (extension defect, #34); extension redeployed before any host.
- **D2:** `ha1-cp-1` needed attempt 1 after the agent fix (#35). The host approval is typed by the operator in a
  terminal; it cannot be supplied by automation.
- **D5:** the extension declined the MachineSet (#36); Cluster API tried to replace a worker until the fix and a
  restart of `capi-controller-manager`; then the guard denied the ByoMachine label re-alignment (#37).
- **D8:** the netcheck workload was removed, the drainer revoked. Not done: KCP `maxSurge` back to 1 (the guard
  denies raising it in a protected namespace), decommission, the guard outage drill.
- **Sequence** was exactly the planned one: cp-1, cp-3, cp-2, w-1, w-2.

## Unregistering and VM destruction
With the guard active, Machine/KCP/MD/namespace deletion in `ha1` is denied. Before the B10.20 teardown:
decommission (runbook §Decommission: registry entry removed and guard rolled, then the label, then a dry-run delete
admitted), revoke the drainer (`drainer-credential.sh revoke`), then the teardown, then Stack B. Disable the
ExtensionConfig, the guard and the gates only after `ha1` is gone. Host cleanup gaps stay deferred (B10.20a).
