# Two-host E2E lab: CAPI workload provisioning with Make targets

> **Provider-specific lab record.** This procedure belongs to the OpenKubes two-host E2E
> lab on Scaleway (hosts `ok-bm-lab-01`/`ok-bm-lab-02`). Its `byoh-cluster-*` targets pin
> that lab's host identities and addresses on purpose (fail-closed checks). It is not a
> product requirement; generic host onboarding is in
> [../byoh-host-onboarding.md](../byoh-host-onboarding.md). Moved here from that guide
> unchanged except for its relative links.

## Scaleway lab CAPI control-plane provisioning (read-only plan)

Following successful post-registration bootstrap revocation, reuse the
original KCP template at
`test/e2e/data/infrastructure-provider-byoh/v1beta1/templates/vm/cluster-with-kcp.yaml`.
The first automation stage is strictly **read-only**:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-control-plane-plan
```

The plan restricts itself to the isolated local kind management, checks the
existing unbound control-plane ByoHost and active agent, rejects already
provisioned Kubernetes state, renders the fixed `ok-byoh-scw1`
Kubernetes `v1.36.6` single-control-plane manifest, and performs a Kubernetes
server-side dry-run. It creates no Cluster, KCP, ByoMachine or host label.
`make byoh-hosts-idempotency-test` includes offline manifest rendering tests.

This is **not yet an apply target**: the earlier successful E2E required a
separate Mac-to-CP workload API SSH tunnel and kind-to-workload NAT rewrite.
The current setup has only verified host-to-management tunnels. Before an
authorized apply and agent-mode transition, independently review the actual
present network paths and the CAPI controller's ability to reach the target
workload endpoint, preserving TLS validation. Do not reuse stale port 61599,
workload kubeconfigs, or NAT addresses without checking this cluster.

The separate Scaleway WireGuard/Private Network design and CAPI pivot test
remain deferred.

## CAPI CP apply and workload API network automation (experimental lab)

The pre-existing `byoh-cluster-control-plane-plan` must pass first.
Two Make commands are now available, but **neither has been live tested**
and they MUST NOT be combined with `ok-up`:

```sh
# Explicitly confirmed CAPI control-plane desired state; does not arm agent.
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-control-plane-apply

# Read-only inspection of old SSH/DNAT workaround.
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-network-plan
```

The apply command requires one typed `apply <plan-hash>` confirmation,
rechecks the same target and render digest, labels ONLY the selected
unbound BYOH CP host `role=control-plane` (without overwrite), then
submits the five objects from the existing v1.36.6 KCP template.
After confirmation it safely STOPS the currently running CP enrollment agent\nbefore labelling or creating any CAPI resource. It does not enable the\nagent, install packages, or run kubeadm. If it
partially succeeds, it refuses automatic replay; inspect CAPI resource
state before any manual recovery.

The experimental `byoh-cluster-network-up` target requires another
typed `network <plan-hash>` confirmation. It is restricted to the
`ok-byoh-make-test-control-plane` container and expected Colima context.
It starts a local SSH `127.0.0.1:16444` workload API forward and installs
exact DNAT rules ONLY if the workload API already answers a
kind-to-Mac connectivity probe. **Do not run it before the workload API
exists.** Because the SSH tunnel is started before the TCP probe, an
API-not-ready failure deliberately leaves a local tunnel for manual review,
but does not install DNAT. A foreign port listener, a conflicting DNAT rule,
or a different network identity causes an immediate STOP. This is a
temporary test workaround, not the WireGuard/Private Network design.

The next safety-gated step to implement is a separate CP host
`--skip-installation` release/activation target, conditioned on
ByoMachine binding and an installer Secret; we must NOT simply remove
the agent's bootstrap flags in the current enrolled service. After
kubeadm starts, set up verified workload API access and then Cilium.
No automatic CAPI pivot or unattended cleanup is part of this milestone.

The confirmed CP apply intentionally stops `ok-bm-lab-01`'s agent first, since `--skip-installation` does not guarantee a complete kubeadm block. This means a partial apply requires reviewing the stopped service state; do not attempt to replay the original preflight against an already-created Cluster. Workload API network-up requires the API to be serving and is *not* the next command immediately after apply. A separate verified host-installation arm workflow is still required.

## CAPI control-plane binding and installer verify (read-only)

Following a successful `byoh-cluster-control-plane-apply`, do not repeat
the original create preflight (it intentionally rejects an existing cluster).
Instead, verify the post-apply state without printing bootstrap data:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-control-plane-verify
```

The target discovers the generated CAPI Machine name (no hardcoded random
suffix), asserts the Cluster/KCP/Machine references and v1.36.6 version,
checks the selected ByoHost role and bound ByoMachine, requires the
KubeadmConfig data Secret and corresponding K8sInstallerConfig to exist,
and independently verifies over SSH that the CP agent is inactive and
disabled, with a permanent credential and without pre-existing kubeadm
control-plane state. It performs no writes. Secret *content is never printed*.

Use `make byoh-hosts-idempotency-test` to run associated offline regression
tests for wrong host binding, foreign Machine and missing bootstrap Secret.
The target is a post-apply read-only pre-installation gate, not an installer
and not proof that the network path from management to workload API is ready.

A missing ByoMachine binding or installer may be a controller reconciliation
delay; inspect the precise failing state and controller logs rather than
starting the agent, changing finalizers, or recreating resources.

## Guarded CP install: read-only plan then explicit activation (experimental)

