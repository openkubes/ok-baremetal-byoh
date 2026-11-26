#!/usr/bin/env bash
# The protected annotated tag records the maintainer's explicit inputs.
set -euo pipefail
ref=${1:?tag ref required}
[[ $ref == refs/tags/v* && $(git cat-file -t "$ref") == tag ]]
annotation=$(git cat-file tag "$ref" | sed '1,/^$/d')
input() {
  local key=$1
  local -a values
  mapfile -t values < <(printf '%s\n' "$annotation" | sed -n "s/^$key=//p")
  [[ ${#values[@]} == 1 ]]
  printf '%s' "${values[0]}"
}
sha=$(input BYOH_RELEASE_COMMIT)
run=$(input BYOH_IMAGE_RUN_ID)
attempt=$(input BYOH_IMAGE_RUN_ATTEMPT)
[[ $sha =~ ^[a-f0-9]{40}$ && $run =~ ^[1-9][0-9]*$ && $attempt =~ ^[1-9][0-9]*$ ]]
[[ $(git rev-parse "$ref^{commit}") == "$sha" && $(git rev-parse HEAD^{commit}) == "$sha" ]]
printf '%s %s %s\n' "$sha" "$run" "$attempt"
