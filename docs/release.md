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
4. Only after an administrator has verified the required protections below
   and separately authorized tagging, an approved maintainer pushes the version tag. Use an annotated tag (signed when required by release policy)
   with these exact lines in its message, substituting approved values:
   ```text
   BYOH_RELEASE_COMMIT=<full BYOH source SHA>
   BYOH_IMAGE_RUN_ID=<successful main publisher run ID>
   BYOH_IMAGE_RUN_ATTEMPT=<publisher attempt number>
   ```
   The tag's peeled commit and checked-out source must equal the approved SHA;
   the recorded publisher attempt must be successful and have exactly one
   matching image lock. The workflow does not itself verify the tag signer's
   identity: verify the proposed protections are active and record maintainer approval.
5. Download the draft bundle, check every file against `checksums.txt`, and
   compare against the accepted dry-run checksums and digest pins. If bytes
   differ, stop and explicitly re-verify the exact draft assets. Only after
   separate publication approval publish the draft and the announcement.
   Image aliases, signed provenance and SBOMs are independent enhancements.

## Required repository protections (proposal, not applied or verified)

**No-go until verified.** Before authorizing any release tag, an authorized
administrator must approve the configuration below, read back the active
repository and inherited rules, and record the ruleset IDs, checks and release
team membership in OK-241. A valid annotation proves neither permissions nor
the signer's identity. This repository's live protections were not verified by
this PR. Do not assume the rules exist, and do not test them by pushing a
production-pattern tag.

| Proposed active ruleset | Ref condition | Rules | Bypass |
| --- | --- | --- | --- |
| BYOH main review and CI (branch) | include `refs/heads/main`; no exclusions | Restrict deletions; block force pushes; require PR with at least 1 approval, dismiss stale approvals, require approval of the most recent reviewable push by someone other than its pusher, and require conversation resolution. Require branches up to date and the GitHub Actions checks `verify`, `build-agent (amd64)`, `build-agent (arm64)`, `build-image` and `test` from workflow **CI**. | None, including admins and apps. |
| BYOH release creation (tag) | include `refs/tags/v*.*.*`; no exclusions | Restrict creations. | **Always** for exactly the organization team approved to create BYOH releases (proposed name: `byoh-release-maintainers`; administrator must resolve/approve the team ID and membership). No other bypass actors. |
| BYOH release tag immutability (tag) | include `refs/tags/v*.*.*`; no exclusions | Restrict updates and deletions. | None. The creation team's bypass does not apply to this separate ruleset. |

In Rulesets, select check source **GitHub Actions**, not "any source"; confirm
the exact check contexts from an eligible PR run before activating. For the
REST ruleset configuration use `target: branch` or `target: tag`,
`enforcement: active`, `conditions.ref_name` with these includes and empty
excludes; the creation-only bypass uses `actor_type: Team`, its approved
numeric `actor_id`, and `bypass_mode: always`. See the
[GitHub ruleset API](https://docs.github.com/en/rest/repos/rules#create-a-repository-ruleset).
No repository settings change is part of the packaging PR.

## Eligible source, retention and safe retry

- Choose a full SHA reviewed and approved as the release source, reachable
  from protected `main`, with successful CI, the reviewed packaging workflow
  and a successful main publisher attempt containing the unique, unexpired
  lock for that exact SHA. Record the source, producer run/attempt, lock hashes,
  digests and acceptance evidence together. The tagless check revalidates the
  producer and all digests; never recover from a short Git tag or Docker tag.
- Docs-only pushes can skip main CI, even though docs-only PRs now run the
  required PR checks. If a chosen docs-only SHA has no lock, stop: choose and
  explicitly approve an eligible SHA, or separately approve a new
  packaging-relevant commit through normal review/CI. Never borrow the nearest
  successful image lock, even when the diff is documentation only.
- Work within the producer's 90-day lock retention (check the actual artifact
  expiry). If missing, expired or ambiguous, stop. A separately approved rerun
  of the trusted producer is possible only if it still uses the exact candidate
  CI source; re-approve its new attempt and digests and repeat validation. If
  that run is unavailable, select a new eligible SHA. Keep copies for audit,
  but do not use a local copy to bypass the workflow's provenance check.
- Workflow writes for a tag serialize. Immediately before draft creation the
  workflow rejects any matching published release **or** existing draft,
  including a failed attempt with partial assets. No asset is overwritten.
  If failure happened before any release existed, rerun **all** jobs (not only
  failed jobs) to generate and verify an attempt-specific staged bundle. If a
  draft exists, stop automated retries: a maintainer must verify every asset
  and checksum before approving a complete draft, or separately authorize
  manual recovery of an incomplete draft. Never move, delete or reuse a
  published tag to recover. Do not run manual/API release writers concurrently;
  GitHub does not provide an atomic check-and-create here. Enable immutable
  published releases if available as a separate, human-approved safeguard.

## Build input limitations

The release job pins [setup-go v7.0.0](https://github.com/actions/setup-go/releases/tag/v7.0.0)
by exact commit and reads `go.mod`. The existing agent Docker build and the
controller Dockerfiles still resolve a version-tagged Go base, not a pinned
OCI index. Before opting into digest pinning, verify the official
`docker.io/library/golang:<toolchain>` index and both platform descriptors,
then update the reference and redo the clean-build matrix together with each
toolchain bump. Do not invent a digest or pin one architecture's manifest for
both builds. This PR does not change the base image; source locking and asset
checksums do not prove fully reproducible or hermetic builds. Existing runner,
tool and module availability, and anonymous GHCR reads remain environment
requirements. `GH_TOKEN` is scoped to the two GitHub API steps, not passed to
the Docker/Go packaging subprocesses.
