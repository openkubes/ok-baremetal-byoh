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

## Idempotency test gate

Run `make byoh-hosts-idempotency-test` for deterministic offline tests of request
freshness, management CA/endpoint binding, transfer checksum replay decisions,
host-inventory plan fingerprints and systemd unit drift/no-op decisions.

**Live idempotency is not yet proven.** The enrollment preflight now supports
an explicit staged mode; an existing remote bootstrap credential is verified
by SHA-256 and reused without transfer, and an already-correct agent systemd
drop-in is left unchanged without a daemon reload. These changes are not yet
E2E-tested against Scaleway.

Do not rerun enrollment while bootstrap tokens are stale. The test cluster
currently has tokens with a 30-minute TTL and a conservative 20-minute
freshness bound; a repeat after that bound must fail closed until an explicit
revocation and new issuance procedure is completed.

The final E2E acceptance test must compare the token Secret identity and
expiration, remote credential hashes, systemd drop-in timestamps, effective
systemd state, and CSR count before and after a second run. Partial failure
replay, simulated interruptions and multi-host failure isolation still need
integration tests. No CSR auto-approval or agent start has been introduced.

## Bootstrap renewal before initial host registration

If the previous 30-minute bootstrap tokens have expired before the agents
were started, use the separate **pre-registration-only** renewal workflow:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-hosts-bootstrap-renew
```

The command checks the complete inventory and kind-API tunnel identity,
requires stopped/disabled agents, no permanent kubeconfigs, no registered
ByoHost, no enrollment CSRs, matching current BootstrapKubeconfig API/CA,
matching remote credential SHA-256, and a persisted token revocation finalizer.

A plan listing all hosts and the operation is displayed before any changes.
One `renew <plan-hash>` confirmation authorizes old object deletion (waits
for token revocation finalizer), removal of that host's exact matching staged
credential, and fresh bootstrap creation/transfer using the existing helper.
New credentials are checksum-verified on each host. No CSR is approved and
no agent is started. Existing host agent systemd settings remain unchanged.

**Recovery limit:** Renewal is not yet a transactional multi-host operation.
After partial success, do not blindly rerun: some hosts may already have new
tokens. Inspect each BootstrapKubeconfig UID and created time, remote
credential SHA-256, and token Secret/finalizer state before choosing an
explicit recovery action. The command does not renew already-registered hosts.
The token TTL is still 30 minutes; changing it to a configurable 24 hours is
a separate planned change.

**Testing status:** Newly implemented; syntax, sandbox tests, and the actual
Scaleway renewal still require validation before treating this as a proven
unattended runbook.
