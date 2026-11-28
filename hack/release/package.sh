#!/usr/bin/env bash
# Shared tagless/draft path. No git tag, push, image publication or release calls.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
sha=${1:?approved full SHA required}
version=${2:?version with v prefix required}
run=${3:?producer run ID required}
attempt=${4:?producer attempt required}
lock=$(realpath -e -- "${5:?image lock required}")
out=$(realpath -m -- "${6:?new output directory required}")
[[ $sha =~ ^[a-f0-9]{40}$ && $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]
[[ $(git rev-parse HEAD^{commit}) == "$sha" && -z $(git status --porcelain) ]]
[[ ! -e "$out" ]]
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
cp -- "$lock" "$work/image-lock.json"
image=$(python3 hack/release/image-lock.py verify "$sha" "$run" "$attempt" "$work/image-lock.json")
git clone -q --shared --no-checkout "$root" "$work/src"
cd "$work/src"
git -c advice.detachedHead=false checkout -q --detach "$sha"
[[ $(git rev-parse HEAD^{commit}) == "$sha" && -z $(git status --porcelain) ]]
# Build once per architecture before any tracked manifest/template edits.
export GIT_VERSION="$version" SOURCE_DATE_EPOCH
SOURCE_DATE_EPOCH=$(git show -s --format=%ct HEAD)
make build-host-agent-binary host-agent-debs HOST_AGENT_ARCHS='amd64 arm64' DEB_VERSION="${version#v}"
make build-cluster-templates build-infra-yaml build-metadata-yaml IMG="$image"
cp -- "$work/image-lock.json" _dist/image-lock.json
bash hack/release/verify-bundle.sh _dist "$sha" "${version#v}" "$image"
mkdir -- "$out"
cp -a _dist/. "$out/"
