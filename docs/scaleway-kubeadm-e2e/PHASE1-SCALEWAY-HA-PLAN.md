# Phase 1 — Scaleway BYOH HA lab: minimal architecture (cost-first)

Status: **analysis only, 2026-11-21.** No provisioning, destroy or apply was done, and no Scaleway API call or Keychain read was made. The lab is temporary and disposable. Priorities are cost first, then reproducible OpenTofu apply/destroy, then simplicity, then real HA/BYOH tests. ADR-047 findings remain **OPEN**.

Basis: the destroy-impact analysis and repo facts below, plus public price lists. Prices exclude VAT. Sources disagree in places; confirm prices in the console before ordering.

## 1. Preferred minimal architecture

```
 Mac: kind mgmt (CAPI + BYOH, unchanged) ── SSH ──► all nodes
   │  CAPI / kubectl → https://<gw-ip>:6443           ▲ agents → mgmt API: existing reverse SSH tunnel per node (:16443)
   ▼
 ┌──────────────────────────┐
 │ gw   STARDUST1-S + IPv4   │  HAProxy TCP :6443 → cp-1..3:6443 (health check); no failover, no VPN
 └─────────────┬────────────┘
   public IPv4 (flat network, Scaleway-internal routing), SG/nft = peer allowlist
   ┌───────────┼───────────┬────────────────────┬──────────────────┐
 cp-1 DEV1-M  cp-2 DEV1-M  cp-3 DEV1-M          w-1 EM-A116X-SSD   w-2 EM-A116X-SSD
 (+IPv4)      (+IPv4)      (+IPv4)              (IPv4 incl.)       (IPv4 incl.)
```

| Building block | Choice | Why |
|---|---|---|
| Control plane | 3× **DEV1-M** (3 vCPU, 4 GB, local SSD, Ubuntu 24.04), each with a public IPv4 | The cheapest type with enough headroom for etcd + apiserver + Cilium. DEV1-S (2 GB) is too tight; PLAY2-PICO has 1 vCPU and fails the kubeadm preflight. No block volume is needed, so there is no storage cost. |
| Workers | 2× **EM-A116X-SSD**, hourly, fr-par-2 | The cheapest Elastic Metal offer, already proven with this installer. |
| API endpoint | A 4th VM `gw` (**STARDUST1-S**, fallback DEV1-S) running only **HAProxy** (TCP 6443, health check `/readyz`) | €0.009/h instead of LB-S (≈ €0.023/h). It is separate from the CPs, so a CP reboot or reset during HA tests does not take the endpoint down. A single point of failure is accepted for the lab. |
| Node network | **Flat over the public IPv4 addresses.** kubeadm/kubelet automatically use the default-route IP. Cilium VXLAN between nodes; optionally Cilium WireGuard encryption (free, one Helm value). | No Private Network, no WireGuard, and **no BYOH code change** (the G-WG/node-IP gap does not arise). |
| Isolation | One Instances **Security Group** for the 4 VMs (free, in OpenTofu): inbound 22 from anywhere (key only), all traffic from the 6 lab IPs, 6443 on `gw` from the operator IP. Elastic Metal has no SG, so the existing `ok_rke2_guard` nft pattern (peer allowlist) is applied during host prep. | The minimum needed to keep etcd/kubelet off the internet. |
| Mgmt connection | Mac → workload: directly to `gw:6443` (the kind container has internet egress; no more `-L 16444`/DNAT). Nodes → mgmt: the **existing** reverse-SSH-tunnel pattern per node. | No new code. A single tunnel through `gw` is a later optimization (it would need tunnel termination plus TLS server name). |
| Zones | All VMs in **fr-par-2**, like the workers. Optionally put `cp-1` in fr-par-1 at no extra cost. Do not use fr-par-3: higher prices, no STARDUST, no EM/LB. | BYOH has no failure-domain support in CAPI anyway; zone spreading only adds complexity. |
| CAPI | KCP 3 replicas, `maxSurge: 0`; MD 2, `maxSurge: 0` / `maxUnavailable: 1` | Real HA. CP in-place updates become possible (the singleton blocker no longer applies). |

## 2. Network variants compared

