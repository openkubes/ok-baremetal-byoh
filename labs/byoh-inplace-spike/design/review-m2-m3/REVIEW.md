# M2 + M3 Review Package (for joint Codex review)

Scope: authorization core (M2) + admission webhook configuration (M3) for the BYOH
in-place upgrade guard. Governed by Design r9 / ADR-Platform-049 / ADR-Platform-047.
NO further implementation, commits or pushes until this review concludes.

Source of truth for all CAPI references: cluster-api v1.14.3 @ 825a79dc92ddcc...

---

## 1. Review set — actual files

Foundation (M1):
- `labs/byoh-inplace-spike/design/M1-grant-contract-and-transition-matrix.md` — grant contract + source-verified KCP/MD/MS transition matrix.

M2 — authorization core (new, additive; 0 edits to existing guard files):
- `internal/safety/noreplacement/grant.go` (352) — Grant type, strict ParseGrant, VerifyGrant predicate (incl. dry-run).
- `internal/safety/noreplacement/grantclaim.go` (105) — guarded-ConfigMap role/field separation + single-use claim.
- `internal/safety/noreplacement/grant_test.go` (221) — grant parse/verify tests.
- `internal/safety/noreplacement/grantclaim_test.go` (104) — claim-CAS / role tests.

M3 — webhook configuration (new) + 1 reuse edit:
- `internal/safety/noreplacement/webhookpaths.go` (35) — single source of truth for the 4 admission paths + protected-namespace label.
- `internal/safety/noreplacement/webhook_negative_test.go` (102) — negative tests: unprotected ns / missing grant / wrong requester.
- `config/webhook/no-replacement/machine-webhook.yaml` — ACTIVE Machine webhook.
- `config/webhook/no-replacement/pending-m6-webhooks.yaml` — PENDING MachineSet/ByoMachine/Grant-ConfigMap webhooks (not applied).
- `config/webhook/no-replacement/kustomization.yaml`, `README.md`.
- `cmd/no-replacement-guard/main.go` — 1 line: path from `noreplacement.MachineAdmissionPath` (drift prevention).

Full unified diff (including untracked file contents): `m2-m3.diff` in this directory.

---

## 2. Test status — PASS / PENDING / FAIL (explicit)

PASS — isolated offline tests (`GOTOOLCHAIN=local`, stdlib-only subset: policy.go, registry.go,
grant.go, grantclaim.go, webhookpaths.go + tests), `ok okx.local/grantoffline/noreplacement`:
- 21 test functions, incl. grant parse fail-closed, VerifyGrant bindings, dry-run-without-claim allow,
  real-write-without-claim deny, two-competing-claims (exactly one wins), payload-digest constant-time,
  and the 3 M3 negatives (unprotected ns not gated / missing grant deny / wrong requester deny).
- Both webhook YAML manifests parse; failurePolicy=Fail, sideEffects=None, namespaceSelector=protected,
  resources exclude machines/status — asserted.

PENDING — only runnable on Mac / CI (NOT run here; the sandbox cannot fetch Go modules or the go1.26
toolchain — proxy.golang.org / vanity hosts egress-blocked):
- Full-package `go test ./internal/safety/noreplacement/...` (files importing CAPI/controller-runtime).
- go1.26 build of the whole module.
- `kubeconform`/schema validation of the webhook manifests.
- Live apiserver admission behaviour + webhook routing-scope (namespaceSelector/objectSelector).

FAIL — none.

IMPORTANT: the isolated Go tests are PASS; the full module build and Kubernetes admission tests remain
PENDING and are NOT claimed as PASS.

---

## 3. Security analysis Q1 — namespace/object selector bypass (analysis only, no new infra built)

### 3.1 The concern
The Machine webhook routes only to namespaces labelled `safety.openkubes.org/in-place-only=true`
(namespaceSelector). The grant-ConfigMap webhook routes only to ConfigMaps labelled
`safety.openkubes.org/grant=true` (objectSelector). An attacker who can edit those labels could try to
make a protected object stop matching the selector, so the guard is never called.

### 3.2 Namespace-label bypass — REAL, must be addressed by policy, not by the selector
`namespaceSelector` is evaluated against the live Namespace object. Removing the label is a separate
**Namespace UPDATE** that the Machine/MachineSet/ByoMachine webhooks do not see (they match on the
workload objects, not on Namespaces). After the label is gone, subsequent Machine DELETEs are no longer
routed → bypass. This is an inherent limitation of selector-scoped webhooks.

Minimal defenses (reuse-first, no parallel infrastructure):
1. **RBAC is the primary control.** In the BYOH tenant model, the protected namespace and its labels are
   platform-managed; tenants must NOT hold `patch namespaces`. The webhook selector is a routing
   optimization, not the trust boundary. The **trusted registry** (`/etc/openkubes/no-replacement/registry.json`,
   not cluster-mutable) is already the Cluster-UID authority — the label is only routing.
2. **Optional defense-in-depth (same guard server, one extra path — NOT new infra):** a Namespace
   UPDATE/DELETE webhook that denies removal of `in-place-only=true` (and deletion) for any namespace
   present in the trusted registry. The guard already knows the authoritative protected-namespace set.
   Open question for Codex: add this now, or rely on RBAC for Phase 0 and defer?

