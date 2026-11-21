#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ok_capd_require_tools
ok_capd_verify_target
printf '=== CAPD workload report (read-only) ===\n'
ok_capd_kubectl get clusters.cluster.x-k8s.io -A
printf '\nNo resources were deleted or stopped.\n'
printf 'This target is informational only. Use ok-capd-workload-down to delete the disposable workload cluster; it preserves the management cluster.\n'
