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

A fresh `git clone` of this repository already provides `origin`, so only the
`upstream` remote needs to be added. The commands below are idempotent:

```bash
# origin already exists from `git clone`; verify it rather than re-adding:
git remote get-url origin        # -> https://github.com/openkubes/ok-baremetal-byoh

# add upstream if missing, otherwise update its URL:
git remote get-url upstream >/dev/null 2>&1 \
  && git remote set-url upstream https://github.com/superbrothers/cluster-api-provider-bringyourownhost \
  || git remote add    upstream https://github.com/superbrothers/cluster-api-provider-bringyourownhost

# prevent accidental pushes to upstream:
git remote set-url --push upstream DISABLED_no_push
```

## Synchronizing with upstream

Upstream changes are integrated on a dedicated sync branch and merged into `main`
only through a reviewed pull request — never by pushing to `main` directly. This
preserves the OpenKubes review and merge-approval policy (Claude implements,
ChatGPT reviews, Arash approves).

```bash
git fetch upstream

# integrate upstream on a dedicated sync branch, based on the current origin/main:
git switch -c sync/upstream-$(date +%Y%m%d) origin/main
git merge upstream/main          # resolve conflicts here
                                 # (import-line conflicts are expected after the module-path rename)

# for a linear fork line, instead rebase the OpenKubes-only commits onto upstream/main
# on this branch before pushing.

git push -u origin sync/upstream-$(date +%Y%m%d)
# then open a PR  sync/upstream-<date> -> main , review (three-way), and Arash merges.
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
reconciliation loop). This rationale is recorded in OpenKubes' internal BYOH provider assessment,
maintained in the OpenKubes project workspace (not part of this repository).
