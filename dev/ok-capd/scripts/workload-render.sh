#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
ok_capd_require_tools
command -v python3 >/dev/null 2>&1 || ok_capd_fail 'required tool not found: python3'
ok_capd_verify_target
python3 "${OK_CAPD_ROOT}/scripts/render-workload.py"
