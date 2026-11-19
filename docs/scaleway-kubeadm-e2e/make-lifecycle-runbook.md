# Scaleway BYOH lifecycle – Make E2E runbook

**Live evidence:** 19 November 2026, local Mac (Colima) and two existing
Scaleway Ubuntu 24.04.5 amd64 hosts. **Scope: disposable lab only.**
Status is **partial live replay**: management, host enrollment, CP kubeadm,
workload API routing and Cilium have been exercised successfully using
Make targets; the worker has **not** been re-provisioned in this replay.
A previous *interactive* E2E did provision both nodes and perform an
in-place v1.36.6 → v1.37.2 upgrade; it is distinct from this replay.

## Identities and change boundaries

| Component | Expected identity |
|---|---|
| Repository/branch | `openkubes/ok-baremetal-byoh`, `feat/byoh-scaleway-lifecycle-automation` |
| Management | kind `ok-byoh-make-test`, `~/.config/ok/byoh-make-test/kubeconfig` |
| Workload | CAPI `ok-byoh-scw1`, Kubernetes `v1.36.6` |
| Control plane | `ok-bm-lab-01`, `ubuntu@51.158.62.217` |
| Worker, reserved | `ok-bm-lab-02`, `ubuntu@51.158.62.216` |
| Inventory | `config/hosts/scaleway.yaml` |
| Workload credentials | `~/.config/ok/byoh-scw1/kubeconfig-tunnel` |
| Cilium artifact | `~/temp/kubernauts/ok/ok-cluster/.tools/cilium-1.19.6.tgz` |
| Cilium artifact SHA-256 | `21c43cf53841f9ab0375047d95aa4c64051ea52bbd2c679416e6408f5f1c9179` |

**Protect:** other kind clusters (especially `ok-byoh-dev`), Scaleway
instances/network/firewall, OpenTofu state, and existing unrelated SSH
sessions. Do not restore old host bootstrap credentials or reuse an old
workload kubeconfig. Keep the management reverse SSH tunnels for each
host alive; they are different from the workload API tunnel.

The commands below are **not a single unattended script**. Commands
labelled `ACTION` change management resources, local networking or
selected hosts. They require a human review and an exact plan-hash
confirmation. A STOP means investigate; **do not blindly rerun**.

## 0. Operator setup – already completed in current replay

The selected management cluster was created via `make
BYOH_KIND_NAME=ok-byoh-make-test ok-up` (kind v1.36.4,
CAPI v1.14.4, current BYOH controller in manual CSR approval mode).

For a NEW environment only, follow
[`docs/byoh-host-onboarding.md`](../byoh-host-onboarding.md)
and the historical [E2E kit](e2e-results.md), then run:

```sh
cd ~/temp/kubernauts/ok/ok-baremetal-byoh
make BYOH_KIND_NAME=ok-byoh-make-test ok-up
```

`ok-up` **does not** create Scaleway hosts, wire network paths,
approve host CSRs or provision the whole workload cluster.

## 1. Host registration – completed and live verified

These existing Make targets were used for the two-host inventory.
Follow their preview/confirmation prompts; do not execute the
registration ACTION commands against already registered hosts.

| Stage | Make target | Nature |
|---|---|---|
| SSH, CA, inventory, tunnel identity | `byoh-hosts-preflight` | READ |
| Bootstrap staging | `byoh-hosts-enroll` | ACTION, confirmed |
| Enrollment-only unit preparation | `byoh-hosts-agent-prepare` | ACTION, confirmed |
| CSR enrollment plan/start | `byoh-hosts-register-plan`, `byoh-hosts-register` | READ / ACTION |
| CSR identity and private-key proof | `byoh-hosts-csr-verify`, `byoh-hosts-csr-approve-plan` | READ |
| Exact CSR approval | `byoh-hosts-csr-approve` | ACTION, confirmed |
| Permanent certificate/ByoHost proof | `byoh-hosts-register-verify` | READ |
| Temporary bootstrap revocation | `byoh-hosts-bootstrap-revoke-plan`, `byoh-hosts-bootstrap-revoke` | READ / ACTION |

