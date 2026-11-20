#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
ok_capd_verify_target
printf '=== CAPD workload clusters (read-only) ===\n'
ok_capd_kubectl get clusters.cluster.x-k8s.io -A
printf '\nNo resources were deleted or stopped.\n'
printf 'This target intentionally preserves the management cluster. Workload-cluster shutdown semantics will be added with the first CAPD workload lifecycle test.\n'
