# Phase 0 · M1 — Grant Contract & Exact KCP/MD Transition Matrix

Status: foundation for Phase 0 implementation (Design r9 / ADR-Platform-049, governed by ADR-Platform-047).
Scope: authorization contract + guard transition matrix ONLY. No production host executor (see ADR-049 §5 scope boundary).

Source of truth for every CAPI write below: **cluster-api v1.14.3 @ 825a79dc92ddcca6acc935e233201c697a2f7488**
(go.mod pins `sigs.k8s.io/cluster-api v1.14.3`). Every line reference was read from that tag, not from memory.

Existing OpenKubes guard reused (not reinvented):
`internal/safety/noreplacement/{policy,update,scope,registry,config,setup,kubernetes,admission}.go`
+ `cmd/no-replacement-guard/main.go` (admission path `/validate-cluster-x-k8s-io-v1beta2-machine-no-replacement`).

---

## 0. Verified CAPI constants (identity of the in-place writes)

| Constant | Value | Source |
|---|---|---|
| `UpdateInProgressAnnotation` (Machine/InfraMachine/BootstrapConfig) | `in-place-updates.internal.cluster.x-k8s.io/update-in-progress` | api/core/v1beta2/machine_types.go:100 |
| `PendingAcknowledgeMoveAnnotation` (Machine) | `in-place-updates.internal.cluster.x-k8s.io/pending-acknowledge-move` | api/core/v1beta2/machine_types.go:96 |
| `AcknowledgedMoveAnnotation` (MachineSet) | `in-place-updates.internal.cluster.x-k8s.io/acknowledged-move` | api/core/v1beta2/machineset_types.go:58 |
| `PendingHooksAnnotation` (Machine) | `runtime.cluster.x-k8s.io/pending-hooks` | api/runtime/v1beta2/extensionconfig_types.go:317 |
| `MachineDeploymentUniqueLabel` (Machine label) | `machine-template-hash` | api/core/v1beta2/machinedeployment_types.go:73 |
| `MachineSetReceiveMachinesFromMachineSetsAnnotation` (target MS) | internal move annotation | api/core/v1beta2/machineset_types.go |

Guard-relevant observation: the three in-place annotation domains
(`in-place-updates.internal.cluster.x-k8s.io`, `runtime.cluster.x-k8s.io`) both end in `.cluster.x-k8s.io`,
so the **current** `reservedMetadataKey()` (update.go) already treats them as reserved → the current
`EvaluateMachineUpdate` DENIES every in-place write today. The grant exception is the ONLY new allow-path.
`machine-template-hash` is unqualified → not reserved → but the MD move is still denied today by the
owner-reference check in `EvaluateMachineUpdate`. Confirmed: no accidental allow exists.

---

## 1. Authorization Grant Contract (ADR-047 Upgrade Grant, Phase-0 realization)

### 1.1 One shared CAS object (criterion 1)
Phase 0 realizes the grant as a **single guarded ConfigMap** per in-place operation, in the protected namespace,
carrying BOTH the immutable grant data and the single claim slot. No new CRD, no new controller.

```
kind: ConfigMap
metadata:
  name: okx-grant-<grantID>
  namespace: <protected-namespace>
  labels: { safety.openkubes.org/grant: "true" }
data:               # --- immutable grant block, written ONCE by the Approver on CREATE ---
  grantID:               <uuid>
  clusterUID:            <CAPI Cluster UID>            # bound, verified against live Cluster
  targetKind:            KubeadmControlPlane | MachineDeployment
  targetName:            <name>
  targetUID:             <UID of KCP or MD>
  machineUID:            <Machine UID>                 # REQUIRED for KCP singleton; omitted for MD (set-move)
  fromVersion:           v1.33.x
  toVersion:             v1.34.y
  transitions:           <canonical list of enumerated transition IDs, see §3>
  payloadDigest:         sha256:<digest of the actual InstallationSecret payload bytes for toVersion>
  notAfter:              <RFC3339>
  permittedUseCount:     "1"
  requestDigest:         sha256:<who requested + what>   # ADR-047 binding
  authorizationDigest:   sha256:<approver authorization>  # ADR-047 binding
binaryData/claim:   # --- claim slot, written ONCE by the Upgrade-Initiator, atomically (see §1.3) ---
  claim:                 <claimToken> (claimedBy, claimedAt)
```

