# Scaleway CAPI-driven in-place upgrade — Test Protocol

> **Status: PENDING — placeholder only.** Do **not** populate this file until a real
> CAPI / RuntimeSDK-driven in-place upgrade has **succeeded**. A failed or not-yet-run
> upgrade is never recorded here as a result.

This protocol will be filled from the captured evidence of the first successful run, per
[../development/byoh/testing-and-evidence.md](../development/byoh/testing-and-evidence.md)
(Layer 2 — Test Protocol). The runbook is
[inplace-upgrade-runbook.md](inplace-upgrade-runbook.md).

## To be filled after a successful upgrade (template)
- [ ] **Run-id + UTC timestamp**
- [ ] **Git commit** (full SHA) + **image identity** (id + tag + arch) of every component under test
- [ ] **Code SHA-256** of every script in the tested codebase
- [ ] **CAPI version** and **Kubernetes** before → after
- [ ] **etcd backup** reference (mandatory; taken before the CP upgrade)
- [ ] **Machine / InfraMachine / host identity** UIDs — before == after (no replacement)
- [ ] **Node `kubeletVersion`** before → after (real change evidenced on each node)
- [ ] **CAPI metadata ↔ node version** reconciled (no drift)
- [ ] **No-Replacement guard** behaviour during the upgrade (no Machine DELETE succeeded)
- [ ] Steps executed · **expected vs actual**
- [ ] **PASS/FAIL with reasoning** (a FAIL is recorded as FAIL)
- [ ] Evidence bundle path (secrets/kubeconfigs excluded)

_Nothing above is filled yet — the upgrade has not been run._
