# Verified BYOH two-node E2E baseline – 20 November 2026

**Repository:** `openkubes/ok-baremetal-byoh`  
**Source branch:** `feat/byoh-scaleway-lifecycle-automation`  
**Scope:** local macOS kind management + existing two Scaleway Ubuntu 24.04.5 amd64 hosts.  
**Evidence type:** captured operator terminal output from the 19–20 November 2026 live replay, as recorded in the [Make lifecycle runbook](make-lifecycle-runbook.md). No server credentials, kubeconfig bytes or tokens included.

## Baseline identity and observed result

| Resource | Observed value |
|---|---|
| Management cluster | `ok-byoh-make-test` (kind Kubernetes v1.36.4) |
| Management CAPI | v1.14.4 |
| Workload CAPI Cluster | `ok-byoh-scw1` |
| Workload Kubernetes | v1.36.6 |
| Control-plane Node | `ok-bm-lab-01`, `51.158.62.217`, Ready |
| Worker Node | `ok-bm-lab-02`, `51.158.62.216`, Ready |
| Worker MachineDeployment | `ok-byoh-scw1-md-0`: desired/current/ready/available/up-to-date **1/1** |
| Worker Machine | `ok-byoh-scw1-md-0-9dths-vkdnx` |
| BYOH identity | Both CAPI Machines associated with the correct ByoHosts via UID-bound references |
| CNI | Cilium v1.19.6, DaemonSet desired/current/ready/available **2/2** |
| Cilium Operator | 1/1 Ready |
| CoreDNS | Ready |
| Local workload API access | SSH forwarding `127.0.0.1:16444` + kind-only DNAT, TLS-validated |

The validated read-only command on the existing deployment:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-worker-status
```

**Captured successful output:**

```text
PASS: CAPI MachineDeployment ok-byoh-scw1-md-0 1/1 Ready/Available/UpToDate
PASS: two CAPI Machines and UID-bound BYOH hosts: ok-bm-lab-01, ok-bm-lab-02
PASS: two workload Kubernetes v1.36.6 Nodes Ready
PASS: Cilium DaemonSet 2/2, Operator 1/1, CoreDNS Ready
PASS: post-install worker status read-only; no credentials or tokens output
NOTE: cross-node pod connectivity and Cilium NetworkPolicy E2E still pending
```

The operator also executed `make byoh-hosts-idempotency-test`
successfully after worker installation. These are **offline tests**;
they do not prove live safe retry or production readiness.

## Validated provisioning gates (historical order)

1. `ok-up`: local management kind/CAPI/BYOH controller bootstrap
2. Inventory/SSH/tunnel preflight, host bootstrap and registration
3. Identity-bound CSR verification and explicit approval; permanent host identity checks
4. Revocation of temporary bootstrap credentials after registration
5. Control-plane CAPI plan/apply/verify and explicit install
6. Workload API network plan/up, restricted to local tunnel and selected kind
7. Pinned Cilium plan/install/verify
8. Worker MachineDeployment plan/apply/verify
9. Worker install plan, explicit digest-bound install, and post-install status

For the exact make commands, human authorization boundaries and the
stop/recovery rules, see [make-lifecycle-runbook.md](make-lifecycle-runbook.md).
The individual install targets must **not** be replayed on the
already-running cluster. Use only status and read-only checks to
observe this baseline.

## Limits and follow-up scope

- Both Nodes Ready and Cilium 2/2 **do not prove** cross-node Pod-to-Pod
  connectivity, Cilium policy enforcement, or multi-node failure recovery.
  Dedicated isolated network E2E remains open.
- The automated one-command `byoh-cluster-up` lifecycle is **not implemented**.
  Present `ok-up` manages the local management plane only.
- Existing worker-specific scripts are a single-worker **Scaleway lab
  implementation**, not generalized multi-pool production automation.
- HA control-plane failover, CAPI-managed Kubernetes upgrades, pivoting
  and unattended cleanup are **not verified** by this baseline.
- Previous *separate* manual E2E successfully upgraded v1.36.6 to
  v1.37.2, but left CAPI version reconciliation open; do not claim
  that upgrade is in this baseline.
- KubeVirt installation on **this specific two-node BYOH replay**
  has not been evidenced; do not add it to this baseline.
- Tunnel and NAT arrangements are ephemeral lab infrastructure, not
  the planned WireGuard / Scaleway Private Network architecture.

## Safety and replay guidance

- Preserve `ok-byoh-dev`, unrelated kind clusters, Scaleway instances,
  firewalls and all OpenTofu resources.
- Do not import stale host bootstrap kubeconfigs, bypass TLS checks,
  automatically approve CSRs or print Secrets.
- A planned next-stage `byoh-cluster-up` must observe this existing
  state and perform **no changes**, rather than replaying any
  `kubeadm init`, `kubeadm join`, Cilium install or host enrollment.
- This is a documented **lab E2E checkpoint**, not a production
  reliability or security certification.
