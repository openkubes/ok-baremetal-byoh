# Reproducible BYOH controller on local kind

This workflow is for disposable, local development only. It automates the
already-verified sequence: kind v1.36.4, CAPI v1.14.4, local BYOH controller
build, direct kind image load, safe manifest rendering, deployment and checks.

## One command

```bash
make byoh-kind-up
```

Allowed targets are `ok-byoh-dev` (default) and `ok-byoh-make-test`
(`make byoh-kind-up BYOH_KIND_NAME=ok-byoh-make-test`).
An existing kind cluster is **never deleted or recreated**. Existing CAPI is
verified rather than reinitialized. Partial/unrecognized CAPI states fail
closed; fix them explicitly before retrying.

Individual commands:

```bash
make byoh-kind-status
make byoh-capi-verify
make byoh-controller-build
make byoh-controller-load
make byoh-controller-render
make byoh-controller-deploy
make byoh-controller-verify
make byoh-controller-test
```

Requires Docker (the same context kind uses), kind, kubectl, clusterctl,
Go, Python 3 and GNU Make. `make kustomize` provides Kustomize v5.7.0.
The controller build requires a **clean Git worktree**, and uses an image
tag derived from the 12-character Git commit SHA. No registry push or
external server provisioning is performed.

## Controller image sources

The default image source is `local`. It builds the controller from
the current Git revision and imports the image into the kind node.

The alternative `ghcr` source installs a previously published
OpenKubes controller image pinned to an OCI SHA-256 digest.

### Local mode (default)

Run `make byoh-kind-up`.

This builds the controller locally and runs `kind load`.

### GHCR mode

Set these environment variables:

- `BYOH_CONTROLLER_SOURCE=ghcr`
- `BYOH_CONTROLLER_DIGEST=sha256:<64 lowercase hex characters>`

For the published Git revision `11e32536ac51`, the verified
multi-architecture OCI index digest is:

`sha256:c7766a4e27ef250880683ff55bfa7a58e6ce2e95850141a1b949f7f0de95c9f7`

Then run `make byoh-kind-up`.

The GHCR mode skips the local controller build and `kind load`.
Kubernetes retrieves the image from GHCR when it is not cached.

For an existing initialized kind cluster, use
`make byoh-controller-deploy` followed by
`make byoh-controller-verify`.

Missing or malformed digests, image tags and unsupported sources
are rejected. The manifest renderer permits only the expected
OpenKubes GHCR repository with a complete SHA-256 digest.

### Verification boundaries

The GHCR deployment has been tested on an existing kind cluster.
Controller readiness, runtime image ID, manual CSR approval mode
and repeated deployment without a new Deployment revision
have been verified.

A fresh kind-cluster installation without a preloaded image
remains to be tested.

The runtime image ID check does not independently establish
cryptographic image provenance or verify a platform-specific
manifest against the published OCI index.

The existing cluster is never automatically destroyed.
All operations remain limited to disposable local kind
development environments.

## Safety and contract

- Kubernetes operations use only
  `~/.config/ok/byoh-dev/kubeconfig` or
  `~/.config/ok/byoh-make-test/kubeconfig`; the ambient
  `KUBECONFIG` and prompt context are ignored.
- The pre-existing `hack/dev/byoh-kind.sh` verifier checks the kind
  name, API server address, context, single expected node and version.
- Controller deployment is built from `config/default` in memory/temp
  files, **without** changing `config/manager/kustomization.yaml`.
- The manifest renderer permits exactly one expected upstream image
  reference and substitutes either a local, revision-tagged image or a
  digest-pinned image from the OpenKubes GHCR repository. It rejects
  missing or duplicated expected placeholders, unresolved placeholders
  and any remaining upstream image.
- `MANUAL_CSR_APPROVAL=enable` is mandatory: this disables the
  automatic BYOH CSR approval controller. The naming is counterintuitive.
- Required runtime options are replaced with explicit values. The deploy
  command checks that locally built images are loaded in local mode and
  runs a client dry-run before the write. In GHCR mode Kubernetes handles
  image retrieval. Verification checks deployment image, manual CSR mode,
  rollout and controller service-account permissions in kube-system.
  GHCR mode additionally verifies the Ready pod and runtime image ID.
- Repeating the workflow does not create extra kind clusters, reinitialize
  CAPI, or mint bootstrap-token Secrets. It does recheck/reapply BYOH
  management-plane manifests.
- No host enrollment, kubeadm node installation, Scaleway, OpenTofu,
  RKE2, or production deployment is part of this workflow.

### Security limitations

The controller service account still has broad cluster-scoped permissions,
including Secret access and CSR approval capabilities. Manual CSR mode
disables a controller code path; it does not remove those permissions.
Least-privilege RBAC and explicit enrollment authorization remain production
gates. The current unit tests do not establish token-authentication denial
after revocation. Review `docs/bootstrap-token-security.md` before any
non-lab deployment.

The cluster-scoped resources (CRDs, RBAC, webhook) require administrative
credentials. `kubectl apply --dry-run=client` is not an authorization proof.
`byoh-kind-up` is an explicit opt-in deployment command and must only be
run against an isolated local kind environment.

## Verification and troubleshooting

```bash
make byoh-controller-test
make byoh-controller-verify
KUBECONFIG="$HOME/.config/ok/byoh-dev/kubeconfig" \
  kubectl -n byoh-system logs deployment/byoh-controller-manager --tail=100
```

Do not delete kind clusters or re-run `clusterctl init` to work around
verification failures. Check the reported mismatch first.