### 3.3 Grant-ConfigMap objectSelector bypass — largely closeable WITHOUT depending on selector semantics
Two observations:
- On UPDATE, Kubernetes evaluates `objectSelector` and the webhook can still see the request as long as
  the matching object is presented; the guarded-ConfigMap handler (M6) will DENY removal of the
  `grant=true` label and any grant-data mutation, so an attacker cannot "unlabel then tamper" in one
  admitted step. (Exact old-vs-new objectSelector evaluation on UPDATE is version-nuanced — flagged as a
  PENDING k8s-behaviour confirmation, see open questions.)
- Selector-independent mitigation (recommended, minimal, reuse): the guard **recognizes a grant by its
  namespaced name convention** (`okx-grant-*`) in addition to the label. Name-based recognition cannot be
  evaded by label edits, removes reliance on the objectSelector for the security decision, and needs no
  new infrastructure (the handler already reads the object).

### 3.4 Open questions (Q1)
- Add the Namespace-label-protection webhook in Phase 0, or rely on RBAC and defer? (defense-in-depth vs minimalism)
- Confirm exact objectSelector old/new evaluation on UPDATE for the target apiserver version.
- Accept name-based grant recognition (`okx-grant-*`) as the primary selector-evasion mitigation?

---

## 4. Security analysis Q2 — MachineSet allow-by-default (proven against CAPI v1.14.3 source)

Claim: MachineSet admission may be **allow-by-default**, gating only the two move-authorization
annotations, because **every path to machine replacement or unauthorized version change terminates at a
Machine-level admission gate** that already exists.

### 4.1 Source-verified facts (file:line in cluster-api v1.14.3)
- **Scale-down replaces by DELETING Machines** → real Machine DELETE API calls:
  `core/reconcilers/machineset/machineset_controller.go:589` (`r.Client.Delete(ctx, machine)`),
  `:1061` and `:1130` (`r.machineClientWithDeleteResponse.Delete(ctx, machine)`).
  → gated by the Machine DELETE webhook (EvaluateMachineLifecycle Delete → deny under protected scope).
- **Scale-up / new MachineSet creates Machines** → Machine CREATE
  (`machineset_controller.go:958` computeDesiredMachine(nil) → `:1005` ssa.Patch create).
  → gated by the Machine CREATE webhook (EvaluateMachineLifecycle Create → deny under protected scope).
- **MachineSet template version is NOT pushed to existing Machines by syncMachines**:
  `machineset_controller.go:440-441` ("spec.version and spec.failureDomain are not mutated in-place by
  syncMachines"); `computeDesiredMachine:1283` sets `desiredMachine.Spec.Version = existingMachine.Spec.Version`.
  The ONLY MS-side version write is `completeMoveMachine:444` (`desiredMachine.Spec.Version =
  machineSet.Spec.Template.Spec.Version`) → the grant-gated **MD-FIN-M** Machine transition.
- **The move itself is a Machine UPDATE** (ownerRef source→target + label + annotations,
  `machineset_controller.go:1173-1203`) → the grant-gated **MD-MOVE** Machine transition.

### 4.2 Consequence for each MachineSet field
| MachineSet change | Replacement / unauthorized-version path? | Where it is actually stopped |
|---|---|---|
| `spec.replicas` down | delete Machines | **Machine DELETE deny** |
| `spec.replicas` up / new MS | create Machines | **Machine CREATE deny** |
| `spec.template` (version) | only via move or rollout | **MD-FIN-M grant-gate** (move) or **Machine CREATE/DELETE** (rollout); syncMachines does not propagate version |
| `spec.template` (other) | triggers rollout → new MS + replace | **Machine CREATE/DELETE deny** |
| OwnerReferences | re-parent MS; cannot move a Machine by itself | Machine move is **MD-MOVE grant-gated** (Machine UPDATE) |
| status | no machine mutation | n/a |
| move annotations (`acknowledged-move`, `receive-machines-from`) | coordination only; cannot move a Machine without the grant-gated MD-MOVE | **this is the only MS-specific residual** |

### 4.3 Minimal necessary protection (recommended)
Because the Machine gate backstops replacement and unauthorized version, the MachineSet handler should:
- **Allow** replicas, template, ownerRefs, status, CREATE, DELETE (denying them would break legitimate
  in-place and normal MD operations, with failurePolicy: Fail).
- **Deny only** the addition/mutation of `acknowledged-move` / `receive-machines-from` when there is no
  valid MD grant binding this exact target MS (UID) to the grant's MD UID.
This is defense-in-depth: even these two annotations cannot by themselves move/replace a Machine (the
move is the grant-gated MD-MOVE Machine UPDATE). Open question: keep this minimal annotation-gate, or
(given the Machine gate already suffices) defer MachineSet admission entirely for Phase 0?

### 4.4 Open questions (Q2)
- Keep the minimal two-annotation gate, or defer MachineSet admission (Machine gate is sufficient)?
- Any concern that MachineSet DELETE (orphaning Machines) could later enable adoption-based replacement
  by another controller? (Machine ownerRef adoption is itself a Machine UPDATE → gated; to confirm.)

---

## 5. Consolidated open security questions for Codex
1. Namespace-label bypass: RBAC-only for Phase 0, or add a Namespace-protection webhook now?
2. Grant objectSelector: accept name-based recognition (`okx-grant-*`) as the evasion-proof primary; confirm objectSelector old/new UPDATE semantics.
3. MachineSet: minimal two-annotation gate vs defer entirely (Machine gate already backstops replacement)?
4. Confirm the Machine DELETE/CREATE deny remains unconditional even under a valid grant (grants authorize in-place UPDATE transitions only, never CREATE/DELETE) — re-assert in M6 wiring.
5. PENDING verification plan: full `go test`, go1.26 build, kubeconform, live apiserver admission on Mac/CI before any activation.