After `byoh-cluster-control-plane-verify` succeeds, the next safety
boundary is the **read-only** installer readiness plan:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-control-plane-install-plan
```

This new workflow is not yet live tested. It checks the current 1-node
CAPI KCP/Machine/ByoMachine/ByoHost binding, matches attachment **UID**
(not only name), checks that `K8sInstallerConfig.status.ready` is true,
and confirms its `status.installationSecret` matches the exact
`ByoHost.spec.installationSecret`. The corresponding Secret must
exist and have nonempty data; no Secret payload is read into log output.
The ByoHost bootstrap Secret reference must also match the
`KubeadmConfig.status.dataSecretName`.

Remote checks require an inactive/disabled agent, valid permanent BYOH
credential file, no temporary enrollment credential, no initialized
kubeadm state, and exactly one known enrollment drop-in at
`90-ok-byoh-enrollment.conf` with expected content and 0644/root
ownership. This is bound to an SSH-observed SHA-256 checksum of that
existing unit, the host UID, ByoMachine UID, installation Secret UID,
inventory bytes and the exact machine identity.

Only after the plan and explicit operator authorization, the **mutating**
target may be used:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-control-plane-install
```

A typed `install <plan-hash>` approval is required. The entire
preflight is re-evaluated before changes. The host-only operation
installs a dedicated `95-ok-byoh-cp-install.conf` systemd drop-in, with
effective `ExecStart=/usr/local/bin/byoh-hostagent --namespace default`,
validates systemd state, and starts the **existing disabled** agent
once. It does not enable it at boot, install packages directly,
replace permanent credentials, approve CSRs, or change CAPI specs.
The agent itself can immediately execute kubeadm work as root, so this
is a high-impact approval boundary. **Never run it unattended.**

On a partial failure do not rerun the original plan: a written 95 drop-in,
started agent, or partially initialized kubeadm leaves a deliberate
fail-closed state. Inspect systemd, the control-plane host, and CAPI
reconciliation before recovery. No automatic rollback or retry.
Existing `network-up` is a separate post-API step and must not be
attempted before the workload API starts.

### Optional ObjectReference fields after API conversion

For a ready K8sInstallerConfig, the BYOH controller copies
`status.installationSecret` into `ByoHost.spec.installationSecret`
using a Kubernetes `corev1.ObjectReference`. The live Scaleway
preflight showed matching Secret names and namespace `default`, but
both references omitted the optional `kind` field. The CP install
plan accepts this legitimate omission only when the exact names and
namespaces agree and the actual core Kubernetes Secret is present.
Explicit foreign `kind`, `apiVersion`, mismatched UID or missing
Secret still cause STOP. The ByoHost bootstrap Secret reference is
checked with the same optional-field semantics and against the
KubeadmConfig Secret name. No secret payload is logged or changed.

## Post-install control-plane health check (read-only)

Once `byoh-cluster-control-plane-install` has started the agent, use:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-control-plane-status
```

The target validates management/CAPI identity and ByoMachine attachment UID;
then via SSH checks the BYOH agent state, installed v1.36.6 packages,
workload API `/readyz`, kubeadm static-Pod manifests, workload Node
identity and Running/Ready control-plane Pods. It classifies a single
`NotReady` Node and pending CoreDNS as `WAIT_CNI` rather than treating
pre-CNI status as a failed kubeadm install. Neither journal output (which
could include kubeadm join tokens) nor Secret payloads are printed.
No writes occur on management, workload or Scaleway hosts.

This is a **development-phase status target**, not a production workflow
or single-command install. A later orchestrator should use these private
checks under an inventory-driven `ok up` operation; the individual
`plan`, `verify` and `install` steps remain available for diagnosing
fail-closed trust boundaries. The superbrothers BYOH provider already
automates package installation and kubeadm for attached Machines; it
does not create a remote networking/management infrastructure for the
operator or install CNI as an all-in-one bare-metal command.

## Workload Cilium 1.19.6 (Scaleway lab only)

The existing E2E Cilium guidance is in
[cilium.md](cilium.md). To avoid manual Helm commands
against an ambiguous context, these Make targets use the existing
management CAPI identity and a freshly obtained **workload** admin
credential from the verified control-plane host:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-cilium-plan
```

The read-only plan requires the exact chart at
`~/temp/kubernauts/ok/ok-cluster/.tools/cilium-1.19.6.tgz` with
SHA-256 `21c43cf53841f9ab0375047d95aa4c64051ea52bbd2c679416e6408f5f1c9179`.
It validates the initialized CAPI v1.36.6 KCP, that the local
`127.0.0.1:16444` tunnel is present, retrieves the *current*
`/etc/kubernetes/admin.conf` over checked SSH and uses a temporary
mode-0600 kubeconfig with `tls-server-name=51.158.62.217` to validate
workload API TLS trust and exact Node identity. No kubeconfig, keys,
tokens or Secret contents appear in output.

If an existing `~/.config/ok/byoh-scw1/kubeconfig-tunnel` does not
match the current CP credential and CA **exactly**, the operation
fails closed: archive and inspect that stale file manually rather than
silently overwrite it. Similarly, a pre-existing or partial Cilium
release is **not** automatically upgraded or repaired.

Only after an explicit review and typed confirmation:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-cilium-install
```

The installer re-evaluates its digest-bound plan, stores the fresh
workload-specific kubeconfig with private permissions, then uses
`helm install` (not `upgrade --install`) with
`kubeProxyReplacement=false`, `operator.replicas=1`,
`k8sServiceHost=51.158.62.217`, `k8sServicePort=6443`
and `--wait --timeout 5m`. The target is not yet live-tested.

Read-only post-install check:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-cluster-cilium-verify
```

This checks the Helm release/values, Cilium DaemonSet and Operator
Deployment, and Node/CoreDNS readiness. It does **not** replace a
cross-node data-plane/policy test after the worker joins. A real
cluster needs stable private management networking; the local
SSH+kind-NAT workaround remains disposable lab-only infrastructure.
