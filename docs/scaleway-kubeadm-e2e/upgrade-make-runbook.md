# BYOH Kubernetes upgrade automation – baseline and handoff

**Recorded:** 20–21 November 2026 (operator local session, Europe/Berlin)  
**Repository:** `openkubes/ok-baremetal-byoh`  
**Base:** `main` at `ccb9ca8` (merged PR #21)  
**Working branch:** `feat/byoh-kubernetes-upgrade-automation`  
**Current task:** Design a **read-only** `byoh-cluster-upgrade-plan` before any package, CAPI or cluster changes.

> This is a handoff/checkpoint, **not an upgrade execution log for the current cluster**.
> The 1.36.6 → 1.37.2 upgrade in [upgrade-1.37.md](upgrade-1.37.md)
> happened in an **earlier, separate manual experiment**.

## 1. Baseline established by the live operator

The two-node cluster was installed via the guarded Make lifecycle from PR #21.
The results below were captured from an explicit read-only operator check
on the currently running cluster, after switching to the new upgrade branch.

| Dimension | Live result |
|---|---|
| Management | kind `ok-byoh-make-test`, with CAPI v1.14.4 (provisioning baseline) |
| Workload CAPI Cluster | `default/ok-byoh-scw1`, `Available=True`, phase `Provisioned`, CP 1/1, Worker 1/1 |
| Cluster pause | `spec.paused` returned an **empty value** (field not set); do not interpret this as an explicitly set `false` |
| KubeadmControlPlane | `ok-byoh-scw1-control-plane`, v1.36.6, initialized, Ready/Available/UpToDate 1/1 |
| MachineDeployment | `ok-byoh-scw1-md-0`, v1.36.6, Ready/Available/UpToDate 1/1 |
| CP Machine | `ok-byoh-scw1-control-plane-2tztj`, Node `ok-bm-lab-01`, v1.36.6, Ready/Available |
| Worker Machine | `ok-byoh-scw1-md-0-9dlhs-vkdnx`, Node `ok-bm-lab-02`, v1.36.6, Ready/Available |
| CP Node | `ok-bm-lab-01`, `51.158.62.217`, Ubuntu 24.04.5, kubelet v1.36.6, Ready |
| Worker Node | `ok-bm-lab-02`, `51.158.62.216`, Ubuntu 24.04.5, kubelet v1.36.6, Ready |
| Runtime | containerd 2.3.7 on both Nodes |
| Cilium | DaemonSet `kube-system/cilium`, desired/current/ready/up-to-date/available **2/2** |

The management and workload version views **currently agree at v1.36.6**.
No v1.37 package or CAPI version changes have been made to this
current deployment as part of the new upgrade task.

### Exact read-only baseline commands

Run from the repository on the operator's Mac; never rely on the
ambient kubectl context:

```sh
cd ~/temp/kubernauts/ok/ok-baremetal-byoh

MGMT="$HOME/.config/ok/byoh-make-test/kubeconfig"
WORKLOAD="$HOME/.config/ok/byoh-scw1/kubeconfig-tunnel"

echo "=== CAPI Cluster / Control Plane / Worker ==="
kubectl --kubeconfig "$MGMT" -n default \
  get clusters,kubeadmcontrolplanes,machinedeployments,machines

echo
echo "=== CAPI desired versions and pause state ==="
kubectl --kubeconfig "$MGMT" -n default \
  get cluster ok-byoh-scw1 \
  -o jsonpath='Cluster paused: {.spec.paused}{"\n"}'

kubectl --kubeconfig "$MGMT" -n default \
  get kubeadmcontrolplane ok-byoh-scw1-control-plane \
  -o jsonpath='KCP version: {.spec.version}{"\n"}'

kubectl --kubeconfig "$MGMT" -n default \
  get machinedeployment ok-byoh-scw1-md-0 \
  -o jsonpath='Worker version: {.spec.template.spec.version}{"\n"}'

echo
echo "=== Actual workload versions ==="
kubectl --kubeconfig "$WORKLOAD" get nodes -o wide

echo
echo "=== Cilium ==="
kubectl --kubeconfig "$WORKLOAD" -n kube-system \
  get daemonset cilium
```

### Captured evidence (selected)

```text
Cluster ok-byoh-scw1: Available=True, CP desired/available/up-to-date 1/1/1,
                      Worker desired/available/up-to-date 1/1/1
KCP ok-byoh-scw1-control-plane: version v1.36.6, initialized=true, 1/1 Ready/Available
MD ok-byoh-scw1-md-0: version v1.36.6, 1/1 Ready/Available/UpToDate

Cluster paused:
KCP version: v1.36.6
Worker version: v1.36.6

ok-bm-lab-01 Ready control-plane v1.36.6 51.158.62.217 containerd://2.3.7
ok-bm-lab-02 Ready <none>        v1.36.6 51.158.62.216 containerd://2.3.7

cilium desired=2 current=2 ready=2 up-to-date=2 available=2
```

## 2. Previous manual v1.37.2 upgrade – separate, historical evidence

See [upgrade-1.37.md](upgrade-1.37.md), dated 19 November 2026.

Previously completed in a disposable two-host lab, with downtime and
data-loss risk explicitly accepted:

1. Pause the CAPI Cluster.
2. Verify availability of Kubernetes 1.37 packages.
3. Upgrade control-plane `kubeadm`; run `kubeadm upgrade plan` and
   `kubeadm upgrade apply v1.37.2`.
4. Upgrade control-plane `kubelet` and `kubectl`.
5. Change the worker to the 1.37 package repository; run
   `kubeadm upgrade node`; upgrade worker `kubelet` and `kubectl`.
6. Verify both Nodes, API readiness and system Pods.

**Observed:** both Nodes v1.37.2 Ready, API `/readyz=ok`,
Cilium running. **Critical limitation:** CAPI Cluster stayed paused;
KCP, MD and Machine desired versions stayed at v1.36.6. This was an
in-place kubeadm upgrade, not a CAPI-managed replacement rollout.

**Do not unpause or blindly patch** KCP, MachineDeployment or Machine
versions: that could trigger a replacement rollout on a lab that has
only one control-plane host and one worker host. The prior experiment
did not validate safe CAPI reconciliation or adoption of manually
upgraded machines. No etcd backup was made for that disposable lab
experiment, and the risk was explicitly accepted.

The successful previous experiment **does not** mean the current
cluster has already been upgraded. It remains at v1.36.6.

## 3. Upgrade automation – scope and required safety gates

Proposed user-facing Make targets:

| Target | Contract |
|---|---|
| `byoh-cluster-upgrade-plan` | Strictly read-only: verify identity, inventory, live versions and pause state, CAPI resource health, package/repository availability, target compatibility, Cilium, SSH and upgrade path. Report blockers without writes or token/Secret disclosure |
| `byoh-cluster-upgrade` | **Not yet implemented**. Explicit plan-bound confirmation and stepwise CP-before-worker upgrade **only after** CAPI reconciliation policy and rollback/recovery approach are reviewed |
| `byoh-cluster-upgrade-status` | **Not yet implemented**. Read-only detection of actual Kubernetes and CAPI version alignment, Node/Pod readiness and CNI health; separate partial/paused/at-risk states |

**First milestone:** implement `byoh-cluster-upgrade-plan` on
`feat/byoh-kubernetes-upgrade-automation` and validate it against
the untouched v1.36.6 two-node cluster. The next action **must not**
execute an upgrade, set `Cluster.spec.paused`, rewrite repositories,
or modify any Machine.

Key design questions to resolve **before** allowing an executing target:

- How does CAPI v1.14.4 / BYOH reconcile and express an in-place
  kubeadm upgrade without creating replacement Machines?
- When and how can the KCP/MD/Machine desired versions be aligned
  with the actual upgraded Nodes, safely and supportably?
- Is a CAPI-managed rollout actually required, and if so what spare
  hosts / HA endpoint / capacity prerequisites would it require?
- What backup, availability, rollback and partial-failure policy is
  acceptable, beyond the earlier disposable-lab-only risk acceptance?
- How are host-specific package repositories and exact Kubernetes
  patch versions pinned and checked without surprising upgrades?
- How is the current ephemeral SSH/NAT workload API path validated
  throughout the upgrade?

Guardrails: **do not modify Scaleway instances, network, firewall,
OpenTofu, unrelated kind clusters or `ok-byoh-dev`.** Preserve host
and cluster identities. Protect credentials, prohibit TLS bypasses,
and stop closed on conflicting or unexpected CAPI state.

## 4. Useful existing references and tested Make targets

- [Two-node provisioning baseline](two-node-e2e-baseline-2026-11-20.md)
- [Make lifecycle runbook](make-lifecycle-runbook.md)
- [Manual Kubernetes 1.37 upgrade](upgrade-1.37.md)
- `make BYOH_KIND_NAME=ok-byoh-make-test BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml byoh-cluster-worker-status`
  – live passed on 20 November 2026, read-only
- `make byoh-hosts-idempotency-test` – offline tests live passed after provisioning

**Deferred work, not part of the upgrade milestone:** cluster destroy,
fully generalized `byoh-cluster-up`, multiple worker pools, pivot,
automatic cleanup and cross-node Cilium NetworkPolicy E2E.

## 5. Resume checklist

1. Confirm clean current Git branch:
   `git status --short --branch`; do not run `git reset --hard`.
2. Fetch this branch's new runbook commit (if developed remotely).
3. Re-run the read-only baseline commands above; verify both Nodes and
   KCP/MD/Machines all still report v1.36.6.
4. Inspect prior [manual upgrade results](upgrade-1.37.md) and CAPI
   reconciliation constraints.
5. Develop **read-only** `byoh-cluster-upgrade-plan`, with offline
   drift/incorrect-target regression tests.
6. Review the plan's live output and explicitly decide the safe
   desired-version reconciliation strategy **before** creating any
   action target or initiating an upgrade.

**Checkpoint:** ready to design `byoh-cluster-upgrade-plan`; **no
upgrade action approved**.

## 6. M1 implementation checkpoint (20 November 2026)

The branch now contains `hack/dev/byoh-cluster-upgrade-plan.py` and the
`byoh-cluster-upgrade-plan` Make entry point. These files are **implementation
work**, not evidence of a successful live preflight or permission to upgrade.

Command (operator Mac, explicitly selected management cluster):

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-upgrade-plan
```

Current checks:
- Strict inventory and kind management context selection; fixed two-host SSH identities.
- Management API availability; CAPI Cluster not deleting or explicitly paused.
- Singleton KCP version v1.36.6, initialized and 1/1 status.
- Reuse of the existing worker-status checks for the MD, two Machines,
  ByoHost/ByoMachine UID binding, workload Node readiness, Cilium and CoreDNS.
- Read-only SSH inspection of installed kubeadm/kubelet/kubectl package versions
  and cached apt target availability on both hosts.
- Stable observation fingerprint without exposing kubeconfigs, SSH output,
  bootstrap tokens, Secrets or raw Kubernetes objects.

**Important limits:**
- apt-cache availability is a local cache observation, not a verified release
  repository provenance or a guarantee that package installation will succeed.
- The current helper does not validate etcd backup/restore, target Kubernetes
  compatibility, workload disruption, KCP/MD safe reconciliation, or rollback.
- Its success exit code means *observations completed*, not *upgrade approved*.
  Output deliberately remains BLOCKED pending a separately reviewed CAPI policy.
- No CAPI pause or desired version patch is performed; no kubeadm, apt, firewall,
  Scaleway, OpenTofu or kind mutations are performed.
- Offline unit tests and actual operator-side live evidence remain **pending**.
  Do not claim those have passed without recorded output.

**Next step:** review the new code and run offline tests. Then execute the
read-only plan on the operator Mac and append its redacted PASS/STOP evidence
here before proceeding with CAPI design.

### M1 offline verification commands (not yet executed)

```sh
make BYOH_KIND_NAME=ok-byoh-make-test byoh-cluster-upgrade-plan-test
```

The dedicated test script covers healthy baseline, explicit pause (both true
and false), pause annotation, KCP version drift, unavailable KCP,
uninitialized KCP, missing Cluster availability, SSH failure and mixed
installed package versions. Passing these isolated tests does **not**
replace the operator-side read-only plan or CAPI upgrade policy review.
