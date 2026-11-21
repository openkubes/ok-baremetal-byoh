# BYOH architecture (contributor view)

**Maturity:** component model & No-Replacement safety **validated**; RuntimeSDK in-place
model **experimental**.

This page is a contributor-oriented summary. The authoritative component description is
**[../../architecture/byoh-controller.md](../../architecture/byoh-controller.md)** — read
it first; this page adds the in-place-upgrade and guard view and links the decisions.

## Component model (summary)
CAPI management cluster ⟷ the OpenKubes-maintained **BYOH provider** ⟷ the BYOH **host
agent/installer** on each bare-metal host. CAPD (Docker infrastructure) is used only as a
local development substrate for the same CAPI management model. Full detail:
[../../architecture/byoh-controller.md](../../architecture/byoh-controller.md);
local substrate: [../../ok-capd/architecture.md](../../ok-capd/architecture.md).

## Lifecycle strategies
- **InPlaceOnly** — hosts are upgraded in place; Machines are **not** replaced.
- **RollingReplacement** — the classic CAPI replace-and-roll model.

Summary and rationale: [../../architecture/byoh-controller.md](../../architecture/byoh-controller.md)
(§Lifecycle strategies). The decision record is **ADR-Platform-048** (infrastructure
lifecycle strategies) — an **external project artifact**, not yet promoted to `docs/adr/`.

## No-Replacement safety
For BYOH, a destructive Machine replacement can mean losing a real, hard-to-reprovision
host. The **No-Replacement Safety Contract** forbids a control-plane/worker Machine DELETE
during an in-place-only BYOH lifecycle, enforced by an independent admission guard
(ValidatingWebhook, fail-closed).
- In-repository decision: [../../adr/ADR-001-byoh-no-replacement-safety.md](../../adr/ADR-001-byoh-no-replacement-safety.md).
- Broader authorization model: **ADR-Platform-047** (No-Replacement & in-place upgrade
  exceptions) — **external project artifact**, promotion to `docs/adr/` pending.

## RuntimeSDK in-place upgrade model — experimental
> **Experimental:** validated only via the local CAPD spike, **not** in production, and the
> RuntimeSDK extension and BYOH host executor are **not** yet validated end-to-end.

CAPI's RuntimeSDK lifecycle hooks (`CanUpdateMachine` / `UpdateMachine`) let an external
extension approve or decline an in-place machine update. When an update is **declined** on
a **singleton control plane at `maxSurge:0`**, KCP falls back to a scale-down — i.e. it
attempts a **control-plane Machine DELETE**. The No-Replacement guard must **deny** that
DELETE (expected verdict `PASS_DENIED`), with Machine/InfraMachine UID preserved. This is
exercised by the Step-0 decline-guard harness; see
**[testing-and-evidence.md](testing-and-evidence.md)** and
[../../../labs/byoh-inplace-spike/README.md](../../../labs/byoh-inplace-spike/README.md).

## References
- [../../architecture/byoh-controller.md](../../architecture/byoh-controller.md)
- [../../ok-capd/architecture.md](../../ok-capd/architecture.md)
- [../../adr/ADR-001-byoh-no-replacement-safety.md](../../adr/ADR-001-byoh-no-replacement-safety.md)
- ADR-Platform-047, ADR-Platform-048 — external project artifacts (promotion pending)