### 1.2 Role separation enforced at admission (criterion 1)
ConfigMaps have no subresources, so field-level write separation is enforced by the guard admitting the
grant ConfigMap itself (the "guarded ConfigMap"):

| Actor | Allowed write | Enforced rule |
|---|---|---|
| **Approver** (SA: approver) | CREATE grant ConfigMap with the immutable `data` block, NO `claim` | On CREATE: `claim` key MUST be absent. After CREATE the `data` block is immutable. |
| **Upgrade-Initiator** (SA: initiator) | UPDATE adding ONLY the `claim` key, exactly once | On UPDATE: every `data` key byte-identical to stored; the ONLY delta permitted is adding `claim`; if `claim` already present → DENY. |
| **Guard** (SA: guard) | none | read-only `get`; never writes the grant or the claim. |
| anyone | mutate/delete any `data` key, or re-claim | DENY (fail-closed). |

RBAC backs this (approver cannot `update`, initiator cannot change `data`, guard is `get`-only), and the
admission rule is the field-level enforcement RBAC alone cannot express.

### 1.3 Atomic single-use claim (criterion 4: "zwei konkurrierende Claims: genau einer gewinnt")
The claim is an **out-of-band atomic CAS**, independent of any CAPI write:
- The Initiator reads the grant (gets its `resourceVersion`), then issues an UPDATE adding `claim`,
  conditioned on that `resourceVersion` (apiserver optimistic concurrency).
- Two concurrent claims: the apiserver admits exactly one (the other gets HTTP 409 Conflict); additionally the
  guard denies any UPDATE whose old object already carries `claim`. Exactly one winner — by construction, twice over.
- The guard **verifies** the claim read-only; it never mints or consumes it. `permittedUseCount=1` + "claim present"
  is the consumed state.

### 1.4 Unclear claim-write outcome (criterion 1: "zuerst den dauerhaften Zustand prüfen")
If the Initiator's claim UPDATE returns an ambiguous result (timeout / connection reset / unknown), the Initiator
MUST re-read the durable grant object and inspect whether `claim` landed **before** any retry.
- `claim` present and equal to its token ⇒ success; do NOT retry.
- `claim` absent ⇒ safe to retry under a fresh `resourceVersion`.
- `claim` present but foreign ⇒ lost the race; abort.
Identical requests alone are never a retry trigger (criterion 1): retry requires positive proof the write did not land.
This mirrors ADR-047 INV-047-11 (INDETERMINATE claim recovery via bounded idempotent re-submission).

### 1.5 Guard verification predicate (read-only)
For a protected, **non-dry-run** in-place admission the guard ALLOWS iff ALL hold, else DENY (fail-closed):
1. scope verified & protected (`ResolveRegisteredScope`, bound to live Cluster UID) — reused unchanged;
2. grant object present, `notAfter` not passed, `clusterUID` == live Cluster UID;
3. `targetKind/targetUID` match the owner chain of the object under admission;
4. the admission diff version delta == `fromVersion`→`toVersion` (where the transition carries a version);
5. the admission diff **shape ⊆ the enumerated transition** (§3) named by `transitions`;
6. `payloadDigest` == sha256 of the **actual InstallationSecret payload bytes** (criterion 2) — never a digest
   recomputed from the admission request bytes (that was the rejected greenfield error);
7. `claim` present and single-use valid (`permittedUseCount` not exceeded).
The guard performs NO write. PASS_DENIED remains the real CAPI-DELETE/audit denial signal (unchanged).

### 1.6 Dry-run handling (criterion 2: KCP dry-run is part of the matrix)
`CanUpdateMachine` issues **server-side dry-run SSA patches** that carry the version bump
(inplace_canupdatemachine.go:160 Machine, :169 currentInfraMachine, :178 desiredInfraMachine, all `ssa.WithDryRun{}`).
These reach admission with `req.DryRun=true` and **persist nothing**.
Guard rule for `req.DryRun=true`: verify predicate 1–6 (grant present/valid/shape/version/payload), but **predicate 7
is NOT required and the claim is NEVER consumed**. Allow the shape-valid dry-run probe; deny otherwise.
Rationale: denying the dry-run version bump would make `canUpdateMachine` return false → KCP falls back to
**replacement** → fatal on a singleton control plane. Dry-run cannot mutate, so allowing it is safe. This is exactly
ADR-047's "read-only admission is dry-run safe".

