#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

printf '=== OpenKubes CAPD: complete local setup ===\n'
printf 'Management cluster: %s\n' "$OK_CAPD_KIND_NAME"
printf 'Workload cluster: %s\n' "$OK_CAPD_WORKLOAD_NAME"
printf '\n[1/4] Bootstrap or verify the management cluster\n'
bash "$OK_CAPD_ROOT/scripts/up.sh"

printf '\n[2/4] Verify CAPI/CAPD controllers and Docker socket\n'
bash "$OK_CAPD_ROOT/scripts/test.sh"

printf '\n[3/4] Create or verify the CAPD workload cluster\n'
bash "$OK_CAPD_ROOT/scripts/workload-up.sh"

printf '\n[4/4] Show final management and workload status\n'
bash "$OK_CAPD_ROOT/scripts/status.sh"
bash "$OK_CAPD_ROOT/scripts/workload-status.sh"

printf '\nPASS: local CAPD management and workload clusters are ready.\n'
printf 'Management kubeconfig: %s\n' "$OK_CAPD_KUBECONFIG"
printf 'Workload kubeconfig: %s/workloads/%s.kubeconfig\n' "$OK_CAPD_CONFIG_DIR" "$OK_CAPD_WORKLOAD_NAME"
printf '\nNOTE: this setup installs CAPI + CAPD, not the OpenKubes BYOH controller.\n'
