# BYOH host onboarding: staged enrollment

The local management plane is bootstrapped as before:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test ok-up
```

This does **not** contact remote hosts. Host inventory files (`config/hosts/*.yaml`) are local and ignored by Git. Copy `config/hosts/my-provider-hosts-inventar.tpl` and fill in the addresses.

To stage fresh bootstrap credentials for both hosts using the existing checked helper:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  BYOH_ENROLL=true ok-up
```

The `BYOH_ENROLL=true` path first establishes/verifies local management via `byoh-kind-up`, then verifies both SSH paths and TLS peer identity, confirms both hosts have stopped/disabled agents and retired credentials, verifies no existing bootstrap objects, then invokes existing `scripts/scaleway-e2e/byoh-e2e.sh` `enroll-create` and `enroll-transfer` for each host. Every creation and transfer requires a host-specific typed confirmation.

**Scope limitation:** This is *bootstrap preparation*, **not completed BYOH registration**. No BYOH agent is started. No CSR is approved. After staging, the workflow checks each existing unit and requires a separate host-specific confirmation to install a safe systemd enrollment drop-in with `WorkingDirectory=/root` and `--skip-installation`. Bootstrap freshness and management binding are rechecked. Agent identity/requester/token authorization must still be independently inspected before starting the agents. The command stops at that trust boundary.

The workflow resumes an existing BootstrapKubeconfig only if it matches the current management CA and forwarded API endpoint and is younger than 20 minutes. It waits up to 90 seconds for the controller to populate the status. Remote transfer still refuses to overwrite an existing credential. On expired, mismatched or partially transferred state, stop for an explicit recovery/revocation review; do not automatically recreate tokens or delete resources.

A direct entrypoint, useful after management is already up:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-hosts-enroll
```

The host preflight remains read-only. The bootstrap preparation target creates cluster BootstrapKubeconfig resources and transfers temporary credentials to the hosts. No OpenTofu or Scaleway infrastructure changes are performed.

When credentials are **already staged**, do not rerun `byoh-hosts-enroll` or `ok-up BYOH_ENROLL=true`: the bootstrap preflight is intentionally for clean hosts. Instead, while both bootstrap resources are fresh, run:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-hosts-agent-prepare
```

This command verifies the stopped/disabled services, matching management context and bootstrap target, checks freshness, and asks for two explicit host-specific confirmations before applying systemd drop-ins. It does not start or enable services. After 20 minutes from creation the bootstrap resources are considered stale for this workflow; do not override this guard. Perform an explicit revocation/reissue review instead.