On 19 November, both hosts' permanent credentials were verified with
0600/root ownership; ByoHosts registered; bootstrap tokens and
temporary host credentials were revoked. **No private keys, CSR
request bytes, bootstrap tokens or kubeadm join tokens are runbook
evidence.** The worker is *registered*, not yet rejoined to this new
workload cluster.

## 2. Control-plane CAPI declaration – completed

Targets (in tested order):

```sh
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-control-plane-plan
# ACTION: review digest, approve
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-control-plane-apply
# READ ONLY, after reconciliation
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-control-plane-verify
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-control-plane-install-plan
# ACTION: changes selected CP agent unit and starts agent/kubeadm
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-control-plane-install
# READ ONLY: expected WAIT_CNI until Cilium is installed
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-control-plane-status
```

The CP install plan validates exact ByoHost/ByoMachine identity,
UID-bound references, ready installer Secret, permanent host
credentials and stopped systemd unit. An optional absent
`corev1.ObjectReference.kind` was encountered during live tests; the
validator now permits absence only for a real core Secret with
consistent name/namespace and valid UID constraints. The resulting
agent installed kubeadm/kubelet/kubectl v1.36.6 and initialized the
control-plane API on port 6443. `/readyz` returned `ok`; all
control-plane static Pods were Running/Ready. Before CNI,
`NotReady` Node + Pending CoreDNS is **WAIT_CNI**, not a failed
kubeadm install.

**Never rerun CP install** after it has started: systemd and kubeadm
operations are not guaranteed idempotent on partial failures.

## 3. Workload API network – completed, lab-only

```sh
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-network-plan
# ACTION, only after the CP API is serving
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-network-up
```

Observed:

- Mac `127.0.0.1:16444` SSH forwarding to
  `51.158.62.217:127.0.0.1:6443`
- DNAT **only inside** `ok-byoh-make-test-control-plane`:
  `51.158.62.217:6443` → `192.168.5.2:16444`
- Kind management CAPI `Cluster.Available=True`,
  KCP `Available=True`, CAPI Machine `Running` after the route
- TLS validation remains mandatory; a TCP listener alone is not
  evidence of authenticated API connectivity

Separate host→management reverse SSH tunnels on port 16443 were
already established earlier. See [network-paths.md](network-paths.md).
Do not recreate old tunnel destination ports from yesterday without
comparing them to the current kind API endpoint. SSH/NAT are not a
production architecture; WireGuard/Private Network is a separate
planned replacement.

## 4. Cilium installation – completed, live verified

```sh
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-cilium-plan
# ACTION, only if plan shows no prior or partial Cilium release
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-cilium-install
# READ ONLY and safe to repeat on the existing deployment
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-cilium-verify
```

Cilium 1.19.6 was installed against the **workload** API, not kind;
`operator.replicas=1`, `kubeProxyReplacement=false`,
`k8sServiceHost=51.158.62.217`, `k8sServicePort=6443`.

Observed real sequence: Helm installation succeeded, then the
install target exited nonzero because the Node was still `NotReady`
during its immediate post-install check. Shortly afterward,
`byoh-cluster-cilium-verify` returned:

```text
PASS: pinned Cilium release deployed with expected values; Node/CoreDNS Ready
NOTE: multi-node connectivity and network policy still require worker E2E
```

Additional read-only `kubectl` evidence showed one
`ok-bm-lab-01` Node `Ready` / v1.36.6; Cilium DaemonSet 1/1,
Cilium Operator Deployment 1/1, both CoreDNS Pods 1/1 Running.
**Do not retry `cilium-install` after a successful Helm install.**
Use `cilium-verify` for convergence. The implementation should later
separate Helm install success from bounded readiness waiting.

