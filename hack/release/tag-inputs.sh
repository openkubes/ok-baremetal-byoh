#!/usr/bin/env bash
# Parse an annotated tag; parsing does not establish authorization or protection.
set -euo pipefail
fail() { printf 'STOP: %s\n' "$*" >&2; exit 1; }
ref=${1:?tag ref required}
[[ $ref == refs/tags/v* ]] || fail "expected a release tag ref"
[[ $(git cat-file -t "$ref") == tag ]] || fail "expected an annotated tag"
if ! annotation=$(git cat-file tag "$ref"); then
  fail "expected an annotated tag"
fi
[[ $annotation == *$'\n\n'* ]] || fail "missing tag annotation"
declare -A values=()
while IFS= read -r line; do
  [[ $line != '-----BEGIN PGP SIGNATURE-----' && $line != '-----BEGIN SSH SIGNATURE-----' ]] || break
  if [[ $line =~ ^[[:space:]]*(BYOH_RELEASE_COMMIT|BYOH_IMAGE_RUN_ID|BYOH_IMAGE_RUN_ATTEMPT)($|[[:space:]=:]) ]]; then
    key=${BASH_REMATCH[1]}
    [[ $line == "$key="* ]] || fail "malformed tag annotation: $key"
    [[ ! ${values[$key]+set} ]] || fail "duplicate tag annotation: $key"
    values[$key]=${line#*=}
  fi
done <<< "${annotation#*$'\n\n'}"
for key in BYOH_RELEASE_COMMIT BYOH_IMAGE_RUN_ID BYOH_IMAGE_RUN_ATTEMPT; do
  [[ ${values[$key]+set} ]] || fail "missing tag annotation: $key"
done
sha=${values[BYOH_RELEASE_COMMIT]}
run=${values[BYOH_IMAGE_RUN_ID]}
attempt=${values[BYOH_IMAGE_RUN_ATTEMPT]}
[[ $sha =~ ^[a-f0-9]{40}$ ]] || fail "malformed tag annotation: BYOH_RELEASE_COMMIT"
[[ $run =~ ^[1-9][0-9]*$ ]] || fail "malformed tag annotation: BYOH_IMAGE_RUN_ID"
[[ $attempt =~ ^[1-9][0-9]*$ ]] || fail "malformed tag annotation: BYOH_IMAGE_RUN_ATTEMPT"
[[ $(git rev-parse "$ref^{commit}") == "$sha" && $(git rev-parse HEAD^{commit}) == "$sha" ]] ||
  fail "tag, approval and checkout commits differ"
printf '%s %s %s\n' "$sha" "$run" "$attempt"
