# BYOH In-Place Guard — Phase 0 Implementation Summary (M1–M6)

For the first coherent Codex code review. Governed by Design r9 / ADR-Platform-049 / ADR-047.
Source of truth for CAPI references: cluster-api v1.14.3 @ 825a79d. No commits/pushes.

## 1. Milestone status
| M | Scope | Status |
|---|---|---|
| M1 | Grant contract + source-verified KCP/MD/MS transition matrix | DONE (doc) |
| M2 | Grant verification + guarded-ConfigMap claim CAS | DONE, offline-TESTED |
| M3 | ValidatingWebhookConfiguration (reuse guard server) | DONE, YAML-validated |
| M4 | Read-only CanUpdateMachine / CanUpdateMachineSet (version-only) | DONE; offline tests and full Mac package tests PASS |
| M5 | CAPD UpdateMachine test handler | DONE; offline tests and full Mac package tests PASS |
| M6 | Transition matcher + guard wiring + M3 security fixes | DONE; full Mac build and package tests PASS, CAPD validation PENDING |

## 2. Complete Phase-0 code inventory

### Pure policy core — stdlib-only, OFFLINE-TESTED (PASS)
- `internal/safety/noreplacement/grant.go` — Grant type, strict ParseGrant, VerifyGrant (+dry-run), transition IDs.
- `internal/safety/noreplacement/grantclaim.go` — approver/initiator field separation + single-use claim CAS.
- `internal/safety/noreplacement/transition.go` — MachineChange classifier (shapes) + TransitionIDForShape + EvaluateMachineUpdateInPlace.
- `internal/safety/noreplacement/machineset.go` — allow-by-default MachineSet gate (move-annotation only).
- `internal/safety/noreplacement/byomachine.go` — deny-by-default ByoMachine in-place gate.
- `internal/safety/noreplacement/grantselect.go` — name-based grant recognition (okx-grant-*) + label-removal denial.
- `internal/safety/noreplacement/webhookpaths.go` — single source of truth for the 4 admission paths + protected-ns label.
- `internal/inplace/decision.go` — version-only AcceptInPlace + UpdateDone (extension decision core).

### CAPI-typed wiring — Mac build PASS, CAPD integration PENDING
- `internal/safety/noreplacement/inplace_extract.go` — Extract{Machine,ByoMachine}Change, MachineSetMoveTouched (CAPI->pure).
- `internal/safety/noreplacement/inplace_guard.go` — GrantReader + KubernetesGrantReader + Guard{Machine,MachineSet,ByoMachine}Update + GuardGrantConfigMap (read-only bridge).
- `internal/safety/noreplacement/inplace_handlers.go` — admission.Handler adapters for MachineSet / ByoMachine / grant-ConfigMap.
- `internal/safety/noreplacement/inplace_resolve.go` — `TargetResolver` resolves Machine and ByoMachine owner chains for grant target selection. Compiles on Mac. Live CAPD validation is pending.
- `internal/safety/noreplacement/inplace_setup.go` — constructors + SetupInPlaceFromEnvironment.
- `internal/safety/noreplacement/admission.go` — MODIFIED (+WithGrantReader; UPDATE path uses GuardMachineUpdate when a reader is set; backward compatible).
- `cmd/no-replacement-guard/main.go` — MODIFIED (registers all four guarded paths via the shared path constants).
- `internal/inplace/extension/handlers.go` — DoCanUpdateMachine/MachineSet (read-only), DoUpdateMachine (CAPD test).
- `cmd/inplace-extension/main.go` — RuntimeSDK extension server.

### Webhook config (M3) — YAML validated, CAPD deployment pending
- `config/webhook/no-replacement/machine-webhook.yaml` is included by the current kustomization. `pending-m6-webhooks.yaml` remains excluded and its activation is pending. No webhook activation was performed during the build or this summary update.

### Tests
- Offline (stdlib): `grant_test.go`, `grantclaim_test.go`, `transition_test.go`, `m6_policy_test.go`, `webhook_negative_test.go`, `internal/inplace/decision_test.go`.

### Design docs
- `M1-grant-contract-and-transition-matrix.md`, `review-m2-m3/REVIEW.md`, `review-m2-m3/m2-m3.diff`, this summary.

Review snapshot: **21 untracked Go files** plus the tracked changes and other non-ignored untracked files. The review diff is based on `15b668d`.

## 3. Test status — PASS / PENDING / FAIL

### PASS — offline (`GOTOOLCHAIN=local`, stdlib-only subset of both packages)
`ok okx.local/grantoffline/noreplacement` and `ok okx.local/grantoffline/inplace`. Coverage:
- Grant parse fail-closed; VerifyGrant bindings (cluster/target/machine/version/payload/expiry); dry-run-without-claim ALLOW; real-write-without-claim DENY; two competing claims (exactly one wins); constant-time payload digest.
- Transition classifier: all 7 shapes + fail-closed on identity/finalizer/cluster-label/protection/other-reserved/ownerRef/extra-spec changes; KCP-vs-MD shape mapping.
- EvaluateMachineUpdateInPlace: claimed version bump ALLOW; unclaimed DENY; dry-run ALLOW; shape-none DENY; version mismatch DENY; MD move under MD grant ALLOW; KCP grant cannot authorize a move.
- MachineSet allow-by-default + move-annotation gate; ByoMachine deny-by-default in-place; grant name/label recognition + label-removal denial.
- Negative: unprotected namespace not gated; unregistered namespace fail-closed; missing grant DENY; wrong requester DENY.
- The earlier offline verification reported the checked Go files gofmt-clean.

