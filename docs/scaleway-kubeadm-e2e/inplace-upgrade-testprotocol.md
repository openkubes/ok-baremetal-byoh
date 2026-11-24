# E2E lab CAPI-driven in-place upgrade — Test Protocol

> **Status: PASS (2026-11-24).** First real CAPI / RuntimeSDK-driven in-place Kubernetes upgrade of BYOH hosts:
> the five-host lab cluster `ha1` went from **v1.36.6 to v1.37.2** without replacing a Machine, ByoMachine, ByoHost,
> Node or VM. Isolated DEV/E2E lab. **This is not a statement about production readiness**: the security review of
> the guard is NO-GO for production (see [Known limitations](#known-limitations-and-open-findings)).

Protocol per [../development/byoh/testing-and-evidence.md](../development/byoh/testing-and-evidence.md) (Layer 2).
Runbook: [../inplace-upgrade.md](../inplace-upgrade.md). Plan that was executed, with the deviations of the run:
[inplace-upgrade-ha1-rollout.md](inplace-upgrade-ha1-rollout.md).

## Run
- **Run-id:** `b10.24-ha1-20261124`; window 2026-11-24 12:25Z (controller update) to 19:29Z (final checks).
- **Cluster:** `ha1/ha1`, Cluster UID `414317b7-77d4-4bd0-b86e-5cbe38b6c2c7`; 3 control planes with stacked etcd
  (`ha1-cp-1..3`), 2 workers (`ha1-w-1`, `ha1-w-2`), Ubuntu 24.04.5, kernel 6.8.0-147, containerd 2.3.7.
- **Management:** kind cluster `ok-lab-mgmt` on the lab gateway; operator tooling of the lab repository
  (pinned, independently rebuilt artifacts; typed confirmations; one approval per step and per host).

## Versions and identities of the components under test

| Component | Version / commit | Identity |
|---|---|---|
| Kubernetes | v1.36.6 -> v1.37.2 | pinned debs `kubeadm`/`kubelet`/`kubectl` 1.37.2-1.1; package lock `config/inplace/base/packages-v1.37.2.json` sha256 `e574496a2057fa767a5ad93dc08edeb66d0c48c597f8a25fc3b506acc94a3350` |
| Cluster API | v1.14.4 | feature gates `RuntimeSDK`, `InPlaceUpdates` on the core controller, `InPlaceUpdates` on the KCP controller |
| BYOH controller | `28fd408738b6b563080930fbe0d5bb3765456e42` | `ghcr.io/openkubes/byoh-controller@sha256:a8ab6bea5a7358c7c74ad0ec70c57aeed7ce0c33137a9e35d794458d4b4f22ef` (linux/amd64) |
| BYOH host agent | `cdf534b7e2369f58bb068ecc20b18b934e273d3b` (from host 2 on; `28fd408` for the failed attempt 0 on `ha1-cp-1`) | binary sha256 `1ab14bbd7f0ebac4eb556ba234699e178c2194313730b9572e340945a289a120` |
| In-place extension | `7532e79886e15c06fe867d8d6b8b1a958e4e8f13` for the workers; `14f549d79c6aa4d2be8326c94323845b634d7c88` for the control planes | `inplace-extension@sha256:5f2cc8e9507e168360e5ef86311ce134db115fe03da01136409e5e954aa978d6`; `...@sha256:52b9e329cf7bf3b5b68f6b8f9ec825b3c81686dd8361435c10fc12ca1fe3658a` |
| No-Replacement guard | `7cac257ab5ec382d7f6a1d2cce8d5763794e7f95` for the workers; `4fa332e6243bdd4597486f3aaec8d650241ea041` before | `no-replacement-guard@sha256:e9a601dee4afd5b36f4f65c10c220dbb41c5fb9a4ede908082148b942453aef8`; `...@sha256:f456774d8d6485a8239f436fcaa0459437db72844bd59d7c0b1fffec9a4c1b1d` |
| CNI | Cilium 1.21.0-pre.4 (pre-release, accepted for the lab only), upgraded from 1.20.5 before the run | agent `quay.io/cilium/cilium@sha256:15ae104d770b0687baa0f3753ed92185744abe77ea4012e2b2f285c97595be86` |
| etcd | 3.6.8-0 -> 3.7.0-0 (by kubeadm) | member IDs `7ececafc36c9587b`, `8841bf7bdbb968e8`, `f42d91ae837569e9` before and after |

Every image and the agent binary were rebuilt independently from `git archive <commit>` and compared bit for bit
with the published artifact before use (build provenance REPRODUCED in the lab's lock files; the artifacts are not
signed). The code SHA-256 of each script is not listed here: the scripts are identified by the commits above
(`hack/inplace` of this repository at the extension/guard commits, and the lab repository's `main`).

## etcd backups (mandatory before the control-plane upgrade)
Snapshots taken on `ha1-cp-1`, copied off-host, each with matching sha256 on host and operator machine and an
isolated restore into a throw-away directory with the etcd image(s) running in the cluster:

| UTC | Taken before | sha256 |
|---|---|---|
| 14:46 | readiness check | `645b953919685885d1c8fc572597e09554e029f8503d8a38914595da748b8d8d` |
| 15:02 | KCP version patch | `ad03b58669de55a72564ccf0f7fd520937f50656674f660ba8fc5ee06fbd39de` |
| 16:40 | `ha1-cp-3` (after `ha1-cp-1`; restore verified with 3.7.0-0 and 3.6.8-0) | `ee5f696e225e18e8684f8d2af55cb5b1bfc3358ef9b0e4556f9f946f28ec4462` |
| 16:51 | `ha1-cp-2` (restore verified with both versions) | `06fa1081773040c5f0268453fc44a1b2e5ba2325bbf1629b1ebe985ee05ceca9` |

A restore into the running cluster was not exercised.

## Identities: before == after (no replacement)
`hack/inplace/uids.py compare <baseline taken before the first change> <final> --expect-version v1.37.2`: **PASS**,
all 20 UIDs preserved. The comparison also passed after every single step of the run.

| Host / Node | Machine (name, UID) | ByoMachine UID | ByoHost UID | Node UID |
|---|---|---|---|---|
| ha1-cp-1 | `ha1-control-plane-nt28b` `e7520154-3f40-474e-8648-6d60f215b734` | `765f01cd-56be-433b-a194-5686ea18e230` | `156fa7a7-6f63-441f-a18a-49fdf6f54c78` | `918fa75c-2a06-488f-9599-24e9cec06b07` |
| ha1-cp-3 | `ha1-control-plane-77sx8` `f6f6f0d9-a197-4334-87f3-6ffadba2fc29` | `7a502c8e-cf39-4507-9693-f749738002df` | `7ab21b7e-7768-4656-8e80-4826285cd1c2` | `d32b3f28-81d4-4a09-8913-3ac2a73a1a27` |
| ha1-cp-2 | `ha1-control-plane-58967` `2fa4337b-3bf8-4e42-8dfb-e77833815e33` | `cdf4126e-5b18-459d-a5c3-9dd8147031ae` | `c6fbe75d-5d55-431b-937b-e114f91b4dcf` | `ee2b9ef7-e4f0-4c3a-9e53-02d59ea29e12` |
| ha1-w-1 | `ha1-md-0-frlf2-4725m` `7c6292d7-ef7f-4bbf-a4ba-21eb367351e2` | `17b856fa-ea48-4af8-be1c-f5999cf392db` | `2f508a04-133c-4cab-b34c-6f18ce37875e` | `beacff11-25da-44cf-a770-de41689c46ae` |
| ha1-w-2 | `ha1-md-0-frlf2-5dqc9` `a80b1c60-eb9b-4c68-aeab-00c30aa903aa` | `07ac6890-7e35-47ca-ac43-c181074b6dbc` | `3fd7d785-3a0b-4ab2-9d9b-473589a63731` | `5c01b9a4-232c-44ea-9994-4179118b45a9` |

Node `kubeletVersion` and Machine `spec.version` are v1.37.2 for all five; Machine names of the workers still carry
the name of the first MachineSet (`ha1-md-0-frlf2-*`): the Machines were moved to the new MachineSet, not recreated.

## Steps: expected vs actual

| # | Step | Expected | Actual |
|---|---|---|---|
| 1 | KCP `spec.version` -> v1.37.2 under a claimed grant (15:03:08Z) | guard admits; Cluster API triggers the in-place update of the oldest control plane; extension waits for the host approval | guard admitted, Cluster API chose `ha1-cp-1`; **UpdateMachine failed** on every call: the extension required a Machine UID that Cluster API does not send. No host touched. Fixed (#34), extension rolled out, the hook then waited for the approval |
| 2 | `ha1-cp-1`, role ControlPlaneFirst (approval 15:48:52Z) | drain, `kubeadm upgrade apply`, verify | **attempt 0 failed** at step Kubeadm: relative package path for dpkg in a transient systemd unit; nothing installed. Fixed (#35), agent updated, attempt raised to 1: resumed from the checkpoint, Succeeded 16:23:08Z, hook done 16:23:09Z |
| 3 | `ha1-cp-3` (approval 16:42:48Z) | `kubeadm upgrade node`, verify | Succeeded 16:47:16Z, hook done 16:47:31Z, one attempt |
| 4 | `ha1-cp-2` (approval 16:56Z) | as 3; cluster-wide addons with the last control plane | Succeeded 17:01:57Z, hook done 17:02:08Z; kube-proxy v1.37.2 and CoreDNS v1.14.6 rolled out by kubeadm |
| 5 | MachineDeployment template version -> v1.37.2 under a claimed grant (17:08:50Z) | in-place move of the workers to the new MachineSet | **extension declined** the MachineSet (template kind `ByoMachineTemplate`); Cluster API fell back to replacement and tried to DELETE a worker Machine for 54 minutes; **the guard denied every attempt**. Fixed (#36); the core controller had to be restarted (it caches the hook answer for 24 h); then the Machine of `ha1-w-1` was moved with the same UID |
| 6 | label re-alignment of the moved worker's ByoMachine | admitted | **denied by the guard**, which blocks the new MachineSet's reconcile. Guard fixed with a narrow rule bound to the move (#37); Cluster API then re-aligned the labels by itself |
| 7 | `ha1-w-1`, role Worker (approval 19:17:31Z) | drain, `kubeadm upgrade node`, verify; then move of `ha1-w-2` | Succeeded 19:18:42Z (30 s), hook done 19:18:57Z; `ha1-w-2` moved and its ByoMachine labels admitted at once |
| 8 | `ha1-w-2` (approval 19:23:24Z) | as 7 | Succeeded 19:23:52Z (19 s), hook done 19:24:04Z; `host-step.sh next`: COMPLETE; the old MachineSet was removed by Cluster API (empty) |

After each host: `hack/inplace/host-step.sh verify v1.37.2 <host>` PASS (agent Succeeded, Node Ready at the target,
Machine no longer updating, static pods at the target on control planes, all Nodes Ready, etcd 3/3 with the original
member IDs, CNI and CoreDNS ready, load balancer with 3 active backends, no identity added, removed or replaced), and
the connectivity and NetworkPolicy check `netcheck.sh` 70/70.

## No-Replacement guard during the run
- No Machine DELETE or CREATE succeeded. The guard denied the replacement attempts of step 5 (every DELETE of
  `ha1-md-0-frlf2-4725m` from 17:08:51Z to 18:02:26Z) and the manual negative checks.
- `hack/inplace/negative-tests.sh` (server dry-runs only) at the end: 16 of 16 denied by the guard, with the test
  fixes of #39 (14 of 16 with the script as it was during the run; the two others could not reach the guard).
- The guard also blocked a legitimate write (step 6). That was a defect of the guard, fail-closed.

## API availability
`/readyz` through the load balancer, one probe per second with a 3 s timeout, 14:57Z to 19:52Z: 13 single failed
probes. 11 during the three control-plane upgrades (cp-1: 1, cp-3: 4, cp-2: 6), 2 at 18:40:54Z and 18:43:17Z while no
host was being upgraded (not investigated), 0 during the worker upgrades. No availability objective is derived
from this.

## Defects found by the run
All of them passed the unit and envtest suites before; only the live run showed them.

| Component | Defect | Fix |
|---|---|---|
| extension | UpdateMachine required the Machine UID, which Cluster API strips from the hook request | #34 |
| agent | relative package path given to dpkg in a transient systemd unit (working directory `/`) | #35 |
| extension | CanUpdateMachineSet compared the template kind `ByoMachineTemplate` with `ByoMachine` | #36 |
| guard | denied the MachineSet label re-alignment of a moved ByoMachine | #37 |
| agent | patched the ByoHost itself after the step reports (denied for a status-only host identity; no effect on the upgrade) | #40 |
| tooling | netcheck readiness probe; drainer `verify` query; grant name for a second window; two stale negative tests | #33, #39 |
| CI | three envtest suites (`admissiontest`, `hardeningtest`, `drainertest`) were skipped by a substring skip pattern | #38 |

## Known limitations and open findings
- **Not production-ready.** The guard's security review (two rounds) is NO-GO for production; the deferred hardening
  (atomic serialization of hosts, status RBAC, replay, admission hardening) is not part of this result.
- Findings of the independent review of #37 that predate it: under a MachineDeployment grant the update-in-progress
  toggle of a ByoMachine may carry arbitrary label and annotation changes; a grant's `machineUID` is not enforced on
  the ByoMachine paths; grants are not consumed per request; ByoMachine subresource updates and grant-less dry-runs
  are allowed; no check of the requesting user on the ByoMachine paths. None of them permits deleting, replacing
  or re-binding a Machine or a host.
- The guard does not log admitted or denied ByoMachine updates. MachineSet DELETE is not guarded.
- Approver and initiator of the grants were one operator with two ServiceAccounts (separation technical, not
  personal). The host approval is typed by the operator in a terminal.
- The load-balancer check is event-based (last UP/DOWN event), not a live backend state.
- Rollback does not exist in place; a restore of etcd into the running cluster was not exercised.
- The upgrade executor supports Debian/Ubuntu (`dpkg`, `apt`) only.
- Decommission of a protected namespace and raising `maxSurge` again were not exercised.

## Verdict
**PASS** for the stated scope: Cluster API v1.14.4 drove an in-place upgrade v1.36.6 -> v1.37.2 of three stacked-etcd
control planes and two workers on existing BYOH hosts, with every Machine, ByoMachine, ByoHost and Node identity
preserved, each host after an explicit operator approval, and with the No-Replacement guard denying every replacement
that Cluster API attempted while the extension was defective. Four product defects had to be fixed during the run;
the result therefore describes the commits listed above, not the ones the run started with.

## Evidence
Kept by the lab operator outside Git (no secrets or kubeconfigs in it; etcd snapshots are stored separately):
`evidence-b10.24/` with one directory per step (`b1-baseline` ... `d13-final`, `d14-cleanup`), `FINAL-SUMMARY.md`,
`RECOVERY-CASE-3-machineset-move.md`, `d1-kcp-patch/TOOLING-DEFECTS.md`, the independent review of #37 in
`d8-guard-byomachine-labels/`, and the CI proof for #38 in `d9-ci-envtest-suites/`.
