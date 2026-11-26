# Release the OpenKubes BYOH provider

[OK-241 packaging acceptance](https://kubernauts.atlassian.net/browse/OK-241?focusedCommentId=15452).
The trusted main workflow is the only release-image producer. It checks out the
successful CI run's full source SHA and publishes Linux amd64/arm64 builds of:

- `ghcr.io/openkubes/byoh-controller`
- `ghcr.io/openkubes/no-replacement-guard`
- `ghcr.io/openkubes/inplace-extension`

Its Actions artifact `byoh-image-lock-<full-sha>-attempt-<attempt>` records the
producer run and Buildx output digests, including both runnable platform digests.
The version-tag workflow does not build or push images. It validates that lock
against GHCR and embeds the Controller index digest in
`infrastructure-components.yaml`. The other two image pins are recorded in the
lock; use them only with a reviewed overlay for a supported profile that requires
those components. Do not install the raw lab overlay or broaden the GA profile.

## Tagless dry run

Use a disposable clean Linux clone with Git, Docker/Buildx, Go from `go.mod`,
Python 3, gh (read access), jq, dpkg-deb, binutils and coreutils. Choose a
maintainer-approved full candidate SHA, intended version and successful main
producer run ID/attempt for that exact candidate. These positional shell
variables are inputs, not defaults:

```bash
git switch --detach "$BYOH_RELEASE_COMMIT"
work=$(mktemp -d)
bash hack/release/fetch-image-lock.sh "$BYOH_RELEASE_COMMIT" \
  "$BYOH_IMAGE_RUN_ID" "$BYOH_IMAGE_RUN_ATTEMPT" "$work/lock"
bash hack/release/package.sh "$BYOH_RELEASE_COMMIT" "$VERSION" \
  "$BYOH_IMAGE_RUN_ID" "$BYOH_IMAGE_RUN_ATTEMPT" "$work/lock/image-lock.json" "$work/bundle"
(cd "$work/bundle" && sha256sum --check --strict checksums.txt)
```

Set `VERSION` to the intended `vX.Y.Z` (or valid prerelease). The packager builds
from a clean isolated checkout, passing the version explicitly so no local or
remote version tag is needed. It only reads GHCR and builds local files. It
rejects missing platforms, upstream/mutable BYOH images, and mismatched source,
run or digests. It checks the architecture and source SHA of the standalone Host
Agent executables, DEB metadata, and byte equality of packaged and standalone
executables on both architectures. The bundle includes six templates,
`metadata.yaml`, the provider manifest, the image lock, both binaries and both
DEBs. Keep the accepted `checksums.txt`, version, SHA, run ID/attempt, and
validation evidence together.

## Clean install and release gates

1. In a fresh disposable supported management cluster, validate the approved
   bundle with the documented installation path and no fallback to upstream.
   If using clusterctl, configure a local provider repository/override pointing
   at the exact staged version; ensure `metadata.yaml` maps the selected
   major/minor to the supported `v1beta1` contract. Only add a series for a new
   major/minor. For a public release, verify that a clean unauthenticated
   consumer can pull all required OpenKubes digests.
2. Verify BYOH CRDs, controller and webhooks are ready; compare observed image
   IDs with the lock's platform digests. For a supported profile that uses
   Guard/Runtime Extension, verify its reviewed digest-pinned overlay and
   readiness. Install the checked DEB on one fresh supported host of each
   architecture; verify agent version/source and systemd operation, perform
   explicit CSR approval, confirm host readiness, and provision/smoke-test a
   workload. Record exact versions, checksums, and results.
3. Confirm all remaining OK-241 release gates separately: the independently
   reviewed and tested MR !16, the independent repeated five-host lifecycle
   including the separately installed hard-clean tool, and the reviewed
   Advanced Guide and supported profiles.
4. Only after separate authorization, a maintainer pushes the protected
   version tag. Use an annotated tag (signed when required by release policy)
   with these exact lines in its message, substituting approved values:
   ```text
   BYOH_RELEASE_COMMIT=<full BYOH source SHA>
   BYOH_IMAGE_RUN_ID=<successful main publisher run ID>
   BYOH_IMAGE_RUN_ATTEMPT=<publisher attempt number>
   ```
   The tag's peeled commit and checked-out source must equal the approved SHA;
   the recorded publisher attempt must be successful and have exactly one
   matching image lock. The workflow does not itself verify the tag signer's
   identity: retain the repository's protected tag policy and maintainer approval.
5. Download the draft bundle, check every file against `checksums.txt`, and
   compare against the accepted dry-run checksums and digest pins. If bytes
   differ, stop and explicitly re-verify the exact draft assets. Only after
   separate publication approval publish the draft and the announcement.
   Image aliases, signed provenance and SBOMs are independent enhancements.
