# Local BYOH Kind + CAPI bootstrap (Make)

These commands replay the **manually verified** bootstrap of a separate local
management cluster. They do **not** deploy the BYOH controller, register hosts,
provision Scaleway machines or remove Kubernetes resources. This is a developer
workflow, not a second CAPI lifecycle implementation.

## Isolated bootstrap contract

| Setting | Value |
| --- | --- |
| Kind cluster | `ok-byoh-dev` (default), or `ok-byoh-make-test` for fresh validation |
| Kubernetes node image | `kindest/node:v1.36.4` |
| CAPI Core, kubeadm Bootstrap and Control Plane | `v1.14.4` |
| Private kubeconfig | `~/.config/ok/byoh-dev/kubeconfig` or `~/.config/ok/byoh-make-test/kubeconfig` |
| Kind context | `kind-ok-byoh-dev` or `kind-ok-byoh-make-test` |
| BYOH infrastructure provider | **Not installed** |

This matches the first successful manual run on an Apple Silicon Mac with
Colima / Linux ARM64. `clusterctl` must be on `PATH`; cert-manager is installed
by `clusterctl init`. The image tag is a deliberately fixed tested tag, not an
immutable digest. Before using this workflow as a reproducibility or release
gate, pin and verify an appropriate multi-platform digest.

## First creation on a clean machine

```bash
make byoh-preflight
make byoh-kind-create
make byoh-kind-status
make byoh-capi-init
make byoh-capi-verify
```

**Important:** No target runs `kind delete`, `kubectl delete` or a cleanup
on failure. An interrupted installation must be inspected and recovered
manually; running `byoh-capi-init` again is intentionally rejected once the
CAPI provider inventory or reserved namespaces exist.

## Validation on a separate fresh cluster

Keep the successful `ok-byoh-dev` and historic `ok-bm-bootstrap` unchanged.
The name override is allow-listed to `ok-byoh-make-test`, with a separate
private kubeconfig. Before running creation, check available Colima resources.

```bash
make byoh-preflight BYOH_KIND_NAME=ok-byoh-make-test
make byoh-kind-create BYOH_KIND_NAME=ok-byoh-make-test
make byoh-kind-status BYOH_KIND_NAME=ok-byoh-make-test
make byoh-capi-init BYOH_KIND_NAME=ok-byoh-make-test
make byoh-capi-verify BYOH_KIND_NAME=ok-byoh-make-test
```

Negative tests: run `make byoh-kind-create BYOH_KIND_NAME=ok-byoh-make-test`
and `make byoh-capi-init BYOH_KIND_NAME=ok-byoh-make-test` a second time;
both must exit nonzero **without changing the cluster**.

No automatic teardown is implemented. After explicit review and authorization,
any deletion of the disposable test cluster is a separate manual operation.

## Existing `ok-byoh-dev` (as of 2026-11-18)

The cluster already exists with CAPI installed. Use read-only verification
instead of attempting to recreate or reinstall it:

```bash
make byoh-preflight
make byoh-kind-status
make byoh-capi-verify
```

`make byoh-kind-create` **fails closed** when the name or kubeconfig already
exists. `make byoh-capi-init` **fails closed** when CAPI has already been
initialized. No destructive "reset" target is provided.

## Safety boundaries

- Every Kubernetes command uses the dedicated kubeconfig explicitly; the
  caller's `KUBECONFIG` may still point to Scaleway and must not be used.
- Before any CAPI write, the script verifies the Kind name, Docker context's Kind
  API server, kubeconfig context, node name and Kubernetes version.
- No request is made to `ok-bm-bootstrap` or Scaleway.
- The existing upstream `make test-e2e` is separate and should not be run
  against preserved management clusters.
- The next phase (OpenKubes BYOH installation) is intentionally blocked until
  token idempotence, CSR handling and controller image provenance are verified.

See [the Scaleway end-to-end plan](https://github.com/openkubes/ok-baremetal-byoh/blob/main/docs/scaleway-kubeadm-e2e/README.md) for
later host-registration and workload-cluster phases.
