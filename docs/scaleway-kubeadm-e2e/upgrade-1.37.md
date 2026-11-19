# Scaleway BYOH Kubernetes 1.37 Upgrade

Date: 2026-11-19
Cluster: ok-byoh-scw1
Scope: Lab-only, manually executed in-place upgrade

## Objective

Validate Kubernetes 1.36.6 -> 1.37.2 on two existing
Scaleway BYOH hosts without additional infrastructure.

Downtime was explicitly accepted.

## Environment

- Control Plane: ok-bm-lab-01
- Worker: ok-bm-lab-02
- OS: Ubuntu 24.04.5 LTS
- Container runtime: containerd 2.3.7
- CNI: Cilium 1.19.6
- Kubernetes initial version: v1.36.6
- Kubernetes target version: v1.37.2

## Upgrade Procedure

1. Paused the CAPI Cluster.
2. Verified Kubernetes 1.37 package availability.
3. Upgraded kubeadm on the control plane.
4. Executed kubeadm upgrade plan.
5. Executed kubeadm upgrade apply v1.37.2.
6. Upgraded control-plane kubelet and kubectl.
7. Switched the worker to the 1.37 package repository.
8. Executed kubeadm upgrade node on the worker.
9. Upgraded worker kubelet and kubectl.
10. Verified Kubernetes readiness and system pods.

No etcd backup was created for this disposable lab test.
Data-loss risk was explicitly accepted.

## Verified Results

| Component | Result |
|---|---|
| Control-plane node | v1.37.2, Ready |
| Worker node | v1.37.2, Ready |
| Kubernetes API /readyz | OK |
| System pods | Running, 1/1 |
| Cilium | Running |
| CAPI pause | true |

Both existing hosts were reused.

No Scaleway servers or OpenTofu resources were
created, deleted, or modified during this upgrade.

## CAPI Lifecycle Limitation

This was a manual kubeadm in-place upgrade, NOT a
CAPI-managed Machine replacement rollout.

At the end of the test:

- Kubernetes nodes reported v1.37.2.
- KCP spec.version remained v1.36.6.
- MachineDeployment template version remained v1.36.6.
- Existing CAPI Machines remained at spec.version v1.36.6.
- BYOH host-to-Machine assignments were preserved.
- Cluster.spec.paused remained true.

CAPI reconciliation must NOT be resumed without a
separate compatibility and rollout-impact assessment.

## Follow-up Work

- CAPI reconciliation and version alignment
- Safe BYOH host reset and uninstall
- Cluster cleanup and reprovisioning
- etcd backup and restore validation

These are not covered by this successful upgrade test.

## Evidence

Raw evidence was captured locally under:

/tmp/ok-byoh-upgrade-137-evidence

Raw CAPI objects are intentionally not committed.

## CAPI Reconciliation Assessment

The existing BYOH E2E tests demonstrate replacement-based
upgrades with host reuse:

- cluster_upgrade_test.go: maxSurge 0 with sufficient
  control-plane replicas.
- clusterclass_upgrade_test.go: maxSurge 1 with an
  additional available control-plane host.

Neither path validates adopting an existing Machine after
a manual kubeadm in-place upgrade.

The Scaleway lab contains one control-plane host and one
worker host, both assigned to existing Machines.

Decision:
- Keep Cluster.spec.paused=true.
- Do not patch Machine.spec.version or KCP/MD versions.
- Do not resume CAPI reconciliation without validating
  the resulting rollout behavior.
- Track safe reconciliation or reprovisioning separately.

This limitation does not invalidate the successful
Kubernetes 1.37.2 in-place upgrade.
