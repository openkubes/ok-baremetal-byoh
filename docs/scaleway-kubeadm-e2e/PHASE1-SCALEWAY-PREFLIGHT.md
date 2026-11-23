# Phase 1 — Preflight: real BYOH installer & lifecycle on `ok-bm-lab-01/02`

Status: **read-only preflight, 2026-11-21 ~22:05 UTC.**
Decision: the two existing Scaleway lab servers are **reused**; no new server is ordered.

What was done to collect this: SSH via the existing `~/.ssh/config` entries `scw1`/`scw2` (`StrictHostKeyChecking=yes`, `BatchMode`) and `kubectl get` against the mgmt cluster. There were no writes, no service actions, no `apt update`, and no Scaleway API calls. ADR-047 findings remain **OPEN**; this is not a production release.

## 1. Must stay unchanged

| Item | Fact | Rule |
|---|---|---|
| OpenTofu definition | `ok-rke2/tofu/scaleway`. Inputs are in a local, gitignored `terraform.tfvars`: project `2b7fcb3b…`, SSH key `0db33da6…`, fr-par-2, offer `EM-A116X-SSD`; created 2026-11-15. | Unchanged. No `plan`/`apply`/`destroy`. |
| OpenTofu state | The only copy is `ok-cluster/hack/labs/scaleway-baremetal/terraform.tfstate` (serial 6, OpenTofu 1.13.9). | Unchanged. A plan without this state shows 2 new servers; a plan with other inputs recreates both. |
| Other clusters / profiles | Guarded CAPD lab (`colima-okx-guarded`), `ok-colima-capi-profile`, `default` | Not touched. |
| WireGuard | The Hetzner team VPN (`../hetzner/wireguard-users`, ok-vpn). **Not** installed on `ok-bm-lab-*`; nothing links it to the BYOH path. | Not used. Coupling the lab to it would touch the Hetzner management/production network. |

## 2. Current state (verified)

### Hosts

Both are Dell PowerEdge R210 II, 8 vCPU, 31 GiB RAM, 2× 954 GB disks in md RAID (`/` 911 GB, ~1% used). OS: Ubuntu 24.04.5, kernel 6.8.0-149, amd64. Both have been up for 2 d 22 h.

| | `ok-bm-lab-01` (`scw1`) | `ok-bm-lab-02` (`scw2`) |
|---|---|---|
| Role today | **BYOH control plane** of workload `ok-byoh-scw1`: kubeadm-static etcd, apiserver, controller-manager, scheduler | **BYOH worker** (MD `ok-byoh-scw1-md-0`) |
| Kubernetes | kubelet/kubeadm **v1.36.6**, containerd 2.3.7, runc 1.5.2, crictl 1.36.0; apt repo `pkgs.k8s.io …/v1.36` (held); `byoh-kubernetes.sources.pre-v1.37` backup present | same |
| CNI | Cilium (agent, envoy, operator), `05-cilium.conflist`; kube-proxy present | Cilium agent and envoy |
| BYOH agent | `byoh-hostagent.service` **active, UnitFileState=disabled** (would not start after a reboot), `--namespace default`, WorkingDirectory `/root`, `/root/uninstall.sh` present, `/root/.byoh/config` (re-enrolled 2026-11-19), agent version `v0.0.0-unknown` | same; agent `v0.0.0-ac0c204d637f6e`; additionally `/root/byoh-containerd-recovery-20261119` |
| Agent → mgmt | kubeconfig server `https://<host>:16443` (the reverse SSH tunnel). The tunnel is **down**: there is no listener on :16443, and the last agent log line is "watch ended with error" at ~04:20 UTC on 2026-11-21. | same |
| Firewall | `ok-rke2-guard.service` (enabled). It is the `inet ok_rke2_guard` nft table from `hetzner/ok-rke2/playbooks/scaleway-guard.yml` and drops NEW traffic from non-peers on the public NIC for TCP 80, 443, 2379-2381, 4240, 6443, 9345, 10250, 30000-32767 and UDP 8472, 30000-32767. SSH stays open. ufw is inactive. | same |
| Workloads | `kube-system` only (13 pods): no user workloads, no RKE2 left (`/etc/rancher` and `/var/lib/rancher` are absent) | – |

