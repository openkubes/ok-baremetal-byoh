#!/usr/bin/env bash
# Validate the staged bytes before writing the checksums used for the upload.
set -euo pipefail
cd "${1:?bundle directory required}"
sha=${2:?approved source SHA required}
version=${3:?DEB version required}
image=${4:?controller index digest required}
[[ $sha =~ ^[a-f0-9]{40}$ && $image =~ ^ghcr\.io/openkubes/byoh-controller@sha256:[a-f0-9]{64}$ ]]
files=(metadata.yaml infrastructure-components.yaml image-lock.json
       cluster-template.yaml cluster-template-topology.yaml clusterclass-quickstart.yaml
       cluster-template-docker.yaml cluster-template-topology-docker.yaml clusterclass-quickstart-docker.yaml)
for arch in amd64 arm64; do
  bin="byoh-hostagent-linux-$arch"
  deb="byoh-hostagent_${version}_${arch}.deb"
  files+=("$bin" "$deb")
  case "$arch" in
    amd64) machine='Advanced Micro Devices X86-64' ;;
    arm64) machine='AArch64' ;;
  esac
  LC_ALL=C readelf -h "$bin" | grep -E "Machine:[[:space:]]+$machine$" >/dev/null
  grep -aFq -- "$sha" "$bin"
  [[ $(dpkg-deb -f "$deb" Architecture) == "$arch" ]]
  # nFPM encodes a SemVer prerelease separator as a Debian tilde.
  [[ $(dpkg-deb -f "$deb" Version) == "${version/-/\~}" ]]
  work=$(mktemp -d)
  trap 'rm -rf -- "$work"' EXIT
  dpkg-deb -x "$deb" "$work"
  cmp -- "$bin" "$work/usr/bin/byoh-hostagent"
  rm -rf -- "$work"
  trap - EXIT
done
for file in "${files[@]}"; do [[ -f $file && ! -L $file && -s $file ]]; done
mapfile -t rendered < <(awk '$1 == "image:" && $2 ~ /byoh-controller(@|:)/ {print $2}' infrastructure-components.yaml)
[[ ${#rendered[@]} == 1 && ${rendered[0]} == "$image" ]]
if grep -Eiq 'ghcr\.io(:443)?/superbrothers/|ghcr\.io/openkubes/(byoh-controller|no-replacement-guard|inplace-extension):(latest|UNPINNED|dev)([^[:alnum:]_-]|$)' -- ./*.yaml; then
  echo 'STOP: upstream or mutable BYOH image in generated manifests' >&2
  exit 1
fi
[[ $(find . -mindepth 1 -maxdepth 1 -printf x | wc -c) == "${#files[@]}" ]]
sha256sum -- "${files[@]}" > checksums.txt
sha256sum --check --strict checksums.txt
