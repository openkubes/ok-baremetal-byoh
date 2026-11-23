# ha1 in-place upgrade: Scaleway integration and upgrade plan (v1.36.6 → v1.37.2)

> **Status: PLAN.** Prepared in Gates B10.22/B10.23, nothing executed. Every numbered step is its own gate with
> explicit approval; stop on any failed check; no automatic retry, rollback or cleanup. Mechanism and recovery:
> [inplace-upgrade-runbook.md](inplace-upgrade-runbook.md). Tooling: `hack/inplace/`. Management context is always
> named explicitly (`kind-ok-lab-mgmt`); hosts only over the pinned SSH of ok-scaleway-lab.

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
C1. **Cilium** 1.21.x stable, own gate (`ok-scaleway-lab docs/cilium-upgrade.md`); `preflight.sh` passes the
    Cilium check.
C2. **etcd snapshot** (`etcd-snapshot.sh save`, off-host) and **restore verification** (`etcd-snapshot.sh verify`
    with `registry.k8s.io/etcd:3.6.8-0`, throw-away directory); sha256 in the evidence.
C3. `preflight.sh v1.37.2` READY.

## Phase D — upgrade (separate approval per step)
Hold points: the extension requests the upgrade of a host only when an administrator has set the annotation
`inplace.openkubes.org/approved-upgrade: v1.37.2` on its ByoHost (agents cannot set it). Without it the Machine's
UpdateMachine hook waits ("waiting for the approval"), so no host starts or follows another without a decision.
D1. **Control-plane canary.** Approver creates the KCP grant (KCP UID, v1.36.6 → v1.37.2, KCP transitions),
    initiator claims it. `monitor.sh` running. Patch KCP `spec.version` (rv-bound). KCP (maxSurge 0) selects one
    Machine and calls UpdateMachine; `progress.sh` shows which ByoHost waits for approval. Annotate **that one
    ByoHost** only; the extension drains it and requests `ControlPlaneFirst` (`kubeadm upgrade apply`). Verify:
    agent `Succeeded`, Node v1.37.2 Ready, static pods v1.37.2, etcd 3/3 healthy with the same member IDs,
    `/readyz` continuous, HAProxy 3 active, `uids.py compare` (no change).
D2. Second and third control plane: annotate the next waiting ByoHost only after the previous one is verified; the
    same checks after each (etcd quorum never below 2 healthy members).
D3. **Workers**: MD grant (MD UID, MD transitions), claim; patch the MD template version. The extension waits until
    all three control planes report v1.37.2; annotate w-1, verify, then w-2.
D4. **Final**: `uids.py compare before after --expect-version v1.37.2` PASS; KCP/MD UpToDate; kube-proxy and CoreDNS
    images; Cilium 5/5; grants verify; `negative-tests.sh` 16/16; fill the test protocol.

Never: delete or replace a Machine, ByoMachine, ByoHost or VM; remove CAPI in-place annotations or hooks by hand;
`kubeadm reset`; continue after a failed check without a new approval.

## Unregistering and VM destruction
With the guard active, Machine/KCP/MD/namespace deletion in `ha1` is denied. Before the B10.20 teardown:
decommission (runbook §Decommission: registry entry removed and guard rolled, then the label, then a dry-run delete
admitted), revoke the drainer (`drainer-credential.sh revoke`), then the teardown, then Stack B. Disable the
ExtensionConfig, the guard and the gates only after `ha1` is gone. Host cleanup gaps stay deferred (B10.20a).
