# Upstream baseline and synchronization

`openkubes/ok-baremetal-byoh` is the OpenKubes-maintained Cluster API **Bring Your Own Host (BYOH)**
infrastructure provider. It is a downstream fork that tracks upstream closely, keeps divergence
minimal, and contributes fixes back where possible.

## Pinned upstream baseline

| | |
|---|---|
| Upstream source | [`superbrothers/cluster-api-provider-bringyourownhost`](https://github.com/superbrothers/cluster-api-provider-bringyourownhost) |
| Baseline commit | [`5ea71bf4ed5d7e592d66252d62c1613334c296af`](https://github.com/superbrothers/cluster-api-provider-bringyourownhost/commit/5ea71bf4ed5d7e592d66252d62c1613334c296af) |
| Baseline subject | "Merge pull request #50 from superbrothers/drop-unneeded-settings" (2026-11-08) |
| Import method | Exact-history import — the complete upstream Git history (809 commits, back to the 2021 initial commit) was pushed unchanged to `main`; `main` HEAD equals the baseline commit SHA. No squash, no history rewrite. |

## Fork lineage and attribution

```
vmware-tanzu/cluster-api-provider-bringyourownhost   (original project, Apache-2.0; no longer maintained — issue #894)
        └── superbrothers/cluster-api-provider-bringyourownhost   (maintained fork; CAPI v1.14)
                └── openkubes/ok-baremetal-byoh   (this repository)
```

The original project was created by the VMware Tanzu team as a Cluster API sub-project and is
licensed under the Apache License 2.0. All upstream licensing and copyright notices are preserved
unchanged in this fork:

- `LICENSE` — Apache License 2.0 (unmodified).
- `NOTICE` — upstream notice (unmodified).
- Per-file copyright headers (e.g. `Copyright 2021/2022 VMware, Inc.`,
  `Copyright 2026 The Cluster API Provider BringYourOwnHost Authors`) are retained.

OpenKubes additions carry their own headers; they do not replace upstream copyright.

## Git remotes

```bash
git remote add origin   https://github.com/openkubes/ok-baremetal-byoh
git remote add upstream https://github.com/superbrothers/cluster-api-provider-bringyourownhost
# prevent accidental pushes to upstream:
git remote set-url --push upstream DISABLED_no_push
```

## Synchronizing with upstream

```bash
git fetch upstream
git switch main

# While main still equals the upstream baseline (no OpenKubes commits yet):
git merge --ff-only upstream/main

# Once OpenKubes commits exist on main, prefer one of:
git merge upstream/main        # keeps OpenKubes commits; resolve conflicts, then PR
# or, for a linear fork line, rebase the OpenKubes-only commits onto upstream/main.
```

**Contribution policy.** Fixes that are not OpenKubes-specific (for example, general security
hardening) are opened as pull requests against the upstream `superbrothers` repository first, then
pulled back via the `upstream` remote. This keeps divergence minimal and is the purpose of the fork.

**Known future divergence.** A later change migrates the Go module path to
`github.com/openkubes/ok-baremetal-byoh` (tracked separately; pending a reviewed decision). That
rename touches every Go import and will cause recurring merge conflicts on import lines during
upstream synchronization — the known, accepted cost of a fork-owned module path. It is kept as a
single isolated commit to make replaying it on upstream merges straightforward.

**Explicitly not imported.** Modifications from the `mensylisir` fork are **not** carried into this
repository. In particular, OpenKubes preserves upstream's separation between BYOH infrastructure
provisioning and kubeadm machine bootstrap, and does not adopt `mensylisir`'s repurposing of the
`BootstrapKubeconfig` CRD as a machine bootstrap provider (which introduced a bootstrap-token
reconciliation loop). See the OpenKubes assessment `reviews/byoh-provider-evaluation-and-bootstrap-token-bug.md`.
