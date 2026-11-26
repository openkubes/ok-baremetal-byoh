#!/usr/bin/env bash
# Guard push targets, not local builds. Downloads must still use actual digests.
set -euo pipefail
image=${1:?image required}
expected=${2:?approved target name required}
if [[ ! $image =~ ^ghcr\.io/openkubes/(byoh-controller|no-replacement-guard|inplace-extension):git-[0-9a-f]{12}$ ]]; then
  echo "STOP: push target is not an approved OpenKubes commit-tagged image: $image" >&2
  exit 1
fi
if [[ ${image%:*} != "ghcr.io/openkubes/$expected" ]]; then
  echo "STOP: push target does not match the image being built" >&2
  exit 1
fi
sha=$(git rev-parse --verify HEAD^{commit})
if [[ ${image##*:} != "git-${sha:0:12}" ]]; then
  echo "STOP: push tag does not match the checked-out commit" >&2
  exit 1
fi
