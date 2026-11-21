# BYOH local development (CAPD)

**Maturity: validated** (local CAPD management + workload clusters).

This is the fast path to a working local Cluster API + CAPD management cluster for BYOH
development. It does **not** repeat the full walkthrough — for Cluster API background,
pinned versions, prerequisites, and the per-target internals see
**[../../ok-capd/tutorial.md](../../ok-capd/tutorial.md)**.

## Prerequisites
Docker (Colima on macOS), `kubectl`, `clusterctl`, `kind`, Go — exact versions and the
macOS/Colima limits are documented in
[../../ok-capd/tutorial.md](../../ok-capd/tutorial.md) (§3 Prerequisites).

## Developer command path
All targets are defined in the repository `Makefile` (`##@ Local CAPD …`). Each is
idempotent and isolated to the dedicated CAPD Kind cluster; none touches a production or
protected cluster.

```bash
make ok-capd            # create/verify the isolated local CAPI + CAPD management cluster
make ok-capd-status     # read-only status of the CAPD management cluster
make ok-capd-test       # verify CAPI/CAPD readiness + Docker socket access (smoke test)
make ok-capd-workload-render   # render the pinned CAPD workload template (no cluster writes)
make ok-capd-workload-up       # create/verify the disposable CAPD smoke workload cluster
make ok-capd-workload-status   # read-only status of the smoke workload cluster
make ok-capd-workload-diagnose # read-only diagnostics → private report
make ok-capd-report     # read-only report of CAPD workload clusters
make ok-capd-workload-down     # delete ONLY the smoke workload cluster (explicit confirm)
make ok-capd-clean      # delete ONLY the isolated CAPD Kind cluster (explicit confirm)
```

See [../../ok-capd/tutorial.md](../../ok-capd/tutorial.md) §2.5 for exactly what each
target does, and [../../ok-capd/troubleshooting.md](../../ok-capd/troubleshooting.md) for
Docker-daemon / kubeconfig / provider-inventory issues.

## In-place upgrade & guard testing — experimental / planned
> **Not yet implemented as Make targets.** `make ok-capd-inplace-test` and
> `make ok-capd-guard-test` are **planned** and currently resolve to
> `dev/ok-capd/scripts/not-yet-implemented.sh` (they intentionally do nothing).

The actual RuntimeSDK in-place and No-Replacement guard work currently lives in the
**experimental** harness **[../../../labs/byoh-inplace-spike/README.md](../../../labs/byoh-inplace-spike/README.md)**
(the CAPD-backed Step-0 decline-guard spike). How it is run and evidenced is described in
**[testing-and-evidence.md](testing-and-evidence.md)**. Nothing there is a validated,
production lifecycle operation yet.

## Safety
The local CAPD environment is isolated (dedicated Kind cluster, isolated kubeconfig,
ownership-/label-scoped teardown). Protected clusters are never created, used, or deleted.
Cleanup targets delete only the disposable CAPD cluster and require explicit confirmation.
