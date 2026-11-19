# OpenKubes Scaleway CAPI/BYOH E2E kit

**Status:** lab kit based on the successful interactive run on 19 November 2026. **Not committed to the user's repository, not tested against a live cluster from this environment.** The commands were derived from the terminal transcripts; verify against current repository APIs before using for a new cluster.

## Proven result

- Management: `kind-ok-byoh-make-test` on Mac/Colima, CAPI and forked BYOH controller.
- Workload: `ok-byoh-scw1`, kubeadm/Kubernetes `v1.36.6`, Ubuntu 24.04.5 LTS, containerd `2.3.7`.
- Control plane: `ok-bm-lab-01` / `51.158.62.217` (CAPI Running, Kubernetes Ready).
- Worker: `ok-bm-lab-02` / `51.158.62.216` (MachineDeployment `ok-byoh-scw1-md-0`: desired/current/ready/available/up-to-date = 1, Kubernetes Ready).
- Cilium `v1.19.6` installed from previously SHA-256 verified local chart, with `kubeProxyReplacement=false` because kubeadm installed kube-proxy.
- Cluster `Available=True`, CP desired/available=1/1, workers desired/available=1/1.
- No server reprovisioning, RKE2 installation, OpenTofu or Scaleway firewall changes during this test.

**Scope of proof:** successful provisioning, join and readiness. Not yet proven: full Cilium cross-node connectivity/network policy, service workloads, failure recovery, stable management connectivity, high availability, move/pivot, safe unattended host onboarding.

## Design boundaries

1. BYOH handles host identity, binding, installation and kubeadm bootstrap; Kubernetes components do **not** include a CNI installation in this fork's reviewed Go/YAML/shell file search.
2. CAPI owns `KubeadmControlPlane` and `MachineDeployment`. Workload add-ons (Cilium) should be managed by OpenKubes, not embedded in the agent.
3. Bootstrap credentials: per host, short-lived `BootstrapKubeconfig`; review CSR (`CN=byoh:host:default:<host>`, `O=byoh:hosts`, client-auth, correct signer and bootstrap group); approve **only intentionally**; delete bootstrap resource and remove remote bootstrap credential when permanent client kubeconfig is present. Avoid printing `.status.bootstrapKubeconfigData` or token-bearing Secrets.
4. Do not automatically retry `kubeadm init` / `join`, `clusterctl move`, or cleanup on real hosts. Do not expose the workload API publicly merely to make lab reconciliation work.
5. The worker systemd unit must set `WorkingDirectory=/root` for the agent's relative `.byoh/config` path; `--skip-installation` only prevents installer setup, **not all kubeadm actions**.

## One-time prerequisites on the operator Mac

Run in `~/temp/kubernauts/ok/ok-baremetal-byoh` (where the templates and agent binary exist). Requires `kubectl`, `ssh`, `scp`, `envsubst`, `openssl`, `base64`, and `helm` for the CNI step. Configure SSH access to both servers. Use explicit addresses for `scp` (shell alias `sshscw2` may not be a real SSH hostname).

Management kubeconfig: `~/.config/ok/byoh-make-test/kubeconfig`.
Workload kubeconfig: `~/.config/ok/byoh-scw1/kubeconfig-tunnel`, which must reference the HTTPS API through the **existing, trusted** SSH local tunnel. Do not copy a privileged kubeconfig into the repository or commit it.

From the lab repo root:

```sh
./scripts/scaleway-e2e/byoh-e2e.sh status
./scripts/scaleway-e2e/byoh-e2e.sh verify
```

> The helper is intentionally phase-based. It does **not** execute destructive provisioning end-to-end unattended. This avoids inadvertently bootstrapping or deleting an already provisioned host. The current live cluster is already healthy; do **not** rerun the create/enroll/apply/start phases on it.

## Reprovisioning a **new** test environment (not the existing healthy cluster)

1. Prepare clean Ubuntu 24.04 AMD64 hosts, RKE2 absent, kubeadm/containerd ports available. Confirm network security and routing before provisioning.
2. Provision kind Management Cluster and install CAPI/BYOH provider with the fork's existing deployment procedure. Verify controller with `MANUAL_CSR_APPROVAL=enable` if manual CSR approval is intended.
3. Establish **two distinct network paths** described in [`docs/network-paths.md`](network-paths.md). Verify TLS and reachability; ensure future workers reach the CP API `:6443` and Cilium cross-node connectivity.
4. Bootstrap/enroll **each** host individually. For a worker, the kit can automate safe preparation while keeping explicit approvals:

   ```sh
   ./scripts/scaleway-e2e/byoh-e2e.sh agent-install
   ./scripts/scaleway-e2e/byoh-e2e.sh enroll-create
   ./scripts/scaleway-e2e/byoh-e2e.sh enroll-transfer
   # On worker, after verifying it is unbound: start enrollment-mode agent
   ssh ubuntu@51.158.62.216 'sudo /usr/local/bin/byoh-hostagent --namespace default --bootstrap-kubeconfig /root/.byoh/bootstrap.kubeconfig --skip-installation'
   # In a separate terminal:
   ./scripts/scaleway-e2e/byoh-e2e.sh inspect-csr
   ./scripts/scaleway-e2e/byoh-e2e.sh approve-csr
   # Stop the foreground enrollment agent after obtaining permanent /root/.byoh/config.
   ./scripts/scaleway-e2e/byoh-e2e.sh enroll-revoke
   ```

   **Note:** The enrollment-mode agent stays in the foreground; do not proceed to approval in that same terminal. Avoid invoking it after host binding. The helper installs and enables the systemd unit but does not start it.
