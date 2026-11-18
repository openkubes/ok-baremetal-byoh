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
  reference and substitutes a local, revision-tagged image. It rejects
  missing or duplicated expected placeholders, unresolved placeholders
  and any remaining upstream image.
- `MANUAL_CSR_APPROVAL=enable` is mandatory: this disables the
  automatic BYOH CSR approval controller. The naming is counterintuitive.
- Required runtime options are replaced with explicit values. The deploy
  command checks the image is in the kind node and runs a client dry-run
  before the write. The verification checks deployment image, manual CSR
  mode, rollout and controller service-account permissions in kube-system.
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