### Workload `ok-byoh-scw1`

Read through `scw1:/etc/kubernetes/admin.conf`. Both Nodes are **Ready**, v1.36.6:

| Node | Node UID | providerID |
|---|---|---|
| `ok-bm-lab-01` | `1a1dac8e-9424-405d-a4a0-5f7ecc5daafe` | `byoh://ok-bm-lab-01/601s8j` |
| `ok-bm-lab-02` | `0acdc27d-3b46-4ded-9272-d5927e7a65be` | `byoh://ok-bm-lab-02/3fs59l` |

### Management cluster

kind **`ok-byoh-make-test`**, v1.36.4, running on colima profile **`ok-cli-kvm`** (not `ok-colima-capi-profile`). It runs:
- CAPI / KCP / CABPK **v1.14.4**
- cert-manager v1.21.3
- BYOH controller `localhost/openkubes/byoh-controller:git-cabd5c10d00b`

Feature gates are **`RuntimeSDK=false`, `InPlaceUpdates=false`**. There is no ExtensionConfig and no okx guard webhook.

Objects for Cluster `ok-byoh-scw1`: Provisioned, `Available=False`; Machines `Ready=Unknown`. The reason is that mgmt cannot reach the workload API (no tunnel/DNAT), **not** that a host failed.

### Identity bindings (intact)

| ByoHost (UID) | ← byoMachineRef (UID) | ByoMachine / Machine |
|---|---|---|
| `ok-bm-lab-01` (`cb766947-…`) | `b52fec16-e90e-42cd-9a8d-82fc8c050013` | `ok-byoh-scw1-control-plane-2tztj`, ready, providerID matches the Node |
| `ok-bm-lab-02` (`5694e102-…`) | `100c2860-4030-4fc5-b49a-8ab743c6b5f6` | `ok-byoh-scw1-md-0-9dlhs-vkdnx`, ready, providerID matches the Node |

### Connection model (existing, proven 2026-11-20)

| Path | How | State |
|---|---|---|
| Operator Mac → hosts | SSH (`scw1`/`scw2`, `~/.ssh/id_ed25519`) | working |
| Hosts → mgmt API | reverse SSH tunnel; remote `:16443` on each host points to the local kind API | **down** |
| Mgmt (kind) → workload API | operator `-L 16444` plus DNAT inside the kind node (`byoh-cluster-network-up`) | **down** |
| Worker → CP | `:6443` over the public NIC; only the peer set is allowed by `ok_rke2_guard` | working |

## 3. Reuse assessment

| Component | Reuse | Note |
|---|---|---|
| Hosts, OS, agent, installer output, InstallationSecret flow | **yes** | Already installed by exactly this code path (baseline of 2026-11-20). |
| Mgmt kind `ok-byoh-make-test` | **yes, isolated** | Local kind on the Mac; manages only `ok-byoh-scw1`. No production management plane is involved. |
| Tunnel/DNAT model | **yes** | `make byoh-cluster-network-up` plus the host reverse tunnel. Restarting it is a connection operation and needs approval (A1). |
| `ok_rke2_guard` nft table | **yes, keep** | It protects the public cluster ports. It is an RKE2 artifact, but it is protective and needs no change. |
| Make/Python toolchain | **yes, as is** | Its allowlists (`ok-byoh-make-test`, `ok-byoh-scw1`, `ok-bm-lab-01`) match the reused lab exactly. **Gap G1 from the previous draft no longer applies.** |
| Phase-0 RuntimeSDK extension and guard | later (M2) | Needs `RuntimeSDK`/`InPlaceUpdates=true` on this mgmt cluster. That changes the mgmt controllers and needs approval. |

