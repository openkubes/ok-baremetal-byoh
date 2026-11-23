# BYOH Reproducibility & Evidence Standard

**Status:** standing engineering convention for ALL BYOH lifecycle work (CAPD spikes,
guard validation, the CAPI-driven in-place upgrade on real hosts, and beyond). **This
repository is the source of truth for the standard.** Audience: every OpenKubes
contributor must be able to reproduce any test **without knowledge of the session that
produced it.**
**Rule of record:** a failed run is **never** documented as successful. Provenance and the
actual verdict come from captured evidence, not memory.

## The four documentation layers (required for every validated step)
### 1. Developer Runbook
- Prerequisites + **exact tool versions** (go, docker, kind, kubectl, clusterctl, CAPI, cert-manager, golangci-lint, host arch).
- Exact commands, copy-pasteable.
- Installation + execution **order**.
- Cleanup and recovery (incl. partial-failure recovery and how to remove leftover admission webhooks before any teardown).
- Troubleshooting (known failure modes → cause → action).

### 2. Test Protocol (one per run, durable after a PASS)
- **Unique run-id + UTC timestamp.**
- **Git commit** (full SHA) and **image identity** (image id + tag + arch) of every component under test, plus the **code SHA-256 of every script in the tested codebase** (harness `scenario.sh`, the runner, and sourced helpers such as `lib.sh`).
- **CAPI and Kubernetes versions.**
- Steps executed.
- **Expected vs actual** result.
- **PASS/FAIL with reasoning.** A FAIL is recorded as FAIL.

### 3. Evidence Bundle (machine-readable, run-specific directory)
- apiserver **audit logs** (authoritative; windowed + full) and relevant **controller logs** / extension logs.
- **Machine / InfraMachine UIDs** (before + after).
- **Before/after snapshots** of the CAPI objects.
- **Node versions** (kubelet) before/after.
- **Image + source provenance** (image id, source commit, build args, daemon id) and **code SHAs** (`scenario_sha256`, `runner_sha256`, `lib_sha256`, …).
- **Machine-readable result files** (`run-manifest.json`, `result-*.json`, `report-*.txt`).
- **No secrets, no privileged kubeconfigs** in the bundle or in Git (enforced; see below).

### 4. Architecture Decisions
- Why each safety mechanism exists (No-Replacement Guard, single-event audit assertion, audit cutoff, registry-UID binding, `maxSurge:0` handling).
- Known limitations (e.g. the CAPD stub fakes the node upgrade; crictl same-daemon caveats).
- Failure + recovery behaviour (fail-closed everywhere; guard removed and its absence verified before any ownership-verified teardown).
- References to **ADR-Platform-047** (No-Replacement + in-place authorization) and **ADR-Platform-048** (infrastructure lifecycle strategies) — currently **external project artifacts**, not yet promoted to `docs/adr/`; the in-repository decision is [../../adr/ADR-001-byoh-no-replacement-safety.md](../../adr/ADR-001-byoh-no-replacement-safety.md).

## Conventions
- **Run-id:** reuse the harness `run.json` `run_id` when present; otherwise the UTC timestamp `YYYYMMDDThhmmssZ`. Every artifact of a run carries it.
- **Provenance (mandatory for PASS):** every run records git commit (full SHA), image id/tag/arch, CAPI version, Kubernetes version, the docker daemon id used to build/load, and the SHA-256 of every script in the tested codebase (`scenario.sh`, the runner, `lib.sh`, …). **A missing mandatory provenance field forces OVERALL=FAIL** — a run with incomplete provenance is never a PASS.
- **Evidence location:** results land automatically in a run-specific directory (`<lab>/.state/evidence-<test>-<ts>/`) written by the runner — not hand-copied.
- **Secrets hygiene:** kubeconfigs, certs, keys, tokens never enter Git or an evidence bundle. Runners actively strip secret-like files from the bundle; `.state/kube/` and any `*.kubeconfig`/`*.pem`/`*.key`/`*secret*`/`*token*` are excluded. This repository enforces it via `.gitignore` (`labs/**/.state/`, `**/*.kubeconfig`). Commit only sanitized protocols/manifests.
- **Durable protocol:** after a PASS, the run protocol + manifest are promoted to a durable location (repository docs and/or the planning project `reviews/…`), with secrets already excluded.

## RuntimeSDK in-place testing — experimental
> **Experimental:** the harness is built and self-tested; it has **not** been run
> end-to-end against a live cluster, and the RuntimeSDK extension and BYOH host executor
> are **not** yet validated.

The CAPD-backed harness lives in
[../../../labs/byoh-inplace-spike/README.md](../../../labs/byoh-inplace-spike/README.md)
(`scenario.sh`, `lib.sh`, `bootstrap.sh`, `run-step0-651a655.sh`, `test/offline-test.sh`).
The **Step-0 decline-guard** test drives a declined in-place update at `maxSurge:0` on a
singleton control plane and expects the No-Replacement guard to **deny** the resulting CP
Machine DELETE (`PASS_DENIED`). The runner auto-writes `run-manifest.json` (incl.
`scenario_sha256`, `runner_sha256`, `lib_sha256`, `missing_provenance`) and `run-protocol.md`
into `.state/evidence-step0-<ts>/`, and marks `OVERALL=PASS` only when
`scenario_rc==0 && verdict==PASS_DENIED && missing_provenance==[]`; otherwise `OVERALL=FAIL`
and it stops.

## No-Replacement Guard testing
Admission concept **validated** (see [../../adr/ADR-001-byoh-no-replacement-safety.md](../../adr/ADR-001-byoh-no-replacement-safety.md), `cmd/no-replacement-guard`). The
`make ok-capd-guard-test` wrapper is **planned** (currently a `not-yet-implemented` stub);
guard scenarios run today only via the experimental harness above.

## Offline tests (CI-safe)
[../../../labs/byoh-inplace-spike/test/offline-test.sh](../../../labs/byoh-inplace-spike/test/offline-test.sh)
is a pure-logic test suite (41 cases: poll/audit classification, fallback binding,
provenance gating, ownership, verdict mapping). It needs no cluster and is safe for CI.