---

## 2. Actors & ordering (claim-before-intent, no coordinator)

```
Approver  --CREATE grant (immutable)-->  grant ConfigMap
Initiator --set KCP/MD .spec.version (intent)  AND  --atomic claim (out-of-band)-->  grant ConfigMap
          (claim is set before CAPI performs its first PERSISTENT in-place write)
CAPI/KCP/MS controllers --dry-run probe (no claim needed)--> ... --first persistent write (claim verified)-->
Host agent  [Phase 0: CAPD test handler, NOT a production executor]
```
The first persistent in-place write is the `UpdateInProgressAnnotation` MergeFrom patch
(inplace_trigger.go for KCP; startMoveMachines for MD). Everything before it (`CanUpdateMachine`) is dry-run only.

---

## 3. Exact transition matrix (enumerated, source-verified)

All transitions below are **Machine/InfraMachine UID-stable** (Name/Namespace/UID unchanged) and are DENIED by the
current guard today; each becomes an allow ONLY under a valid, claimed, shape-matching grant.

### 3.1 KCP control-plane path — object: **Machine** (controlplane/kubeadm/reconcilers/kubeadmcontrolplane/inplace_trigger.go)

| ID | Phase | Admission op (field manager) | Exact diff on Machine | Dry-run? | Claim |
|---|---|---|---|---|---|
| KCP-DRY | CanUpdate | UPDATE/PATCH SSA `kcpManagerName`, DryRun | desired spec incl. `spec.version`=to (probe) | yes | not consumed |
| KCP-1 | Trigger | PATCH MergeFrom (kcp client) | add annotation `update-in-progress=""` (only if absent); spec unchanged | no | **verify** |
| KCP-2 | Trigger | PATCH SSA `kcpManagerName` (desiredMachine) | `spec.version` from→to (+ kcp-owned desired labels/annos); ownerRefs/finalizers/infraRef/bootstrap/providerID unchanged | no | verify |
| KCP-3 | Trigger | PATCH MergeFrom (hooks.MarkAsPending) | add `pending-hooks` containing `UpdateMachine` | no | verify |
| KCP-C1 | Cleanup | PATCH MergeFrom (MarkAsDone) | remove `UpdateMachine` from `pending-hooks` | no | verify (same grant) |
| KCP-C2 | Cleanup | PATCH MergeFrom | delete annotation `update-in-progress` | no | verify (same grant) |

Node version (`status.nodeInfo.kubeletVersion`) is written via the **status subresource**
(machine_controller_inplace_update.go) — out of scope of the main-resource webhook (§5: webhook scoped to
`machines`, not `machines/status`; disjoint main vs status authz per ADR-047).

### 3.2 KCP path — object: **InfraMachine (ByoMachine)** (same inplace_trigger.go, SSA `kcpManagerName`)

| ID | Diff on InfraMachine | Claim |
|---|---|---|
| KCP-IM | labels=nil; annotations set to {clonedFrom-name, clonedFrom-groupKind, `update-in-progress`=""} | verify |
| KCP-IM-C | cleanup: delete `update-in-progress` (machine_controller_inplace_update.go:244-247) | verify (same grant) |

### 3.3 MachineDeployment worker path — object: **Machine** (core/reconcilers/machineset/machineset_controller.go)

| ID | Phase | Admission op (field manager) | Exact diff on Machine | Claim |
|---|---|---|---|---|
| MD-MOVE | source scale-down (startMoveMachines :1173-1203) | **single** PATCH MergeFrom (ms client) | (a) controller ownerRef MachineSet replaced source-MS→target-MS (same Machine UID); (b) label `machine-template-hash` source→target value; (c) add `update-in-progress=""`; (d) add `pending-acknowledge-move=""` | **verify** |
| MD-FIN-M | target finalize (completeMoveMachine :505) | PATCH SSA `machineSetManagerName` (desiredMachine) | `spec.version`=target template version, `spec.failureDomain` set (+ ms-owned desired) | verify |
| MD-TRIG | target trigger (triggerInPlaceUpdate :367) | PATCH MergeFrom | add `pending-hooks`(UpdateMachine); delete `pending-acknowledge-move` | verify |
| MD-C1/C2 | cleanup | PATCH MergeFrom | remove `pending-hooks`; delete `update-in-progress` | verify (same grant) |

