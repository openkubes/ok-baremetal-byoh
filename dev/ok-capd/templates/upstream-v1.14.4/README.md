# CAPD workload template source

These files are copied from the Kubernetes Cluster API v1.14.4 source tag:

- `test/e2e/data/infrastructure-docker/main/cluster-template/kustomization.yaml`
- `test/e2e/data/infrastructure-docker/main/bases/cluster-with-kcp.yaml`
- `test/e2e/data/infrastructure-docker/main/bases/md.yaml`
- `test/e2e/data/infrastructure-docker/main/bases/crs.yaml`
- `test/e2e/data/cni/kindnet/kindnet.yaml`

Upstream: https://github.com/kubernetes-sigs/cluster-api/tree/v1.14.4

The renderer combines the three resource bases in the same order as the upstream
Kustomize configuration, substitutes the local test values, and embeds kindnet
as a ConfigMap for the ClusterResourceSet. The pod network is deliberately
rendered IPv4-only for this first local smoke test.

These upstream files are distributed under the Apache License, Version 2.0.