During preflight, an old workload
`~/.config/ok/byoh-scw1/kubeconfig-tunnel` with a mismatched CA/client
identity was archived by the operator. The Cilium workflow then
retrieved the *current* CP admin config over SSH, used a fresh
TLS-validated temporary kubeconfig, and stored a restricted
workload-specific kubeconfig. Never silently overwrite mismatching
credentials. Helm 4 needs `helm list` **without** the Helm 3
`--all` flag; the code now selects by Helm major version.

## 5. Worker CAPI rollout – NOT done in this replay

Historical *interactive* E2E proved the second host
`ok-bm-lab-02` could join using a MachineDeployment with
`role=scaleway-worker` and reach Ready. The new Make-only
`byoh-cluster-worker-{plan,apply,verify,arm,status}` flow
has **not** yet been implemented/tested.

Until it exists, do not pretend this is a two-node cluster or execute
an unreviewed `kubeadm join`. Reuse the existing
[`e2e-results.md`](e2e-results.md) and
`scripts/scaleway-e2e/byoh-e2e.sh` only as source material for the
new guarded Make orchestration. It must verify registration, bound
ByoMachine UID, installer Secret, direct worker-to-CP API connectivity,
version, selector and stopped agent before requiring a single explicit
approval to arm the worker.

After rollout, independently verify two Nodes Ready, MachineDeployment
1/1, Cilium on both Nodes, cross-node pod connectivity and policy.
These are **not** established by today's CP-only status checks.

## 6. Upgrade, KubeVirt, HA, pivot and cleanup – separate milestones

Historical interactive upgrade **did** reach Kubernetes
v1.37.2 on both nodes with Cilium still operational. That upgrade was
manual `kubeadm`, with CAPI Cluster paused and KCP/MD/Machine specs
remaining at v1.36.6; it is **not** a safe CAPI-managed upgrade.
See [upgrade-1.37.md](upgrade-1.37.md). Never unpause the historical
cluster or blindly patch spec versions. This new cluster is currently
v1.36.6.

KubeVirt installation on this exact BYOH workload is **not verified**
by the available BYOH repository runbooks; do not claim it is part
of this replay or add it to the automatic baseline without separate
evidence and opt-in.

HA, `clusterctl move`/pivoting, full egress/policy E2E,
production networking and unattended cleanup remain unproven.

## 7. Target architecture and idempotency/recovery contract

Desired user-facing flow:

```text
make BYOH_KIND_NAME=ok-byoh-make-test ok-up
make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-up   # NOT IMPLEMENTED
```

Underlying state machine:

```text
management-ready → hosts-registered → CP-declared → installer-ready
→ CP-running → workload-network-ready → Cilium-ready
→ worker-bound → worker-Ready → dataplane-E2E
```

- `plan`/`status`/`verify`: read-only; repeat safely.
- Already reached desired state: return PASS/no-op.
- New or drifted identity: STOP and inspect; never silently
  overwrite kubeconfigs, host agents or live CAPI objects.
- Side effects: require digest-bound approval for an explicitly
  selected inventory. Authorization of one phase does not
  authorize different hosts or network changes.
- Partial write: stop and recover with phase-specific evidence;
  no automatic second `kubeadm init`, `helm install`,
  bootstrap-token issuance, CSR approval or destructive cleanup.
- Record non-secret evidence per phase; do not print certificate
  private keys, kubeconfig, Secrets, bootstrap/join tokens or
  token-bearing journal text.
- `make byoh-hosts-idempotency-test` runs offline regression tests.
  It is **not** a live proof of idempotent end-to-end retries.

This runbook is the consolidated **observed** Scaleway replay,
not a claim that the one-command orchestrator or worker
automation has been completed. Older historical
[`README.md`](README.md), [`e2e-results.md`](e2e-results.md),
[`cilium.md`](cilium.md) and
[`network-paths.md`](network-paths.md) remain source evidence.
