# Phase 1 — alternative: BYOH HA lab with 6 Scaleway VMs (cost-first)

Status: **read-only analysis, 2026-11-21.** No destroy, apply, order or commit. Compared against `PHASE1-SCALEWAY-HA-PLAN.md` (3 VMs + 2 Elastic Metal). ADR-047 findings remain **OPEN**. Prices exclude VAT; sources partly contradict each other, so confirm in the console. **UNVERIFIED** marks points that need console/API read access.

**Verdict:** The 6-VM variant is **feasible, about 55–60 % cheaper, and covers the whole BYOH lifecycle** (install → bootstrap → join → ready → reset → upgrade, KCP HA with 3 replicas). No change to the BYOH agent, installer or controller is needed. A real-hardware acceptance run before release remains necessary (§8).

## Minimal architecture

```
 Mac: kind mgmt (new, disposable) ──WireGuard (1 peer)──► gw  STARDUST1-S | public IPv4 | wg0 + HAProxy :6443 + NAT
                                                             │ Private Network 172.16.0.0/22 (free for Instances)
         ┌──────────────┬──────────────┬─────────────────────┼──────────────┐
       cp-1 DEV1-M    cp-2 DEV1-M    cp-3 DEV1-M          w-1 DEV1-S     w-2 DEV1-S     (no public IP; default route → gw)
```

- **All 6 VMs** run Ubuntu 24.04 in **one zone** (fr-par-1 or fr-par-2; whichever has DEV1 and STARDUST, UNVERIFIED), in **one** Private Network.
- **Only `gw`** has a public IPv4 address.
  - The 5 nodes have no public IP. Their default route (static, via cloud-init, no secrets) points to `gw`.
  - `gw` NATs their egress to pkgs.k8s.io and github.com.
  - Because the default route sits on the PN interface, **kubeadm and kubelet automatically use the PN IP**. The node-IP problem of the WireGuard/PN variants does not arise, and no BYOH template is needed.