5. Create the **control plane** with the fork's existing `cluster-with-kcp.yaml` flow (not generated by this kit). Specify a **stable, reachable** `controlPlaneEndpoint` and a host selector matching its ByoHost. After CAPI binds the host and the installation Secret is present, release/start the CP agent. For single-CP lab only, the observed endpoint was `51.158.62.217:6443`.
6. Install Cilium after the control plane initializes (see [`docs/cilium.md`](cilium.md)). Confirm the CP Node `Ready` before worker rollout.
7. Define the worker in one declaration using existing `vm/md.yaml`:

   ```sh
   ./scripts/scaleway-e2e/byoh-e2e.sh render-worker
   # Verify worker systemd agent stopped, own host enrolled; network prerequisites satisfied.
   ./scripts/scaleway-e2e/byoh-e2e.sh apply-worker
   # Wait for host binding; check resulting ByoMachine and worker installation Secret.
   ./scripts/scaleway-e2e/byoh-e2e.sh agent-arm-worker
   ./scripts/scaleway-e2e/byoh-e2e.sh verify
   ```

   `apply-worker` checks direct worker-to-CP API reachability, stops short of kubeadm execution and asks for an explicit confirmation. `agent-arm-worker` verifies an actual worker binding and permanent client credentials before starting a host-mutating installer. Review the generated YAML before applying.
8. Capture non-secret proof:

   ```sh
   ./scripts/scaleway-e2e/capture-evidence.sh /tmp/ok-byoh-scaleway-e2e-evidence
   ```

   Review host addresses/IPs before sharing evidence publicly.

## Observed blockers and fixes

| Symptom | Root cause and fix |
|---|---|
| ByoMachine not bound | A prior merge patch retained an unintended `kubernetes.io/hostname` selector. Remove exactly that JSON Patch key; target `role=scaleway-control-plane` or `role=scaleway-worker` as intended. Do not assume merge patches replace maps. |
| BYOH agent systemd restart loop: `stat .byoh/config` | Set `WorkingDirectory=/root` in systemd unit; retain `/root/.byoh/config` mode and ownership. Do not repeat CSR enrollment. |
| `Machine InfrastructureReady=False`, `ByoMachine status.initialization.provisioned` unset despite Node `Ready` | Controller cannot reach workload API (`https://51.158.62.217:6443`). Fix management-to-workload reachability before suspecting kubeadm. ProviderID and CAPI reconciliation then converged. |
| `Node NotReady`, `NetworkPluginNotReady` | kubeadm does not install CNI. Install compatible verified Cilium chart; ensure kube-proxy replacement settings match cluster mode. |
| `scp: Could not resolve hostname sshscw2` | `sshscw2` was a shell alias/function, not an SSH-config hostname. Use `ubuntu@51.158.62.216` for scp. |

## Follow-up work for production-grade `ok` CLI

- Idempotent `ok byoh hosts enroll` with explicit per-host CSR approval and revocation, hardened service units, actionable diagnostics.
- Management-to-workload network contract, no ad hoc SSH/NAT; strict ACLs, dual-path readiness, cert trust, endpoint stability.
- CAPI MachineDeployment/Worker Topology templates with immutable selectors and controlled scaling.
- Automatic, digest-pinned Cilium install and day-2 lifecycle with CAPI add-on contract.
- Evidence receipts, bounded retries, stateful resume, safe rollback; preserve infrastructure ownership and never mutate OpenTofu unrequested.
- Separate 3-CP+2-worker HA test with stable API VIP/LB, control-plane cert distribution, etcd quorum tests.
- `clusterctl move` evaluated only after providers, object graph and move semantics have been verified, in an isolated management namespace.
- Migrate deprecated CAPI `v1beta1` `MachineDeployment` and `KubeadmConfigTemplate` APIs to supported versions in a separate change.

## Automation validation status

**Status: Offline validation completed; live automation
replay not yet performed.**

The successful Scaleway E2E was performed interactively.
The imported automation scripts are based on that run.

### Verified offline

- Shell syntax checks
- Worker replica parameter validation (0, invalid, 3)
- Multi-worker mutation guard
- Missing kubeconfig rejection
- CSR rejection with invalid mocked metadata
- Bootstrap credential transfer using kubectl/SSH mocks
- Credential file permissions (0600)
- Atomic no-clobber credential installation
- Temporary credential cleanup
- Evidence incomplete-run detection

These tests do not establish that all provisioning,
enrollment, agent activation or recovery paths are
fully idempotent.

### Current limitations

- Automated worker onboarding supports one worker.
- Multiple worker replicas can be rendered, but applying
  them requires future host-inventory validation.
- Control-plane HA is not validated.
- ClusterClass is available in the BYOH fork but is
  outside the scope of this automation integration.
- Same-Plane pivot is not implemented by this kit.
- CSR authorization and host identity binding require
  additional review before unattended operation.
- Agent installation and activation require explicit
  authorization and recovery procedures.
- The full credential-transfer flow has been tested
  with mocks, not against live hosts.

### Protected running environment

The existing cluster `ok-byoh-scw1` must remain unchanged
during this integration.

Preserve all existing management/workload SSH tunnels,
DNAT rules and communication paths.

No Scaleway or OpenTofu changes are authorized.

Do not execute mutating E2E commands against the
existing healthy cluster.

### Release scope

This integration provides a documented, controlled
lab reference workflow. It is not an unattended
production provisioning implementation.

The long-term target remains integration with the
OpenKubes `ok` CLI, with CAPI owning cluster lifecycle
and OpenKubes retaining preflight, authorization
and evidence responsibilities.
