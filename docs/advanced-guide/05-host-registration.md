# 05 – Host Registration

Register only trusted Linux hosts that pass the infrastructure readiness checks. A registered host provides capacity; it is not yet a Kubernetes node. Use a dedicated namespace and explicit management context throughout this guide.

## Prepare the agent

Obtain the OpenKubes agent package or binary from the approved artifact set for the host architecture. Record source commit, SHA-256 and the installed executable identity. Use the OpenKubes downstream agent release — the provider and agent built from an approved OpenKubes revision (release process in [../release.md](../release.md)) — and do not substitute a plain upstream Superbrothers agent build when OpenKubes extensions are required. Note that `v0.6.0` is an OpenKubes release version, not an upstream tag: the pinned upstream baseline is a commit, `5ea71bf` (see [../../UPSTREAM.md](../../UPSTREAM.md)). The exact downstream distribution path remains subject to packaging validation. For a verified DEB, inspect it with `dpkg-deb --info "$AGENT_DEB"`, compare its SHA-256 with the approved lock, and install using `sudo apt install "$AGENT_DEB"` (absolute path). Do not start the service until enrollment is prepared.

## Establish bootstrap credentials

Use Bash and set `MGMT_KUBECONFIG`, `MGMT_CONTEXT`, `NS`, `HOST` explicitly. The namespace must already exist and the hostname must match the intended host. Define a convenience function:

```bash
mg() { kubectl --kubeconfig "$MGMT_KUBECONFIG" --context "$MGMT_CONTEXT" "$@"; }
mg get namespace "$NS"
```

Verify the API endpoint and CA against independently approved values. Set `APISERVER` to the host-reachable TLS endpoint and `CA_CERT` to the base64-encoded trusted CA certificate data. Do not disable TLS verification. Render and review one resource per host:

```bash
cat <<YAML
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: BootstrapKubeconfig
metadata:
  name: bootstrap-kubeconfig-${HOST}
  namespace: ${NS}
spec:
  apiserver: "${APISERVER}"
  certificate-authority-data: "${CA_CERT}"
YAML
```

After review, apply the rendered manifest with the explicit management context. The controller creates a bootstrap token valid for 30 minutes. The token is not bound to the intended host or namespace; treat it as a sensitive bearer credential.

```bash
umask 077
mg -n "$NS" get bootstrapkubeconfig "bootstrap-kubeconfig-$HOST" \
  -o jsonpath='{.status.bootstrapKubeconfigData}' > bootstrap.conf
test -s bootstrap.conf
```

Transfer the file securely only to the intended host, using verified SSH host identity or an equivalent trusted channel. Never commit it or include it in logs. On the host, install it with mode 0600 under `/etc/byoh-hostagent/bootstrap-kubeconfig`. Configure `/etc/default/byoh-hostagent` with the intended namespace and role, for example `BYOH_HOSTAGENT_ARGS="--namespace=lab --label=role=worker"`. Then enable/start `byoh-hostagent` only after the operator authorizes enrollment.

## Manual CSR approval

Select `MANUAL_CSR_APPROVAL=enable` when configuring the provider. Verify this behavior before enrollment. The CSR name is `byoh-csr-<namespace>.<hostname>`.

```bash
CSR="byoh-csr-${NS}.${HOST}"
mg get csr "$CSR" -o jsonpath='{.spec.username}{"\n"}{.spec.groups}{"\n"}{.spec.signerName}{"\n"}{.spec.usages}{"\n"}'
mg get csr "$CSR" -o jsonpath='{.spec.request}' | base64 -d | openssl req -noout -text -verify
```

Check the requester against the issued bootstrap token, signer `kubernetes.io/kube-apiserver-client`, client authentication usages, signed request, exact CN `byoh:host:<namespace>:<hostname>`, organization `byoh:hosts`, absence of SANs, and expected physical/virtual host identity. Deny unexpected requests. Only after the explicit approval gate may the authorized administrator run `mg certificate approve "$CSR"`. Kubernetes signs the approved request; the agent stores its private key and certificate in `/root/.byoh/config`.

## Verify capacity and retire bootstrap credentials

```bash
mg -n "$NS" get byohosts
mg -n "$NS" describe byohost "$HOST"
```

Confirm namespace, hostname, labels, agent information and network addresses. An unassigned host has no `spec.byoMachineRef`. Remove the temporary bootstrap kubeconfig from the host and operator staging area when no longer required; delete its BootstrapKubeconfig through the authorized lifecycle and verify token Secret revocation and finalizer completion. Do not bypass a stuck finalizer. Renewal requires a new bootstrap credential and agent restart under the documented current model.

## Troubleshooting and security boundaries

Inspect `systemctl status byoh-hostagent`, `journalctl -u byoh-hostagent`, CSR conditions and ByoHost events. Check clock synchronization, TLS endpoint, hostname uniqueness and token expiry before retrying. Do not delete existing host credentials or re-enroll an attached host as a routine repair. Host agents run as root and current documented RBAC allows broad Secret reads; namespaces are not tenant security boundaries. Removing local credentials does not revoke a previously issued certificate. Use trusted hosts only.

Reference: [Host registration and permissions](../host_registration.md).