Remaining proven gaps:
- **G2:** machine-readable `evidence.json` (UIDs, versions, binding).
- **G4:** a clean-state host preflight; it only matters for a fresh install.
- **Agent units `disabled`:** they do not survive a reboot. Document this; whether to enable them is a separate decision.
- **No kubeadm-upgrade path in the installer/agent** (M2).

## 4. Recommended role distribution (simplest safe one)

**Keep the current roles:** `ok-bm-lab-01` = control plane (KCP, singleton), `ok-bm-lab-02` = worker (MD).

Reasons:
- no reinstall is needed for M1;
- the worker is the only node that can be reset or reinstalled without losing the cluster;
- CAPI v1.14 cannot in-place upgrade a singleton KCP (the open blocker), so all real lifecycle tests run on the **worker** while the CP stays fixed at v1.36.6.

Swapping roles gives no benefit and would require a full reset of both hosts.

## 5. Test plan (each step needs its own approval; nothing has been executed)

| Step | What | Host impact | Approval |
|---|---|---|---|
| **M1-a** Reconnect + baseline evidence | `make byoh-cluster-network-up` and the host reverse tunnel; then `byoh-cluster-control-plane-status`, `byoh-cluster-worker-status`, `byoh-cluster-cilium-verify`; capture `evidence.json` (G2): Node / Machine / ByoMachine / ByoHost UIDs, binding, kubeletVersion, providerID | none (connections only) | **A1** |
| **M1-b** Real Install → Bootstrap → Join → Ready on the **worker** | Delete the worker Machine. The agent then resets the host (`kubeadm reset`, `uninstall.sh`, a path not yet proven live), the controller detaches, and the MD creates a new Machine. The existing ByoHost is re-attached; the InstallationSecret → install → `kubeadm join` → Ready path follows. Use the MD version **v1.36.5** so that M2 has an upgrade target. With only one worker host the MD must use `maxSurge=0`/`maxUnavailable=1`; otherwise a rollout waits forever for a second free ByoHost. Evidence: old and new Machine UIDs, ByoHost UID unchanged, new Node UID, kubeletVersion. | **destructive on `ok-bm-lab-02` only** (reset + reinstall); CP untouched | **A2** |
| **M2** (prepare only) | Extend the existing installer and agent minimally with a kubeadm-upgrade path (`kubeadm upgrade node`, kubelet package pin, restart). Add the new InstallationSecret key conflict-protected, keeping the bootstrap data. On mgmt, enable `RuntimeSDK`/`InPlaceUpdates` and deploy the Phase-0 extension and guard (A3). Then run an in-place upgrade of the **worker** from v1.36.5 to v1.36.6, never above the CP version (version skew). Verify the UIDs are unchanged and the kubelet is v1.36.6. | worker only | **A3** (mgmt gates/extension), **A4** (run) |

**Out of scope:** CP upgrade (singleton KCP blocker), a v1.37 minor upgrade (it would need the CP first), reinstalling the CP, and resetting both hosts.

## 6. Costs

The two servers keep running as before: EM-A116X-SSD at **€0.077/h each** (list price excl. VAT), about **€3.70/day for both**. There is **no additional cost**: no new server, IP or network.

Sources: [Cloud Mercato EM-A116X-SSD](https://pcr.cloud-mercato.com/providers/scaleway/flavors/em-a116x-ssd), [Scaleway Elastic Metal pricing](https://scaleway.com/en/pricing/elastic-metal).

## 7. Approvals required

- **A1:** restart the tunnel/DNAT (connection only).
- **A2:** worker reset + reinstall (destructive on `ok-bm-lab-02`).
- **A3:** change the mgmt feature gates and deploy the extension/guard.
- **A4:** worker in-place upgrade.
- **Optional:** enable the `byoh-hostagent` units on both hosts.

No commit/push.
