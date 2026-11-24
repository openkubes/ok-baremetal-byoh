# BYOH in-place upgrade runbook

> **Maturity: EXECUTED ONCE on an E2E lab, NOT production-ready.** On 2026-11-24 Cluster API v1.14.4 drove the
> in-place upgrade v1.36.6 -> v1.37.2 of a five-host BYOH cluster (three stacked-etcd control planes, two workers)
> with every Machine, ByoMachine, ByoHost and Node identity preserved; result and limits are in the lab's
> [test protocol](scaleway-kubeadm-e2e/inplace-upgrade-testprotocol.md). <!-- provider-neutrality: allow (E2E lab record) -->
> Four product defects were found and fixed during that run; the security review of the guard is still NO-GO for
> production (deferred hardening). Read [Lessons of the first run](#lessons-of-the-first-run) before the next one.

Read together with the testing/evidence rules in
[development/byoh/testing-and-evidence.md](development/byoh/testing-and-evidence.md) and the
No-Replacement contract [adr/ADR-001-byoh-no-replacement-safety.md](adr/ADR-001-byoh-no-replacement-safety.md).

## History: manual upgrade (2026-11-19)
Two E2E lab hosts were upgraded by hand from **v1.36.6 → v1.37.2** with **CAPI paused**
(`kubeadm upgrade apply` / `kubeadm upgrade node`, apt `v1.36 → v1.37`, kubelet/kubectl), CAPI
metadata left at v1.36.6 and no etcd backup ([lab record](scaleway-kubeadm-e2e/upgrade-1.37.md)). The executor <!-- provider-neutrality: allow (E2E lab record) -->
below performs the same host steps under CAPI control.

## Mechanism
1. **Intent.** The initiator changes `KubeadmControlPlane.spec.version` (then the MachineDeployment
   template version). The guard's intent webhook allows it only under an upgrade grant bound to
   the KCP/MD UID and the exact `fromVersion → toVersion`; every other spec change, any replica
   change (also through `/scale`), a surge above 0, CREATE and DELETE are denied.
2. **CanUpdateMachine / CanUpdateMachineSet** (`internal/inplace/extension`). The extension takes over
   a version-only change for a ByoMachine when the transition is forward, ≤ 1 minor, and the target
   is pinned in the package lock (`config/inplace/base/packages-v1.37.2.json`). Otherwise it
   declines, and the CAPI fallback (Machine delete/create) is denied by the guard: the rollout stops
   visibly instead of replacing a Machine.
3. **UpdateMachine** (stateless, repeatable). For the Machine it reads the live Machine (UID must
   equal the hook's), its Cluster, the ByoMachine controlled by that Machine UID and the single
   ByoHost attached to that ByoMachine UID. It refuses a second upgrade while another host of the
   cluster runs one, makes a worker wait until every control-plane Machine reports the target
   (kubelet ≤ API server), waits for the per-host approval annotation
   `inplace.openkubes.org/approved-upgrade: <target>` on the ByoHost (set by an administrator; the hold point
   between hosts), drains the Node (evictions honour PodDisruptionBudgets; DaemonSet and
   static pods stay; a pod without a controller stops the drain), then writes
   `ByoHost.spec.inPlaceUpgrade` with an ID derived from Machine UID + target. Role:
   `ControlPlaneFirst` (`kubeadm upgrade apply`) for the first control plane, `ControlPlane` and
   `Worker` (`kubeadm upgrade node`) for the others.
4. **Host executor** (`agent/inplaceupgrade`, run by the agent). The agent first records
   `status.inPlaceUpgrade.phase=Running`, then runs, with a checkpoint per finished step
   (`/var/lib/byoh-hostagent/inplace-upgrade-<id>.json`, bound to Machine UID, version and role):
   Preflight (host = Node, kubelet = from/to, worker after the API server, control-plane manifests)
   → Repository (keyring has exactly the pinned fingerprint; source switched to the target minor)
   → Download (each .deb verified against its SHA256; a mismatch removes it and fails) → Kubeadm
   (dpkg + hold, `kubeadm version` = target) → KubeadmUpgrade → Kubelet (kubelet/kubectl, restart)
   → Verify (kubelet binary, kubelet active, Node `kubeletVersion` and Ready, on control planes the
   static-pod manifests and mirror pods of the target, running and ready). It never runs
   `kubeadm reset`, never purges or removes packages and never touches the bootstrap.
5. **Done.** The extension answers success only when the agent reports `Succeeded` with the target
   observed on the host **and** the Node, **and** it reads the target on the Node itself; it then
   uncordons the Node. A `Running` upgrade older than `--upgrade-timeout` (2 h), a `Failed`
   upgrade, an inconsistent report or an unreadable state is never success.

The ByoHost webhook keeps the request immutable while it runs (except a larger `attempt`), binds it
to `spec.byoMachineRef.uid`, refuses it on a host being reset, and lets an agent report only the
request in its spec; `Succeeded` needs the observed target versions.

## Interrupted and failed upgrades (forward recovery)
- **Agent restart / host reboot during a step:** the status stays `Running`; the agent resumes from
  its checkpoint and re-runs only the interrupted step (each step is idempotent). Nothing to do.
- **Extension restart:** stateless; the next UpdateMachine call continues from the ByoHost.
- **`Failed`:** CAPI keeps the Machine in the in-place update and never falls back to replacement.
  1. Read `status.inPlaceUpgrade.step/message` and the agent journal on the host.
  2. Fix the cause on the host **forward** (never `kubeadm reset`, never downgrade packages).
  3. With separate approval: `kubectl patch byohost <h> --type merge -p '{"spec":{"inPlaceUpgrade":{"attempt":<n+1>}}}'`
     (rv-bound); the agent resumes from its checkpoint.
- **Timeout:** same as `Failed`; check whether the step still runs before raising the attempt.
- **Deleting a Machine after an upgrade (host reset):** the ByoMachine controller requests the reset
  of the host only when its upgrade is `Succeeded` for exactly the request in the spec (id and
  attempt); it then removes the request in the same write. For a `Running`, `Failed` or unreported
  request it waits (ByoMachine condition `BYOHostReady=False`, reason `WaitingForInPlaceUpgrade`):
  `Failed` is final in the API, but a step that timed out may still run on the host
  (`systemctl list-units 'okx-inplace-*'`).
  The webhook lets an administrator remove a request that is not `Running`, after which the
  controller uses the ordinary reset. This is not a regular step: it is a recovery with separate
  approval, after the units of the upgrade have been checked on the host. The result stays in
  `status.inPlaceUpgrade`.
- **Never:** remove the CAPI in-place annotations or pending hooks by hand, delete a Machine,
  ByoMachine or ByoHost, or edit the checkpoint file. Each is a separate, explicit decision.
- **Rollback:** there is none in place (kubeadm does not downgrade). A broken control plane is
  recovered from the etcd snapshot taken before the control-plane upgrade (writes after the
  snapshot are lost); this is a separate disaster-recovery procedure.

## Grants
One grant per KubeadmControlPlane and one per MachineDeployment, **without** `machineUID`: it authorizes the
version patch of the KCP/MD (intent guard) and the in-place transitions of each of its Machines. Grants bound to a
Machine UID next to it make the lookup ambiguous and are denied (fail closed). A claimed grant stays usable until
`notAfter`; keep `notAfter` short (the length of the change window).

## Host commands that must not be interrupted
`dpkg --install` and `kubeadm upgrade` run in transient systemd units
`okx-inplace-<id>-<attempt>-<what>.service`. They outlive the agent (restart, crash, `update-agent`) and a step
timeout only stops the waiting, never the command. A restarted agent waits for a running unit of the same attempt
instead of starting it again; a unit that failed fails the step (journal: `journalctl -u <unit>`); the next attempt
runs in a new unit. `update-agent` refuses to restart the agent while such a unit runs. Packages are held again also
after a failed installation.

## Known limitations (review B10.23c)
- **Cluster-admin** can delete the webhook configuration, the admission policies or their bindings, and can
  impersonate the approver/initiator ServiceAccounts directly (impersonation is not admission-checked). These are
  the audited break-glass paths; alert on changes of `okx-no-replacement-guard`, the `okx-inplace-*` policies and
  on impersonation of the grant identities.
- **Namespace writers of `okx-inplace-system`** could still edit ReplicaSets or Pods of the guard (the policies
  protect the Deployments, the registry, the package lock and the identities). Only platform administrators may
  write in that namespace (RBAC).
- **One host at a time** is a check before the drain, not a lock; the per-host approval annotation is the binding
  hold point (approve one host, wait for its verification, then the next).
- **Grant holders**: an initiator with a claimed grant can complete a Machine's in-place update without host work
  (the guard allows the cleanup transitions); the initiator is trusted, the agent status and the Node version show
  the truth (`progress.sh`, `uids.py compare --expect-version`).
- The extension waits without a limit for an agent that never accepts a request (never success); an agent that
  fails after `kubeadm upgrade` and a restored etcd may need its checkpoint removed by an operator before a new
  attempt; success uncordons the Node even if it was cordoned before the upgrade; the drainer may evict on every
  Node (PodDisruptionBudgets apply); a registered namespace without the label is not guarded (preflight checks it).


While a namespace is protected, the guard denies Machine and KCP/MD CREATE/DELETE, so a teardown
is impossible by design. Decommission is a separate, approved operation:
1. Confirm no in-place upgrade is running (`hack/inplace/progress.sh`).
2. Remove the namespace from the trusted registry ConfigMap and roll the guard (platform admin).
3. Remove the label `safety.openkubes.org/in-place-only` from the namespace.
4. Verify a server-side dry-run Machine delete is now admitted, then run the cluster teardown.
The registry entry is the authority; removing only the label leaves the guard failing closed for
that namespace (labelled-but-unregistered ≠ unregistered-but-labelled; both are checked).

## Lessons of the first run
What the first real run showed, beyond the mechanism above. Each item was observed, not derived.

- **A component that is Ready and discovered proves nothing about the hooks.** Every defect of the run was in a hook
  or host path that the unit and envtest suites covered with an input Cluster API never sends (a Machine with a
  UID, a MachineSet whose template names `ByoMachine`). Check the first real hook answer in the extension log
  before approving a host.
- **Cluster API caches the answer of `CanUpdateMachineSet` for 24 h** per MachineSet pair, in the memory of its core
  controller. After a fix of the extension the declining answer stays until `capi-controller-manager` restarts.
  That restart is an approved step of its own.
- **A declined hook means replacement.** When the extension declines, Cluster API scales the old MachineSet down
  and sends Machine DELETEs, every few seconds at first, then every ten minutes. The guard is then the only
  barrier; it held for 54 minutes in the run. Do not leave a cluster in that state longer than the fix needs.
- **Workers move between MachineSets.** The Machine keeps its name and UID and changes its controller MachineSet
  and two labels; the MachineSet controller then applies the same two labels to the ByoMachine. The guard admits
  that re-alignment only when it is bound to the move (owner Machine by UID and infrastructureRef, target and source
  MachineSet of one MachineDeployment, claimed grant with `MD-FIN-IM`). Without a valid grant it stays denied and
  the new MachineSet's reconcile is blocked.
- **Grant windows.** A grant cannot be deleted or changed, and two valid grants of one target are ambiguous and
  denied. If the window ends before the rollout does, wait for the grant to expire, then render one with a later
  `--not-after` (`render_grant.py` gives it another name). Plan the window for the fixes you may need, not for the
  host steps alone: the five hosts took 30 s to 5 min each, the run took seven hours.
- **Failed attempt, forward recovery.** A failed step leaves the host as it was at that step; the checkpoint and the
  downloaded packages survive an agent update (`update-agent` accepts a host whose attempt is `Failed`). Raising
  `spec.inPlaceUpgrade.attempt` resumed at the failed step. The failed transient unit of the previous attempt does
  not block the next one.
- **`kubeadm` upgrades the addons with the last control plane**, not the first: kube-proxy and CoreDNS stay at the
  old version until every control plane runs the target. etcd runs mixed versions meanwhile.
- **The API is not uninterrupted.** With a load balancer in front of three API servers, single one-second probes
  failed during each control-plane upgrade (1, 4 and 6). Measure before promising availability.
- **Hosts with least-privilege RBAC** (status only) need the agent of #40 or later; older agents log one forbidden
  patch per upgrade, without effect.

## Approvals (each separately)
Package/image locks and their review; deploying guard and extension; enabling `RuntimeSDK` and
`InPlaceUpdates`; KCP `maxSurge` 1 → 0; protecting the namespace (label + registry); etcd
snapshot; each grant creation (approver) and claim (initiator); the KCP version patch; the MD
version patch; any attempt increase; decommission.

## Evidence
Before/after: etcd snapshot sha256 and restore verification, `hack/inplace/uids.py` snapshots and
comparison (Machines, ByoMachines, ByoHosts, Nodes; versions), `progress.sh` per step, the
`monitor.sh` API/etcd logs, guard and extension logs, agent journals; sealed with SHA256SUMS,
without secrets or kubeconfigs.

## References
- Historical manual upgrade: [upgrade-1.37.md](scaleway-kubeadm-e2e/upgrade-1.37.md) <!-- provider-neutrality: allow (E2E lab record) -->
- Upgrade automation baseline: [upgrade-make-runbook.md](scaleway-kubeadm-e2e/upgrade-make-runbook.md) <!-- provider-neutrality: allow (E2E lab record) -->
- E2E lab rollout plan, as executed: [inplace-upgrade-ha1-rollout.md](scaleway-kubeadm-e2e/inplace-upgrade-ha1-rollout.md) <!-- provider-neutrality: allow (E2E lab record) -->
- E2E lab test protocol (first successful run): [inplace-upgrade-testprotocol.md](scaleway-kubeadm-e2e/inplace-upgrade-testprotocol.md) <!-- provider-neutrality: allow (E2E lab record) -->
- Tooling: `hack/inplace/` (preflight, progress, monitor, etcd-snapshot, uids, render-registry; the per-host gate
  `host-step.sh` with `host_step.py`; grants rendered by `render_grant.py`; connectivity/policy checks `netcheck.sh`;
  identity checks of the kubeconfigs in `lib.sh`, Cilium compatibility in `cilium_compat.py`; offline tests in
  `hack/inplace/test`)
- Deployment: `config/inplace/base` (generic) and per-lab overlays (`config/inplace/overlays/<lab>`)
- Grant contract: `labs/byoh-inplace-spike/design/M1-grant-contract-and-transition-matrix.md`