### PASS — Second Mac build and package tests, 2026-11-21
- Effective toolchain: **Go 1.27.4 `darwin/arm64`** (`go-version.txt`).
- `go build ./...` → **PASS** (exit 0), including the owner-chain resolver, guard and extension wiring.
- `go test ./internal/safety/noreplacement/... ./internal/inplace/... -v` → **PASS** (exit 0). The guard package passed in 0.422s and the in-place package in 0.655s. The extension subpackage has no test files.
- Evidence: `_build/{_status.txt,go-version.txt,build.out,test.out}`. The recorded run finished at 19:03:44 CET. The commands were not rerun for this summary update.
- Review status: ready for independent code review. Compilation and package tests do not establish live owner-chain resolution or protected CAPI rollout behavior.

### PASS — Extension RuntimeSDK discovery in guarded CAPD, 2026-11-21
Root cause of the earlier discovery failure found and fixed. The RuntimeSDK extension
server never listened on :9444, so `ExtensionConfig okx-inplace-extension` stayed
`Discovered=False` with `connect: connection refused` (a TCP-level failure, not a
caBundle/TLS one). In controller-runtime v0.24.1 a server passed via
`Options.WebhookServer` is only added to the manager's runnables — and thus started —
the first time `GetWebhookServer()` is called (`controllerManager.webhookServerOnce`
-> `cm.Add(cm.webhookServer)`). `cmd/inplace-extension/main.go` never touched
`GetWebhookServer()`, so the server was constructed but never served (process alive,
no restarts, empty logs, nothing on :9444).

Fix (minimal, aligned with CAPI's own test extension):
- `mgr.AddReadyzCheck("webhook", mgr.GetWebhookServer().StartedChecker())` (+ healthz)
  — triggers the lazy runnable registration AND gives a truthful readiness signal.
- `ctrl.SetLogger(klog.Background())` — controller-runtime otherwise suppresses all logs.
- `HealthProbeBindAddress=:9440`; metrics server disabled (unused port).
- Real readiness/liveness probes added to `guarded/manifests/extension.yaml` (:9440).
- New helper `guarded/reload-extension-guarded.sh`: rebuild + `kind load` + rollout into
  the EXISTING `ok-byoh-guarded` only (no cluster create/teardown, no commit).

Evidence (`guarded/.state-guarded/evidence/reload/`): `go build ./...` + unit tests PASS;
gofmt clean; extension logs show `Serving webhook server ... port=9444` + TLS loaded from
`/tls/tls.crt`; after reload `ExtensionConfig Discovered=True` with handlers
`can-update-machine`, `can-update-machineset`, `update-machine`. This proves the
extension-discovery path only; the r9 §8 acceptance matrix below remains PENDING.

### PENDING — NOT yet run (never reported as PASS)
1. **CAPD integration (r9 §8 acceptance matrix)** — on a kind/CAPD mgmt cluster with the guard + extension deployed:
   - KCP accept with UNCHANGED Machine/InfraMachine/Node UIDs.
   - MD accept with real source-move + target finalization.
   - Two competing claims: exactly one wins.
   - Wrong / missing / forged grant denied; foreign requester denied.
   - ByoMachine delete / reset / rebind / detach prevented; PASS_DENIED preserved as a real CAPI-DELETE/audit signal.
   - Hook completion + full CAPI cleanup observed; grant stays locked; NO terminal Completed; NO writer-quiescence claim.
2. **Webhook activation** — validate and install the required webhook rules in the isolated CAPD environment before the protected acceptance runs. Repository manifests remain in their current state.
3. **Live owner-chain validation** — exercise `TargetResolver.ForMachine` and `ForByoMachine` against the CAPD owner chains, including the MD path. The Go build has passed.

### FAIL — none.

## 4. Security findings (M3) — addressed with the smallest safe solution, no new infrastructure
- **namespaceSelector bypass:** RBAC-primary (platform-managed protected namespaces); the trusted registry stays the Cluster-UID authority. Optional Namespace-label-protection webhook on the SAME guard server is noted as defense-in-depth (not built; open question for review).
- **grant objectSelector bypass:** closed via name-based recognition (`okx-grant-*`, IsGrantObject) AND denial of grant-label removal (grantselect.go) — the security decision no longer depends on the evadable label.
- **MachineSet allow-by-default:** proven against CAPI v1.14.3 that every replacement/unauthorized-version path terminates at a Machine gate; only the two move-authorization annotations are gated.

## 5. Build & test commands (Mac / CI)
```
go build ./...
go test ./internal/safety/noreplacement/... ./internal/inplace/... -v
# then CAPD: deploy cmd/no-replacement-guard + cmd/inplace-extension, run labs/byoh-inplace-spike acceptance.
```
