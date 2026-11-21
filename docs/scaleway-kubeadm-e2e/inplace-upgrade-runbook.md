# Scaleway BYOH in-place upgrade runbook

> **Maturity: PLANNED — not yet executed under CAPI control.** This runbook describes the
> intended **CAPI / RuntimeSDK-driven** in-place upgrade. The RuntimeSDK extension and the
> BYOH host executor are **not** yet validated end-to-end. Do not treat any step here as a
> proven production procedure.

This runbook complements the existing upgrade automation baseline
([upgrade-make-runbook.md](upgrade-make-runbook.md)) and must be read together with the
testing/evidence rules in
[../development/byoh/testing-and-evidence.md](../development/byoh/testing-and-evidence.md).

## Two clearly separate things
### A. Historical manual upgrade (2026-11-19) — validated, disposable lab
A manual upgrade of the two Scaleway hosts from **v1.36.6 → v1.37.2** was performed on
2026-11-19 with **CAPI paused**: CP `kubeadm upgrade apply`, worker `kubeadm upgrade node`,
apt `v1.36 → v1.37`, kubelet/kubectl. **CAPI metadata deliberately stayed at v1.36.6**
(KCP `spec.version`, MachineDeployment template, existing Machines), and **no etcd backup**
was taken (disposable test). Full record:
[upgrade-1.37.md](upgrade-1.37.md). This is the **reference procedure / executor basis** —
it is history, not the CAPI-driven flow below.

### B. Planned CAPI / RuntimeSDK-driven upgrade — not yet implemented
The target flow performs the same on-host upgrade **under CAPI control** via the RuntimeSDK
in-place hooks, keeping CAPI metadata and the real node `kubeletVersion` in sync, while the
No-Replacement guard prevents any Machine replacement. **This is planned**; the extension
and host executor are **experimental/unvalidated** (see
[../development/byoh/architecture.md](../development/byoh/architecture.md) §RuntimeSDK
in-place upgrade model).

## Mandatory safety gates (for the planned flow)
- **etcd backup is mandatory** before any control-plane upgrade (the historical lab skipped
  it; the CAPI-driven flow must not).
- Protected clusters are never touched; no unprotected decline on a singleton control plane.
- **Machine / InfraMachine / host identity preserved** (UIDs unchanged) across the upgrade.
- **Real node `kubeletVersion` change evidenced** before/after, and CAPI metadata ↔ node
  version reconciled (no drift like the historical lab's intentional v1.36.6 metadata).
- Fail-closed throughout; a failed run is recorded as FAIL, never PASS.

## Evidence (per [../development/byoh/testing-and-evidence.md](../development/byoh/testing-and-evidence.md))
Full before/after evidence bundle: etcd backup reference, CAPI object snapshots, Machine/
InfraMachine UIDs, kubelet versions, audit/controller logs, provenance, and a machine-
readable result — with secrets and kubeconfigs excluded. The durable per-run result is
recorded in [inplace-upgrade-testprotocol.md](inplace-upgrade-testprotocol.md) **only after
a successful real upgrade**.

## References
- Historical manual upgrade: [upgrade-1.37.md](upgrade-1.37.md)
- Upgrade automation baseline: [upgrade-make-runbook.md](upgrade-make-runbook.md)
- Lifecycle Make E2E: [make-lifecycle-runbook.md](make-lifecycle-runbook.md)
- Testing & evidence: [../development/byoh/testing-and-evidence.md](../development/byoh/testing-and-evidence.md)
- No-Replacement contract: [../adr/ADR-001-byoh-no-replacement-safety.md](../adr/ADR-001-byoh-no-replacement-safety.md); ADR-Platform-047 / ADR-Platform-048 — external project artifacts (promotion pending)
