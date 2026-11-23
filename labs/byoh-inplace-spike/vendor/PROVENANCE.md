# vendored test-extension manifest — PROVENANCE

**Artifact:** `test-extension-v1.14.4.yaml`
**Purpose:** pre-rendered upstream CAPI test-extension manifest so the BYOH CAPD spike needs no full `cluster-api` checkout.

## Source
- Repository: `github.com/kubernetes-sigs/cluster-api`
- Tag: **v1.14.4** (annotated tag object `23803d4eacc3daaffbf5f70032aeedaa2235c155`)
- Commit: **4356c154f4a76b8939748ef7ebc2d0793ffe47e8** (`git describe --tags --exact-match` == v1.14.4)
- Path: `test/extension/config/default`

## Renderer
- Tool: **kustomize v5.5.0** (standalone; `kustomize build`)
- Rendered (UTC): 2026-11-21T02:34:55Z
- Command: `kustomize build test/extension/config/default`
- Note: `bootstrap.sh` uses `kubectl kustomize`; this artifact is the contract, and this exact kustomize version reproduces it byte-for-byte.

## Integrity
- **ext_manifest_sha256 (sha256 of this vendored file, header included):**
  `f5eae26658b9f62edc09e1ea726210d73fc34aa9dc9004f7561d89c3c37484ff`
- Verify (macOS/Linux): `echo "f5eae26658b9f62edc09e1ea726210d73fc34aa9dc9004f7561d89c3c37484ff  test-extension-v1.14.4.yaml" | shasum -a 256 -c -`

## Rendered resources (actual output, not assumed)
- Certificate  test-extension-system/test-extension-serving-cert
- ClusterRole  -/test-extension-manager-role
- ClusterRoleBinding  -/test-extension-manager-rolebinding
- Deployment  test-extension-system/test-extension-controller-manager
- Issuer  test-extension-system/test-extension-selfsigned-issuer
- Namespace  -/test-extension-system
- Role  test-extension-system/test-extension-leader-election-role
- RoleBinding  test-extension-system/test-extension-leader-election-rolebinding
- Service  test-extension-system/test-extension-webhook-service
- ServiceAccount  test-extension-system/test-extension-manager

## Compatibility notes (verified against the spike)
- Deployment `test-extension-controller-manager`, container **`manager`** → matches the `yq` image patch in `bootstrap.sh`.
- Certificate `test-extension-serving-cert` → secret **`test-extension-webhook-service-cert`** → matches `extensionconfig.yaml` `inject-ca-from-secret`.
- Service **`test-extension-webhook-service`** port **443** → matches `extensionconfig.yaml` clientConfig.
- Preserved placeholders (resolved by `bootstrap.sh`): `${CAPI_DIAGNOSTICS_ADDRESS:=:8443}`, `${CAPI_INSECURE_DIAGNOSTICS:=false}`, `${EXP_PRIORITY_QUEUE:=true}`; image placeholder `gcr.io/k8s-staging-cluster-api/test-extension:main` (patched to $EXT_IMAGE).

## Regeneration
```bash
git clone --depth 1 --branch v1.14.4 https://github.com/kubernetes-sigs/cluster-api /tmp/capi
kustomize build /tmp/capi/test/extension/config/default > body.yaml   # kustomize v5.5.0
# prepend the provenance header (see this file), then:
shasum -a 256 test-extension-v1.14.4.yaml
```
