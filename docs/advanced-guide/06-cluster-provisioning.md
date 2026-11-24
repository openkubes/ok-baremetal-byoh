# 06 – Cluster Provisioning

## Plan the topology

A development reference uses one control plane and one worker. An HA reference uses three stacked-etcd control planes and two workers, with a stable API endpoint. These are workload host counts, excluding the management cluster and any external load balancer. Assign unique hosts and reviewed role labels; reserve spare capacity if replacement or surge is intentionally enabled. A protected in-place-only cluster requires a different operating policy from a replacement-capable cluster.

## Native resource model

The workload manifest contains a Cluster, ByoCluster, KubeadmControlPlane, ByoMachineTemplate resources, and worker MachineDeployment and KubeadmConfigTemplate resources, plus installer templates as required. Use the templates from the exact approved provider revision. Do not mix API versions or template references from unrelated releases.

| Input | Review requirement |
|---|---|
| Namespace and cluster name | Explicit, unique operational scope |
| Kubernetes version | Compatible pinned patch release |
| Control plane / worker replicas | Match available eligible hosts |
| API endpoint | Stable host and port, TLS SAN coverage |
| Pod and service CIDRs | No overlap with underlay or management networks |
| Host selectors | Match intended namespace-scoped capacity |
| Installer references | Compatible package and runtime configuration |
| Rollout strategy | Explicitly chosen; avoid unintended reset or replacement |

Use `clusterctl generate cluster` only against the verified provider repository and its supported template variables. Until the downstream provider packaging and template arguments are verified, the guide does not publish a copy-paste generation command. Obtain and render the approved template into `cluster.yaml`, retain its hash and inspect every resource and reference. Never use an unreviewed upstream fallback.

## Validate and provision

With the explicit management function `mg` from Chapter 5 and the intended namespace:

```bash
mg -n "$NS" get byohosts
mg -n "$NS" apply --dry-run=server -f cluster.yaml
```

Review the rendered manifest and namespace of every object. Confirm no existing resource would be unexpectedly modified. After authorization, apply it using the same context and namespace. Server dry-run is a validation aid, not proof of successful reconciliation.

```bash
mg -n "$NS" get clusters,kubeadmcontrolplanes,machinedeployments,machines
mg -n "$NS" get byomachines,byohosts
mg -n "$NS" get events --sort-by=.metadata.creationTimestamp
```

The BYOH controller selects a free matching host and writes assignment, bootstrap and installer references. The agent installs the necessary components and bootstraps the node. Observe conditions instead of manually editing controller-owned fields.

## Access the workload cluster

Retrieve its kubeconfig using the verified Cluster API workflow or the `<cluster>-kubeconfig` Secret, into a protected file. Example using the documented Secret convention (Bash):

```bash
umask 077
mg -n "$NS" get secret "${CLUSTER}-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > workload.conf
test -s workload.conf
```

Validate the workload endpoint and CA independently against the intended Cluster. Use `kubectl --kubeconfig workload.conf get nodes -o wide` only after identity verification. Install the reviewed CNI configuration (Chapter 7); a node can remain NotReady until networking is established. Keep credentials outside Git and evidence bundles.

## Acceptance criteria

All expected Machines and Nodes exist; assignments are unique and match the planned roles; API endpoint and networking work; control plane and etcd are healthy; workloads can schedule; component versions and UIDs are recorded. Capture the rendered desired state and actual identities. No claim of HA is justified solely by three replicas: validate load balancer and failure domains separately.

References: [Getting Started](https://github.com/openkubes/ok-baremetal-byoh/blob/main/docs/getting_started.md), [Host lifecycle](https://github.com/openkubes/ok-baremetal-byoh/blob/main/docs/host_lifecycle.md).
