#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  inplace-upgrade)
    echo "NOT IMPLEMENTED: RuntimeSDK in-place upgrade scenarios are not wired into ok-capd yet." >&2
    ;;
  no-replacement)
    echo "NOT IMPLEMENTED: No-Replacement Guard scenarios are not wired into ok-capd yet." >&2
    ;;
  *)
    echo "Usage: $0 {inplace-upgrade|no-replacement}" >&2
    ;;
esac
exit 2
