# OpenKubes local CAPD developer environment

This environment is intentionally independent from the existing `ok-up`
BYOH management workflow.

## Scope of the first implementation

- Creates a dedicated Kind management cluster named `ok-capd-dev`.
- Installs CAPI core, Kubeadm Bootstrap, Kubeadm Control Plane and CAPD.
- Pins the CAPI/CAPD release line to v1.14.4.
- Pins the Kind node image / Kubernetes version to v1.36.4.
- Pins cert-manager to v1.21.3 through the CAPI v1.14 clusterctl configuration.
- Verifies that CAPD can access the Docker socket through the Kind node.
- Does not install the BYOH provider, RuntimeSDK test extensions or No-Replacement Guard yet.

## Isolation contract

| Item | Value |
| --- | --- |
| Kind cluster | `ok-capd-dev` |
| Context | `kind-ok-capd-dev` |
| Kubeconfig | `~/.config/ok/capd/kubeconfig` |
| Clusterctl config | `dev/ok-capd/config/providers.yaml` |
| Runtime state / credentials | Outside the repository |

The workflow never reads or writes the ambient kubeconfig. It does not reuse
`BYOH_KIND_NAME`, the `byoh-dev` kubeconfig, Scaleway inventories, SSH tunnels
or OpenTofu state. It does not modify `ok-up` or its scripts.

## Prerequisites

Install and put these tools on `PATH`:

- Docker Engine-compatible CLI and a running Linux-container Docker daemon
- kind
- kubectl
- clusterctl v1.14.4
- jq

The Docker context must point to the intended local development daemon. On
macOS, Docker Desktop or Colima can provide this daemon. The Kind node config
bind-mounts `/var/run/docker.sock`; CAPD's access to this socket is equivalent
to Docker daemon authority. Do not use a shared or production Docker daemon.

## Start and verify

From the repository root:

```bash
make ok-capd
make ok-capd-status
make ok-capd-test
```

The first `make ok-capd` creates the management cluster and runs
`clusterctl init`. Repeated runs preserve an existing cluster and verify its
identity and provider inventory; partial or unexpected states fail closed.

`make ok-capd-test` verifies provider versions, controller rollouts and Docker
socket wiring. The first disposable CAPD workload lifecycle is driven separately:

```bash
make ok-capd-workload-render  # render only; no cluster writes
make ok-capd-workload-up      # apply and wait for one CP + one worker
make ok-capd-workload-status  # read-only CAPI and workload-node status
make ok-capd-workload-down    # delete workload only, with explicit confirmation
```

The workload kubeconfig is stored at
`~/.config/ok/capd/workloads/ok-capd-smoke.kubeconfig`. Workload deletion
requires `OK_CAPD_CONFIRM_WORKLOAD_DELETE=ok-capd-smoke`. It does not delete
the management Kind cluster.

## Shutdown and cleanup

`make ok-capd-down` is read-only in this initial phase. It lists CAPI workload
clusters and deliberately preserves the management cluster. Workload shutdown
will be defined together with the first workload lifecycle test.

`make ok-capd-clean` deletes only `ok-capd-dev`, and only when no CAPI
workload Cluster objects remain. Explicit confirmation is mandatory:

```bash
OK_CAPD_CONFIRM_DELETE=ok-capd-dev make ok-capd-clean
```

Do not delete CAPD workload Docker containers manually while CAPI resources
still exist. No cleanup runs automatically after a failed bootstrap or test.

## Deferred work

1. Validate the first real CAPD workload cluster on macOS/Apple Silicon and Linux.
2. Inventory and import the existing BYOH, RuntimeSDK and Guard spike assets.
3. Integrate BYOH as a separate provider in this management cluster.
4. Add RuntimeSDK accept/decline/failure/restart and in-place upgrade tests.
5. Add No-Replacement admission tests and durable evidence capture.
6. Complete Linux/macOS matrix validation and CI integration.
