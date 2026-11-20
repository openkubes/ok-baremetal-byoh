# OpenKubes BYOH development with local CAPD

## 1. Understand the two local environments

Use `make ok-up` for the existing BYOH management environment. Use
`make ok-capd` for the separate CAPD-backed development environment. They
must not share a Kind cluster or kubeconfig.

CAPD creates disposable Docker-backed infrastructure for CAPI workload
clusters. BYOH manages existing Linux hosts and is the provider under
development. CAPD is a test substrate, not a replacement for BYOH.

## 2. Install prerequisites

Install Docker Desktop or Colima on macOS, or Docker Engine on Linux. Install
kind, kubectl, clusterctl v1.14.4 and jq. Ensure Docker is running and the
active Docker context is the intended local development daemon.

## 3. Start the management cluster

From the repository root:

```bash
make ok-capd
```

The workflow creates Kind cluster `ok-capd-dev`, stores its kubeconfig at
`~/.config/ok/capd/kubeconfig`, and initializes CAPI core, Kubeadm and CAPD.

## 4. Inspect and smoke-test

```bash
make ok-capd-status
make ok-capd-test
```

The initial smoke test checks provider inventory versions, controller rollout
status and Docker socket wiring. A real CAPD workload-cluster creation test is
not part of this first bootstrap commit yet.

## 5. Create the first CAPD workload cluster

Render and validate the pinned upstream CAPI v1.14.4 template first:

```bash
make ok-capd-workload-render
kubectl --kubeconfig ~/.config/ok/capd/kubeconfig apply --dry-run=server -f /tmp/ok-capd-smoke.yaml
```

After reviewing the rendered resources, create the disposable workload:

```bash
make ok-capd-workload-up
make ok-capd-workload-status
```

Its separate kubeconfig is stored under
`~/.config/ok/capd/workloads/ok-capd-smoke.kubeconfig`.

Delete only that workload through CAPI:

```bash
OK_CAPD_CONFIRM_WORKLOAD_DELETE=ok-capd-smoke make ok-capd-workload-down
```

The management cluster is preserved.

## 6. CAPD versus BYOH

CAPD provisions disposable Docker containers as workload-cluster machines.
BYOH targets existing Linux hosts. Keep their inventories, kubeconfigs,
credentials and evidence separate.

## 7. RuntimeSDK and No-Replacement

The Make targets are reserved but deliberately fail with
`NOT IMPLEMENTED` until the historical spike assets have been inventoried and
integrated. Do not interpret a placeholder target as a passing test.

## 8. Shutdown and cleanup

`make ok-capd-down` currently reports workload clusters without deleting
anything. To remove the entire management environment, first ensure that no
CAPI workload Cluster objects remain, then run:

```bash
OK_CAPD_CONFIRM_DELETE=ok-capd-dev make ok-capd-clean
```

Never use `kind delete clusters --all`. The cleanup target is scoped to the
dedicated `ok-capd-dev` cluster only.
