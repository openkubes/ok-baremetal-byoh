# OpenKubes BYOH — Developer Documentation

Entry point for contributors working on the OpenKubes **Bring-Your-Own-Host (BYOH)**
provider, its local CAPD development environment, the RuntimeSDK in-place upgrade
work, and the No-Replacement safety guard.

> **Maturity labels** used throughout these docs:
> **validated** — reproduced and verified · **experimental** — built and self-tested,
> not yet run end-to-end on a live target · **planned** — designed, not implemented.

## Start here
1. **[local-development.md](local-development.md)** — stand up a local CAPD management
   cluster with `make ok-capd` and run the smoke/workload targets.
2. **[architecture.md](architecture.md)** — the BYOH component model, lifecycle
   strategies, No-Replacement safety, and the (experimental) RuntimeSDK in-place model.
3. **[testing-and-evidence.md](testing-and-evidence.md)** — the full Reproducibility &
   Evidence Standard (this repository is the source of truth), plus how RuntimeSDK and
   guard work is tested.

## Capability maturity (overview)
| Area | Maturity | Where |
|---|---|---|
| Local CAPD management + workload clusters | **validated** | `make ok-capd*`, [../../ok-capd/tutorial.md](../../ok-capd/tutorial.md) |
| No-Replacement admission guard (design + admission concept) | **validated** | [../../adr/ADR-001-byoh-no-replacement-safety.md](../../adr/ADR-001-byoh-no-replacement-safety.md), `cmd/no-replacement-guard` |
| RuntimeSDK in-place upgrade harness (CAPD Step-0 decline-guard) | **experimental** | [../../../labs/byoh-inplace-spike/README.md](../../../labs/byoh-inplace-spike/README.md) |
| `make ok-capd-inplace-test` / `make ok-capd-guard-test` | **planned** (stubs) | `Makefile` (`not-yet-implemented.sh`) |
| CAPI/RuntimeSDK-driven Scaleway in-place upgrade | **planned** | [../../scaleway-kubeadm-e2e/inplace-upgrade-runbook.md](../../scaleway-kubeadm-e2e/inplace-upgrade-runbook.md) |
| Manual Scaleway v1.36.6→v1.37.2 upgrade (2026-11-19) | **validated (historical, disposable lab)** | [../../scaleway-kubeadm-e2e/upgrade-1.37.md](../../scaleway-kubeadm-e2e/upgrade-1.37.md) |

## Related existing documentation (not duplicated here)
- CAPD tutorial & architecture: [../../ok-capd/tutorial.md](../../ok-capd/tutorial.md), [../../ok-capd/architecture.md](../../ok-capd/architecture.md), [../../ok-capd/troubleshooting.md](../../ok-capd/troubleshooting.md)
- BYOH controller architecture: [../../architecture/byoh-controller.md](../../architecture/byoh-controller.md)
- Scaleway kubeadm E2E master plan: [../../scaleway-kubeadm-e2e/README.md](../../scaleway-kubeadm-e2e/README.md)

## Architecture Decision Records
- **In repository:** [ADR-001 — BYOH No-Replacement Safety Contract](../../adr/ADR-001-byoh-no-replacement-safety.md).
- **External project artifacts (not yet in `docs/adr/`):** **ADR-Platform-047** (No-Replacement &
  in-place upgrade exceptions) and **ADR-Platform-048** (infrastructure lifecycle strategies)
  currently live in the OpenKubes planning project. Their promotion into `docs/adr/` is a
  **separate, pending step**; until then they are referenced as external decisions, not as
  repository links.