| Variant | Extra cost/h | Work | Assessment |
|---|---|---|---|
| **A — flat over public IPs + HAProxy VM** (preferred) | gw ≈ €0.009 | low: SG + nft allowlist; no routes, keys or VLANs | **cheapest working variant** |
| B — Scaleway Private Network + one mgmt VPN | EM PN **€0.03/h per EM server** (×2 = €0.06; free for Instances) + VPN VM | manual VLAN interface on every EM; kubeadm/kubelet must be pinned to the PN IP per node (BYOH templates cannot do that → agent extension); routes Mac↔PN | more expensive and more work; rejected |
| C — WireGuard hub (VM) | hub VM ≈ €0.009 | WG keys/config on 5 nodes + Mac; all node and pod traffic goes through the hub (bottleneck, SPOF); same node-IP gap as B | not needed; rejected |

A "single management VPN" is **not needed** for node communication. Nodes reach each other directly. The only extra path, nodes to the mgmt API on the Mac, is already solved by the existing reverse tunnel.

## 3. Total costs (estimate)

| Item | €/h | Count | €/h total |
|---|---|---|---|
| DEV1-M (CP) | 0.0202 | 3 | 0.0606 |
| Instance IPv4 (CP + gw) | 0.004 | 4 | 0.0160 |
| STARDUST1-S (gw) *(fallback DEV1-S 0.0137)* | 0.0046 | 1 | 0.0046 |
| EM-A116X-SSD (IPv4 included) | 0.077 | 2 | 0.1540 |
| SG, routing, traffic | 0 | – | 0 |
| **Total** | | | **≈ €0.236/h ≈ €5.70/day ≈ €40/week ≈ €170/month (730 h)** |

- The two Elastic Metal servers are **≈ 65 %** of the cost. The rest of the lab costs ≈ €0.08/h, or ≈ €2/day.
- Compared with the first draft (POP2 + LB-S, ≈ €0.42/h), this saves **≈ 44 %**.
- **Savings levers:** destroy the whole lab when idle (hourly billing, no commitment fee), or remove only the VMs and keep the EMs (see availability).

Price sources (some contradict each other; STARDUST was listed at €0.0006–0.0046/h, DEV1-S at €0.0088–0.0137/h). The calculation uses the higher official FAQ values.

## 4. Elastic Metal availability and the old servers

After a delete, an Elastic Metal server is wiped, and the offer is **not guaranteed** to be orderable again (stock per zone).

| Option | Description | Assessment |
|---|---|---|
| **E1 — adopt** `ok-bm-lab-01/02` into the new HA state (`tofu import` into the new root, then `state rm` in the old one); clean the OS via BYOH reset/uninstall or a Scaleway reinstall | no stock risk, no re-order, same IDs. Afterwards the whole lab is still fully removable through OpenTofu. | **recommended** |
| E2 — destroy and re-order (the original Phase A) | clean, but it may fail if EM-A116X-SSD is out of stock in fr-par-2/1. Only order when it is visible in the console. | only if E1 is not wanted |

Both are infrastructure writes (state moves, reinstall or destroy) and need their own approval.

## 5. Reuse

- **OpenTofu:** new root `ok-rke2/tofu/scaleway-ha` with its own state. It reuses the provider/lock, the `baremetal_server` resource with hourly offer and OS data, the `scw-tofu` wrapper (typed confirmation, new-deployment guard), the state backup procedure and the `inventory_hosts` output.
  - **New resources only:** `scaleway_instance_server` ×4, `scaleway_instance_ip` ×4, `scaleway_instance_security_group` ×1.
  - **Destroy:** the saved-plan pattern from `docs/scaleway-lab-rebuild.md` ("partial destroy": `plan -destroy -out`, review, sha256 approval, `apply`). For the old lab, `lab-destroy.sh` stays as is.
- **Host prep (provider-neutral, SSH):**
  - install HAProxy on `gw` (static config with the 3 CP IPs from the tofu output);
  - apply the nft peer allowlist on the EMs (`scaleway-guard.yml` pattern);
  - Ubuntu updates.
  - No secrets in tofu/cloud-init.
- **BYOH:** unchanged (agent, installer, InstallationSecret, enroll/CSR/register/CP/worker targets).
  - Only the toolchain allowlists (lab names, host names, IPs) must come from the new inventory.
  - Mgmt stays a local kind on the Mac.

## 6. Risks / open points

