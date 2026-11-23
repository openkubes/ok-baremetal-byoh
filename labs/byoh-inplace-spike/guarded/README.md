# Guarded CAPD In-Place E2E (Phase 0)

Proves the **guarded** CAPI in-place lifecycle on a disposable kind/CAPD cluster:
MY No-Replacement guard + MY RuntimeSDK extension + the ADR-047 grant (with atomic
claim) protect a KCP and a MachineDeployment in-place version update with **no Machine
replacement**, and the negative/`PASS_DENIED` cases are re-proven.

Reuses the vanilla spike's helpers (`../lib.sh`) and patterns **without modifying them**;
runs on a **distinct** disposable cluster with its own isolated state + kubeconfig.

## Status (honest)
- Scripts are **syntax-checked only** (the preparation environment has no Docker/kind/Go).
  The first run is **diagnostic**: it validates preflight + bootstrap + guard/extension
  deploy + workload provisioning + guard activation, and captures evidence for the
  in-place phases. We then **iterate from the real logs** (expected: the guard's
  allow-rules vs. live CAPI Machine traffic will need tuning — that is a goal of the run).
- **Scope:** CAPD infra is `DockerMachine`, so this proves the **Machine / MachineSet /
  grant / no-replacement / CAPI in-place control loop**. The **ByoMachine-specific**
  transitions cannot be exercised with Docker infra and are deferred to the BYOH
  executor spike (real infra). The ByoMachine webhook is deployed but never matches here.

## Prerequisites (on a DISPOSABLE Docker host — never production/managed infrastructure (e.g. the mgmt Macs or a cloud provider))
- Docker (e.g. colima) with an **explicit** context, `kind`, `kubectl`, `clusterctl`,
  `jq`, `yq`, `python3`, `curl`. Go is **not** needed on the host — the image Dockerfiles
  build with a `golang:1.26` builder stage.
- A pinned cert-manager version + its manifest sha256.

## Dedicated colima profile (required — keeps existing clusters untouched)
The guarded E2E runs on its **own, empty** colima profile = a **separate Docker daemon**,
so any existing profile (e.g. `ok-colima-capi-profile` and its kind clusters) is never
touched. The preflight **refuses** to run on a daemon that hosts any foreign kind cluster.

```
# 1. Create & start a DEDICATED, empty colima profile (its own Docker daemon)
colima start --profile okx-guarded --cpu 4 --memory 8 --disk 60 --kubernetes=false

# 2. Select its docker context (colima names it 'colima-okx-guarded')
export DOCKER_CONTEXT=colima-okx-guarded
docker context show      # -> colima-okx-guarded
kind get clusters        # -> (empty)   [queries THIS daemon only]
```
The existing `ok-colima-capi-profile` and its five clusters live on a different daemon and
are invisible here. Nothing you do in this profile can affect them.

## Preflight (READ-ONLY — creates nothing)
```
DOCKER_CONTEXT=colima-okx-guarded ./preflight.sh
```
Verifies: required tools; explicit docker context; **local** docker endpoint (no remote);
the kind name `ok-byoh-guarded` is **new** (refuses to overwrite); no protected-name
collision; isolated kubeconfig (+ records `~/.kube/config` checksum to prove it is
untouched); no external-infrastructure credentials; Dockerfiles present. Exits non-zero on any FAIL.

## Run
```
DOCKER_CONTEXT=colima-okx-guarded \
CERT_MANAGER_VERSION=v1.16.2 \
CERT_MANAGER_SHA256=<sha256-of-that-cert-manager.yaml> \
./e2e.sh
```
Phases: preflight → bootstrap (kind+CAPI+cert-manager, build+load MY guard+extension,
deploy extension) → scenarios (provision workload, **activate guard after provisioning**,
grant+claim, **KCP accept**, **MD accept**, **PASS_DENIED + negatives**) → evidence report.

Override the cluster name with `GUARDED_CLUSTER=<name>` (must be new).

## Evidence (written under `.state-guarded/evidence/`)
- `result.json` — per-phase PASS/FAIL (provision, activate, kcp_grant, kcp_accept,
  md_grant, md_accept, pass_denied, neg_missing_grant, neg_foreign_requester, neg_single_claim).
- `uids-before.json`, `uids-after-kcp.json`, `uids-after-md.json` + `uidcmp-*.json` —
  Machine / DockerMachine / Node **UID preservation** before/after each accept.
- `guard-*.log` — guard admission decisions per phase. `audit-final.log` — apiserver
  DELETE audit (PASS_DENIED). `snap-*.json`, `events-*.json`, `nodes-*.json`.
- `EVIDENCE.md` — assembled report.

## Teardown (ownership-verified)
```
./teardown-guarded.sh
```
Deletes ONLY the kind cluster whose spike ownership marker == this run's RUN_ID.
Afterwards you may remove the whole dedicated profile if you wish (your call, optional):
`colima delete --profile okx-guarded`. Existing profiles/clusters are never affected.
