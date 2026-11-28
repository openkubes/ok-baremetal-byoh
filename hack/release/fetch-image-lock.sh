#!/usr/bin/env bash
# Download artifacts only from the explicitly selected successful main producer.
set -euo pipefail
repo=openkubes/ok-baremetal-byoh
token=${GH_TOKEN-}
unset GH_TOKEN
read_gh() { GH_TOKEN="$token" gh "$@"; }
sha=${1:?approved full SHA required}
run=${2:?producer run ID required}
attempt=${3:?producer attempt required}
out=${4:?output directory required}
[[ $sha =~ ^[a-f0-9]{40}$ && $run =~ ^[1-9][0-9]*$ && $attempt =~ ^[1-9][0-9]*$ ]]
name="byoh-image-lock-${sha}-attempt-${attempt}"
read_gh api "repos/$repo/actions/runs/$run/attempts/$attempt" |
  jq -e --arg repo "$repo" --arg run "$run" --arg attempt "$attempt" '
    (.id | tostring) == $run and (.run_attempt | tostring) == $attempt and
    .repository.full_name == $repo and .head_repository.full_name == $repo and
    .path == ".github/workflows/publish-ghcr.yml" and .head_branch == "main" and
    .event == "workflow_run" and .status == "completed" and .conclusion == "success"
  ' >/dev/null
if ! read_gh api --method GET "repos/$repo/actions/runs/$run/artifacts" -f name="$name" |
  jq -e --arg name "$name" '.total_count == 1 and (.artifacts | length) == 1 and
    .artifacts[0].name == $name and .artifacts[0].expired == false' >/dev/null; then
  echo "STOP: exact image lock is missing, expired, ambiguous or inaccessible: $name" >&2
  exit 1
fi
mkdir -- "$out"
read_gh run download "$run" --repo "$repo" --name "$name" --dir "$out"
[[ -f "$out/image-lock.json" ]]