- **A0 (blocker for any destroy):** the API key lacks IAM read access to LB/Block/VPC/IPAM (39× HTTP 403 on 2026-11-18). Owner action: grant the read-only sets.
- **Quotas:** unverified (Instances ≥ 4 + 4 IPs, EM ≥ 2), as is STARDUST1-S availability in fr-par-2 (fallback DEV1-S, +€0.009/h).
- **Operator IP:** HAProxy ACL/SG on 6443 is tied to the operator's public IP. If it changes, run `tofu apply` with the new variable.
- **5 reverse tunnels** from the Mac: fragile but proven.
- **Prices:** must be confirmed in the console at order time.

## 7. Decisions

- **D1 — Elastic Metal:** E1 adopt (recommended) or E2 destroy and re-order.
- **D2 — Project:** keep `2b7fcb3b…` with separate state and tags (recommended for simplicity) or create a new project.
- **D3 — Cilium WireGuard encryption** on (free, recommended) or off.
- **D4 — Mgmt:** reuse `ok-byoh-make-test` after removing `ok-byoh-scw1`, or create a new kind.

## 8. Executable plans (each gate needs explicit approval)

### Phase A — old lab (with D1 = E1: adopt; with E2: `lab-destroy.sh` per runbook D1–D3)

| # | Step | Gate |
|---|---|---|
| A0 | IAM read-only sets for the API key (owner) | **Approval** |
| A1 | Encrypted backup of the old state (`terraform.tfstate`, `.backup`, `*.tfplan`, old tfvars) | – |
| A2 | Save mgmt objects `ok-byoh-scw1` and the workload snapshot (read-only) | – |
| A3 | Remove `ok-byoh-scw1` from mgmt in a controlled way (D4) | **Approval** |
| A4 | Clean the hosts: BYOH reset/uninstall, or a Scaleway reinstall of Ubuntu 24.04 | **Approval** (destructive on the hosts) |
| A5 | E1: `import` into the new root, then `state rm` in the old root (with state backups before and after). E2: `lab-destroy.sh plan`, sha256 approval, `apply`. | **Approval** (state write or destroy) |

### Phase B — build the HA lab (new root `ok-rke2/tofu/scaleway-ha`)

| # | Step | Gate |
|---|---|---|
| B1 | Write the root (copy and extend), `fmt`/`validate`/`tofu test` (local, no API) | – |
| B2 | `scw-tofu plan -out=$PRIV/ha.tfplan`. Expected: create 4 instance, 4 IP, 1 SG; for E1 `0 to change` on the imported EMs; **0 destroy** | – |
| B3 | Owner approval of the plan sha256, then `apply`, then state backup | **Approval** (billable) |
| B4 | Host prep: HAProxy on `gw`, nft guard on the EMs, host-key fingerprints | **Approval** |
| B5 | BYOH path: enroll → register → CSR approve → CP install (KCP 3) → Cilium → workers (MD 2) → `evidence.json` | **Approval** |
| B6 | Later: kubeadm-upgrade extension (M2) and in-place tests | separate |

### Teardown (complete, via OpenTofu)

`scw-tofu plan -destroy -out=$PRIV/ha-destroy.tfplan`, review (4 instance, 4 IP, 1 SG, 2 EM), sha256 approval, `apply`. Then confirm in the console that the project has no remaining billable items.

Sources:
- [Scaleway Instances FAQ (prices, IPv4)](https://scaleway.com/en/docs/faq/instances)
- [Scaleway virtual instances pricing](https://www.scaleway.com/en/pricing/virtual-instances-pricing/)
- [Cloud Mercato STARDUST1-S](https://pcr.cloud-mercato.com/providers/scaleway/flavors/stardust1-s/pricing)
- [Cloud Mercato EM-A116X-SSD](https://pcr.cloud-mercato.com/providers/scaleway/flavors/em-a116x-ssd)
- [Elastic Metal options (Private Network €0.03/h)](https://scaleway.com/en/elastic-metal/options)
- [VPC FAQ (PN free except for Elastic Metal)](https://www.scaleway.com/en/docs/vpc/faq.md)
- [Elastic Metal: use Private Networks](https://www.scaleway.com/en/docs/elastic-metal/how-to/use-private-networks/)
- [Network/LB pricing (FR)](https://www.scaleway.com/fr/tarifs/network/)
