# ok-capd troubleshooting

## Docker daemon unavailable

Check `docker context show` and `docker info`. The selected context must be
the intended local Linux-container daemon. Do not switch contexts
automatically.

## Kind cluster exists but kubeconfig is missing

The workflow fails closed. Inspect the Kind cluster and recover its dedicated
kubeconfig manually after verifying its identity. Do not copy the active
`~/.kube/config` or a Scaleway kubeconfig into the CAPD path.

## Provider inventory is incomplete or has unexpected versions

Do not rerun `clusterctl init` blindly. Inspect Provider objects, namespaces,
deployments and webhook state using the dedicated kubeconfig. Preserve the
cluster for diagnosis.

## CAPD cannot access Docker

Check that `/var/run/docker.sock` is mounted in the Kind node and in the
CAPD controller deployment. Docker socket access is highly privileged. Do not
work around this by mounting a different host socket or using a production
daemon.

## Cleanup refuses to run

Cleanup intentionally refuses while CAPI workload Cluster objects remain.
Delete workload clusters through CAPI, wait for their infrastructure deletion,
and verify the inventory is empty before cleaning the management cluster.
