#!/usr/bin/env bash
# Requirement 9 (operator safety): print EXACTLY what will be created and deleted, and the
# cleanup boundary. Pure dry-run — creates and deletes nothing.
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

cat <<EOF
==================== IN-PLACE SPIKE — EXECUTION PLAN (dry run) ====================
This plan performs NO changes. Review before running ./bootstrap.sh.

Isolated kubeconfig (global ~/.kube/config untouched):
  $KUBECONFIG

Kind management cluster that WILL BE CREATED (and later deleted):
  name: $MGMT_CLUSTER   (node image default for kind)

Protected names this spike will NEVER create/use/delete:
  ${PROTECTED_CLUSTERS[*]}

Container/image dependencies pulled or built on the runner:
  - kind node image (kindest/node) for the mgmt cluster
  - CAPI $CAPI_VERSION components (core, kubeadm bootstrap+controlplane, docker infra) via clusterctl
  - cert-manager (for the test-extension webhook TLS)
  - test-extension image: $EXT_IMAGE
      (built from CAPI_SRC if set: ${CAPI_SRC:-<unset — must provide a prebuilt image or a checkout>})
  - CAPD workload Machines run as Docker CONTAINERS labelled cluster.x-k8s.io/cluster-name=<workload>

Feature gates enabled on CAPI controllers:
  EXP_RUNTIME_SDK=true   EXP_IN_PLACE_UPDATES=true

Scenarios (each from a FRESH workload Cluster; decline runs FIRST — safety gate):
  1. decline            (extension disableInPlaceUpdates=true, KCP maxSurge=0, no spare)
  2. accept             (in-place path; stub fakes the node change — see capability boundary)
  3. update-failure     (extension made UNAVAILABLE mid-update; stock extension has no fail switch)
  4. controller-restart (kill KCP controller pod mid-update; expect idempotent resume)

Cleanup boundary (./teardown.sh):
  - deletes ONLY: kind cluster '$MGMT_CLUSTER' and CAPD containers labelled for its workload
    clusters (verified by cluster.x-k8s.io/cluster-name). Nothing matched by bare name prefix.
  - never deletes containers/clusters it cannot attribute to this spike by label/ownership.

Nothing in this plan targets OpenKubes management clusters, Scaleway bare-metal, or scw1.
==================================================================================
EOF