- **API endpoint:** HAProxy on `gw` (TCP 6443 → cp-1..3, health check `/readyz`). This is the KCP `controlPlaneEndpoint` (gw's PN IP). It is a single point of failure, accepted for the lab.
- **Isolation:** the 5 nodes cannot be reached from the internet at all. The `gw` Security Group allows only UDP 51820 (WireGuard) and optionally SSH 22 (key-only), free in OpenTofu. No nft guards are needed.

## 1. Sizing (DEV1-M CP, DEV1-S worker/gw)

| Role | Type | Assessment |
|---|---|---|
| CP ×3 | DEV1-M (3 shared vCPU, 4 GB) | **Sufficient.** kubeadm CP + stacked etcd + Cilium agent/operator + CoreDNS use about 1.5–2.2 GB. The shared vCPU can cause occasional etcd "slow fsync/apply" warnings, which are acceptable for E2E (no performance tests). |
| Worker ×2 | DEV1-S (2 vCPU, 2 GB) | **Enough, but tight.** kubelet + containerd + Cilium agent use about 0.6–0.9 GB, which leaves about 1 GB for test pods. Fine for the BYOH lifecycle and small E2E workloads, not for load tests. The kubeadm preflight (≥ 2 vCPU, ≥ 1.7 GB) passes. |
| gw | **STARDUST1-S** (1 vCPU, 1 GB) | WireGuard + HAProxy + NAT need about 100 MB. Fall back to DEV1-S if STARDUST is not available in the zone (UNVERIFIED; it is not offered in fr-par-3). |

Disks: DEV1 types are expected to include **local SSD** (no block cost; UNVERIFIED). On the 20 GB worker disk the images (Cilium, CoreDNS, test images) fit; watch the eviction threshold. **Zone:** not fr-par-3 (higher prices, STARDUST is not offered there).

## 2. Cheapest suitable variants

| Option | €/h | Use |
|---|---|---|
| STARDUST1-S | 0.0046 (some sources: 0.0006) | gw |
| DEV1-S | 0.0137 (some sources: 0.0088) | worker; gw fallback |
| DEV1-M | 0.0202 | CP |
| PLAY2-PICO / NANO | 0.014 / 0.027 | not suitable: PICO has 1 vCPU, which fails the CP preflight; NANO is more expensive than DEV1-M and boots from block |

## 3. Network: is one Private Network plus one WireGuard access enough?

**Yes.**
- **Node ↔ node:** directly over the PN (free for Instances, no VLAN work as with Elastic Metal).
- **Mac ↔ lab:** a single WireGuard peer, Mac ↔ `gw`. The Mac gets a route to the PN and reaches every node by SSH. `gw` routes between the PN and the WireGuard subnet.
- **Rejected:**
  - a WireGuard hub for the node traffic: unnecessary, and a bottleneck;
  - a Scaleway Public Gateway or Load Balancer: they would cost extra, and `gw` already does both jobs.

## 4. Total costs (comparison)

| Item | 6 VMs (this variant) | 3 VMs + 2 EM (HA plan) |
|---|---|---|
| CP 3× DEV1-M | 0.0606 | 0.0606 |
| Workers | 2× DEV1-S 0.0274 | 2× EM-A116X-SSD 0.1540 |
| gw | STARDUST1-S 0.0046 | STARDUST1-S 0.0046 |
| Public IPv4 | 1× 0.004 (gw only) | 4× 0.016 (EM IPv4 included) |
| Private Network / SG / traffic | 0 | 0 |
| Storage | 0 (local SSD, UNVERIFIED) | 0 |
| **€/h** | **≈ 0.097** | **≈ 0.236** |
| **€/day / week / month (730 h)** | **≈ 2.30 / 16 / 71** | ≈ 5.70 / 40 / 172 |

Sensitivity of the 6-VM variant:
- gw on DEV1-S instead of STARDUST: about +€0.009/h.
- If boot-from-block is required (6 × 20 GB SBS): about +€0.01–0.015/h.
- **Worst case ≈ €0.12/h ≈ €2.90/day.**

The savings in either case are **≈ 50–60 %**.

The two existing EM servers cost **€3.70/day** until their (separately approved) destroy.

## 5. Separate OpenTofu state, full create/destroy

**Yes.** Use a new root (e.g. `ok-rke2/tofu/scaleway-vm-lab`) with its own state. Resources:

| Resource | Count |
|---|---|
| `scaleway_vpc_private_network` | 1 |
| `scaleway_instance_server` | 6, with `private_network` and `root_volume.delete_on_termination=true` |
| `scaleway_instance_ip` | 1 |
| `scaleway_instance_security_group` | 1 |

- Cloud-init for the static default route and for gw (NAT/HAProxy/WireGuard) contains **no secrets**. The WireGuard keys are generated on the gw and on the Mac.
- `plan -destroy` removes all 9 objects.
- Pure Instances: unlike Elastic Metal there is **no stock or re-order risk**, so repeated apply/destroy cycles are cheap and reliable.

Reuse:
- provider/lock and the `scw-tofu` wrapper (typed confirmation, new-deployment guard) from `ok-rke2/tofu/scaleway`;
- the saved-plan destroy pattern from `docs/scaleway-lab-rebuild.md`;
- the `inventory_hosts` output convention.

## 6. CAPI BYOH and the installer on VMs, unchanged?

**Yes, no code change:**
- The agent and installer work at the OS level; the installer requires only Ubuntu 24.04/26.04 on amd64/arm64.
- The ByoHost name, the Node name and the providerID come from the hostname.
- The upstream `test/e2e` suite even runs BYOH in Docker containers.

Prerequisites, all satisfied by Scaleway's Ubuntu image (UNVERIFIED):
- no distro containerd/docker/runc;
- swap off;
- `br_netfilter`/`overlay` kernel modules available.

**Only lab parametrization:** the toolchain allowlists (`ok-byoh-make-test`, `ok-byoh-scw1`, `ok-bm-lab-01` + IP) must come from the new inventory (gap G1). The CP manifest gets `controlPlaneEndpoint = <gw-PN-IP>:6443`, KCP replicas 3 / `maxSurge 0`.

## 7. Management connection without 5 reverse tunnels

| Direction | Solution |
|---|---|
| Nodes → mgmt API | Through `gw` over WireGuard to the Mac. The default route already points to `gw`, which routes the Mac's WireGuard subnet into `wg0`. On the Mac, a forwarder (`socat` from the WG IP:16443 to the kind API on 127.0.0.1) is needed, because kind/colima only publishes on localhost. The **new** mgmt kind gets the Mac WG IP as an API `certSAN` (kind config `kubeadmConfigPatches`). The agents' bootstrap kubeconfig then points at `https://<mac-wg-ip>:16443`. |
| Mgmt (kind in colima) → workload API | `https://<gw-PN-IP>:6443` through the Mac's WG route. Whether colima's VM networking follows the Mac route is UNVERIFIED; the fallback is the existing DNAT pattern (`byoh-cluster-network-up`). |
| Operator → nodes | SSH over WireGuard directly to the PN IPs (no public IPs on the nodes) |

This means 1 WireGuard tunnel and 1 forwarder instead of 5 reverse SSH tunnels.

A **new** disposable mgmt kind is needed, because the existing `ok-byoh-make-test` has no SAN for the WG IP. The remaining small risk is the kubeconfig and certificate handling of the agent CSR flow with a non-localhost server. That is a test item in M1-a.

## 8. What needs real bare-metal hardware

**Not needed** for:
- the BYOH lifecycle (install, bootstrap, join, reset/uninstall, reuse, kubeadm upgrade, in-place via RuntimeSDK);
- KCP HA, etcd quorum and rolling or in-place updates;
- CSR/enroll, InstallationSecret and the guard.

All of these are OS-/Kubernetes-level.

**Real bare metal is required** for:
1. **Hardware and OS specifics:** physical NIC names (`eno1` vs `ens*`; `DefaultNetworkInterfaceName` templating), software RAID/md, multiple disks, long POST/reboot times, and in-place upgrades with a **physical reboot**.
2. **Provider bare-metal lifecycle:** Elastic Metal provisioning, reinstall, stock and re-order (only relevant if it becomes part of the product).
3. **Performance and latency:** etcd fsync on real NVMe/SSD, network throughput, and Cilium datapath performance.
4. **Virtualization workloads:** KubeVirt or nested virtualization (e.g. the Talos VMs in the ok-rke2 lab), which do not run sensibly on DEV1.
5. **Final acceptance** "real host" before a product release (the claim "bare metal supported").

**Recommendation:**
1. Run the full lifecycle and HA development on the **6-VM lab**.
2. Prove bare metal only at the end, in a short time-boxed run (e.g. 1–2 EM for a few hours, then destroy).
3. Destroy the two existing EM servers after a separate approval, following the HA plan's Phase A. The IAM read-permission blocker A0 still applies there.

## Decisions

- **V1:** run the 6-VM variant instead of 3 VMs + 2 EM (recommended).
- **V2:** zone fr-par-1 or fr-par-2, depending on DEV1/STARDUST availability (console check).
- **V3:** a new disposable mgmt kind with WG certSAN (recommended) or keep the old one with the DNAT/tunnel model.
- **V4:** when and whether to run the destroy of `ok-bm-lab-01/02` (Phase A, after A0) and the time-boxed bare-metal acceptance run.

Sources:
- [Scaleway Instances FAQ (prices, IPv4 €0.004/h)](https://scaleway.com/en/docs/faq/instances)
- [Scaleway virtual instances pricing](https://www.scaleway.com/en/pricing/virtual-instances-pricing/)
- [Cloud Mercato STARDUST1-S](https://pcr.cloud-mercato.com/providers/scaleway/flavors/stardust1-s/pricing)
- [Cloud Mercato PLAY2-NANO](https://pcr.cloud-mercato.com/providers/scaleway/flavors/play2-nano/pricing)
- [VPC FAQ (PN free for Instances)](https://www.scaleway.com/en/docs/vpc/faq.md)
- [Elastic Metal options](https://scaleway.com/en/elastic-metal/options)
- [Instances concepts (local volumes)](https://www.scaleway.com/en/docs/instances/concepts/)
- [Product availability guide](https://scaleway.com/en/docs/account/reference-content/products-availability)
