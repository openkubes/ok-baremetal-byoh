# BYOH In-Place Upgrade — Disposable CAPI Validation Spike (Mode A), hardened

Proves, on a throwaway cluster, that a **singleton KubeadmControlPlane** + a **MachineDeployment**
do a **CAPI-consistent in-place version update with no Machine replacement**, and — the first
acceptance gate — that a **declined** in-place update **cannot delete the singleton control-plane
Machine** in a no-spare topology.

> ⚠️ **Not executed.** Written and syntax-checked only; the review container has no
> Docker/kind/Go and blocks the registry. Run on a disposable Docker host — **never** the
> OpenKubes management Macs or Scaleway. All runtime outcomes are *to be observed by running this*.

## Capability boundary (read this)
The reused CAPI in-tree extension (`test/extension/handlers/inplaceupdate`) **fakes** the update
— its `DoUpdateMachine` "intentionally [does] not actually apply any in-place changes." So it
converges **`Machine.spec.version` (metadata)** but does **not** upgrade the real node's kubelet.
This spike therefore proves the **CAPI control loop** (no replacement, convergence of CAPI
objects, hook invocation/retry, safe failure handling) — **not** a real Kubernetes node upgrade.
`assert.sh` records workload Node `kubeletVersion` before/after precisely to show it is unchanged,
and never reports Machine-metadata convergence as node convergence. A real node upgrade is the job
of the later BYOH executor, validated in a follow-up spike.

## Files
- `plan.sh` — dry run: prints exact clusters/containers/images and the cleanup boundary (no changes).
- `bootstrap.sh` — isolated kind + CAPI (gates on) + **pinned+checksummed** cert-manager +
  test-extension (source/image identity verified), fail-closed; writes `.state/run.json`
  (fresh RUN_ID invalidates prior approvals) and an ownership ConfigMap.
- `scenario.sh <decline|accept|update-failure|controller-restart>` — one scenario, fresh
  ownership-stamped workload; decline runs a **continuous CP-deletion audit**; deletes are
  ownership-verified and fail closed.
- `assert.sh` — complete assertions; decline verdict comes from `classify.sh` (audit-based), not
  snapshot equality; stamps run provenance into `result-*.json`.
- `classify.sh` — **pure, file-driven** verdict + provenance logic (no cluster access).
- `test/offline-test.sh` — runnable unit tests for `classify.sh` (**41 cases**; `bash test/offline-test.sh`).
- `teardown.sh` — fail-closed, ownership-verified deletion only. `lib.sh` — isolation/guards/observer/audit.
  `kind-mgmt.yaml`, `extensionconfig.yaml`, `audit-policy.yaml`.

## Evidence model (r4) — authoritative audit, not polling
The **apiserver audit log** is the authoritative record of DELETE *requests* (including denied
ones). `bootstrap.sh` enables it via a kubeadm patch + `audit-policy.yaml` and **proves it is
active end-to-end** (a probe configmap delete must appear in the log) or fails closed. The audit
log is read portably via `docker exec` (works on Linux and Docker Desktop). Polling of Machine
state is **corroboration only** — it can positively detect a deletion but can **never** prove "no
DELETE was requested"; every failed observation is an error sample → `INCOMPLETE`.

Audit DELETE-request states: `NO_REQUEST` · `REQUESTED_SUCCEEDED` (2xx) · `REQUESTED_DENIED`
(403/Forbidden) · `REQUESTED_OTHER` (requested but neither — 409, 5xx, in-flight) · `INCOMPLETE`.
A DELETE request is counted at **any** audit stage, so an in-flight delete can't read as
`NO_REQUEST`; the collector also reads rotated backups.

Decline verdicts:
`PASS_NO_DELETE_REQUESTED` (audit shows no CP DELETE requested **and** fallback exercised) ·
`PASS_DENIED` (DELETE requested but admission-denied — only with a webhook) ·
`FAIL_DESTRUCTIVE` (DELETE succeeded/completed) · `FAIL_DELETE_ATTEMPTED` (DELETE requested but
not denied/completed) · `FAIL_NOT_EXERCISED` (KCP never engaged the rollout for this CP Machine) ·
`FAIL_INCOMPLETE` (any audit/observer gap → fail closed).

**Claim boundary:** a PASS evidences *"CAPI did not request a CP delete in this run"* — observed
behavior, **not** a guarantee that *"deletion cannot occur."* Only an admission webhook with
`failurePolicy=Fail` provides prevention (assessed in the r2 report, still unbuilt).

## Required pins (bootstrap fails closed without them)
`CERT_MANAGER_VERSION`, `CERT_MANAGER_SHA256`, and either a prebuilt `EXT_IMAGE`
(optionally pinned via `EXT_IMAGE_DIGEST`) or `CAPI_SRC` (a cluster-api checkout whose
`git describe` equals `CAPI_VERSION`).

## Isolation & safety (how the 9 requirements are met)
1. **kubeconfig isolation** — everything uses `$SPIKE_HOME/.state/kube/mgmt.kubeconfig`; `kind`
   writes with `--kubeconfig`; `kubectl`/`clusterctl` are wrapped (`k`, `cctl`) to that file.
   `~/.kube/config` is never read or written; no `kubectl config use-context`.
2. **reproducible bootstrap** — cert-manager, CAPI gate verification, image build/load,
   extension deploy, and ExtensionConfig `Discovered=True` are each validated; any gap aborts.
3. **independent scenarios** — each scenario builds a **fresh workload Cluster** (`ipw-<scn>`);
   none reuses an upgraded cluster; decline must pass before others.
4. **safe teardown** — deletes CAPI Cluster objects (CAPD reaps containers by ownerRef) then the
   named kind cluster; leftover CAPD containers are **listed, never force-deleted by prefix**.
5. **complete assertions** — UIDs, convergence, Node versions, hook logs, retry, conditions,
   create/delete **events**, unpaused final state.
6. **safety first** — `scenario.sh accept` refuses to run until `decline` has PASSED.
7. **capability boundary** — asserted explicitly (above).
8. **admission protection** — assessed in the offline report (`reviews/...validation-spike-r2.md`),
   not implemented.
9. **operator safety** — `./plan.sh` shows exactly what will be created/deleted before anything runs.

## Run
```bash
bash test/offline-test.sh       # validate the verdict logic offline (no cluster) — 19/19
./plan.sh                       # review the exact footprint first
CERT_MANAGER_VERSION=v1.16.2 CERT_MANAGER_SHA256=<sha> \
  CAPI_SRC=/path/to/cluster-api@v1.14.4 ./bootstrap.sh        # [--reuse] only for a spike-owned cluster
./scenario.sh decline           # ACCEPTANCE GATE — must PASS (bound to this run's RUN_ID)
./scenario.sh accept
./scenario.sh update-failure
./scenario.sh controller-restart
./teardown.sh
```
`BASE_VERSION`/`TARGET_VERSION`/`EXT_IMAGE`/`EXT_IMAGE_DIGEST`/`CAPI_SRC` are overridable (see `lib.sh`).
A new `bootstrap.sh` run mints a new RUN_ID and deletes prior `result-*.json`, so stale decline
approvals can never gate a later run.
