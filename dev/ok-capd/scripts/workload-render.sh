#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
ok_capd_require_tools
ok_capd_verify_target
python3 "${OK_CAPD_ROOT}/scripts/render-workload.py"