### 3.4 MD path — object: **InfraMachine (ByoMachine)** (completeMoveMachine :493 SSA)

| ID | Diff on InfraMachine | Claim |
|---|---|---|
| MD-FIN-IM | labels=nil; annotations {clonedFrom-name, clonedFrom-groupKind, `update-in-progress`=""} | verify |

### 3.5 MD path — object: **MachineSet** (acknowledge) — REQUIRED in Phase 0 (decided)

The full MachineDeployment in-place lifecycle is guarded, so MachineSet UPDATE is admitted with a dedicated,
tightly-scoped handler. Under a valid MD grant the ONLY allowed MachineSet delta is:

| ID | Allowed diff on MachineSet | Hard constraints |
|---|---|---|
| MD-ACK | add/extend `in-place-updates.internal.cluster.x-k8s.io/acknowledged-move` (replica-name list) and `...receive-machines-from` annotation | (a) MUST be the **approved target MachineSet** only (its UID == grant `targetUID` when `targetKind=MachineSet`, resolved as the target MS of grant's MD); (b) **exact annotation deltas only** — add/extend acknowledged-move + receive-machines-from, no value rewrite of other keys; (c) bound to the **same MachineDeployment UID** (target MS must carry ownerRef/`MachineDeploymentNameLabel` of the grant's MD); (d) **no `spec` change**, no other label/annotation/ownerRef/finalizer change. |

Everything else on a MachineSet under a protected scope stays **DENY**: any `spec` change (replicas, selector,
template), any CREATE/DELETE, any ownerReference/finalizer change, any metadata change other than the two
enumerated move annotations, and any MachineSet that is not the grant's approved target. Fail-closed.

### 3.6 Default-deny (unchanged, BYOH API-object protection — criterion 3/4)
Everything NOT in 3.1–3.5 stays DENIED under a protected scope, grant or no grant:
- Machine/InfraMachine **CREATE** (replacement/provisioning), **DELETE** → deny; PASS_DENIED stays the audit proof.
- Any `spec` change on Machine other than the enumerated `spec.version` from→to.
- Any ownerReference change other than MD-MOVE's source→target controller ref.
- Any finalizer change; any reserved metadata change not enumerated above.
- ByoHost/ByoMachine **detach / rebind / ResetRequested / finalizer strip / installation-secret deletion** → deny
  (no grant input accepted for these; they are never an in-place transition).
- Any MachineSet change other than the enumerated MD-ACK delta on the approved target MS (§3.5) → deny.
- Identity drift (UID/Name/Namespace changed) → deny.

---

## 4. What the guard must gain vs. what already exists

| Needed | Status | Where |
|---|---|---|
| Scope resolution bound to Cluster UID | EXISTS, reuse | scope.go / registry.go / kubernetes.go |
| Deny-all baseline for protected Machine UPDATE/CREATE/DELETE | EXISTS, reuse | policy.go / update.go / admission.go |
| Grant read + verification predicate (§1.5) | TO-BUILD | new `grant.go` in noreplacement |
| Dry-run branch (§1.6) | TO-BUILD | admission.go (read `req.DryRun`) |
| Enumerated transition shape check (§3) | TO-BUILD | new `transition.go` (KCP + MD shapes) |
| InfraMachine (ByoMachine) admission handler | TO-BUILD | new handler + webhook path |
| MachineSet admission handler (MD-ACK only, §3.5) | TO-BUILD | new handler + webhook path |
| Guarded-ConfigMap admission (role/field separation §1.2) | TO-BUILD | new handler + webhook path |
| Missing ValidatingWebhookConfiguration | TO-BUILD (M3) | config/webhook + cmd/no-replacement-guard |
| InstallationSecret payload-digest check (§1.5.6) | TO-BUILD | grant.go (reads actual Secret bytes) |

---

## 5. Hard scope boundary for this milestone set (ADR-049 §5)
NOT in Phase 0: production host executor, `kubeadm upgrade apply/node`, installer `Upgrade()`, agent upgrade
reconcile, recovery execution, drain/uncordon, etcd backup/restore, Scaleway E2E. `UpdateMachine` is a **CAPD test
handler** only (reference: cluster-api v1.14.3 `test/extension/handlers/inplaceupdate/handlers.go`). After cleanup the
grant stays locked; no terminal `Completed`, no writer-quiescence claim.
