#!/usr/bin/env bash
# Reject a legacy default even when a caller supplies it explicitly.
set -euo pipefail
image=${1:?image required}
case "${image,,}" in
  ghcr.io/superbrothers/*|ghcr.io:443/superbrothers/*)
    echo "refusing to push to the upstream BYOH registry: $image" >&2
    exit 1
    ;;
esac
