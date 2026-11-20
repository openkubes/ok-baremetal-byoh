# ok-capd architecture

## Boundary with ok-up

`ok-up` remains the existing local BYOH management bootstrap. `ok-capd`
owns a different Kind cluster and a different kubeconfig:

- BYOH: `ok-byoh-dev`, `~/.config/ok/byoh-dev/kubeconfig`
- CAPD: `ok-capd-dev`, `~/.config/ok/capd/kubeconfig`

No CAPD target calls a BYOH Make target. No BYOH target calls an ok-capd
script. No Scaleway inventory, tunnel or OpenTofu state is in scope.

## Management plane

The CAPD management cluster contains:

- Cluster API core v1.14.4
- Kubeadm Bootstrap v1.14.4
- Kubeadm Control Plane v1.14.4
- Docker Infrastructure Provider (CAPD) v1.14.4
- cert-manager v1.21.3

The Kind node runs Kubernetes v1.36.4. The Docker socket is mounted into the
Kind node and then into the CAPD controller. This grants Docker-daemon-level
authority to CAPD and is therefore restricted to the disposable local daemon.

BYOH, RuntimeSDK extensions and No-Replacement Guard are intentionally not
installed in the initial bootstrap.

## Lifecycle semantics

- `ok-capd`: create if absent; verify if present; reject ambiguous partial state.
- `ok-capd-status`: read-only.
- `ok-capd-test`: verify provider inventory, controller readiness and socket wiring.
- `ok-capd-down`: read-only until workload lifecycle semantics are implemented.
- `ok-capd-clean`: delete only the exact allow-listed Kind cluster, with explicit
  confirmation, and only after no CAPI workload Cluster objects remain.

Credentials and runtime state remain outside Git.
