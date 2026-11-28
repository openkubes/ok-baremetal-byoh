#!/usr/bin/env bash
# Fail closed on a published release, an existing draft (including partial
# uploads), or an unavailable/malformed release listing. Never mutate assets.
set -euo pipefail
tag=${1:?tag required}
repo=openkubes/ok-baremetal-byoh
token=${GH_TOKEN-}
unset GH_TOKEN
if ! pages=$(GH_TOKEN="$token" gh api --paginate "repos/$repo/releases?per_page=100"); then
  echo "STOP: cannot verify existing releases for $tag" >&2
  exit 1
fi
if ! state=$(jq -ser --arg tag "$tag" '
  if type != "array" or length == 0 or (all(.[]; type == "array") | not) then error("invalid release pages")
  else [ .[][] | if (.tag_name | type) != "string" or (.draft | type) != "boolean"
                 then error("invalid release entry") else . end | select(.tag_name == $tag) ]
  | if any(.[]; .draft == false) then "published" elif length > 0 then "draft" else "absent" end
  end' <<< "$pages"); then
  echo "STOP: cannot classify release state for $tag" >&2
  exit 1
fi
case "$state" in
  absent) ;;
  published) echo "STOP: a published release already exists for $tag" >&2; exit 1 ;;
  draft) echo "STOP: an existing draft for $tag requires manual reconciliation" >&2; exit 1 ;;
  *) exit 1 ;;
esac
