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

## Host registration: explicit start, independent CSR approval

After bootstrap renewal and successful agent preparation, first run the read-only
preflight for the registration phase:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-hosts-register-plan
```

Once the plan is reviewed, run the same command with `byoh-hosts-register`
to request a single explicit start confirmation.

The registration command first validates all inventory hosts (management CA/tunnel, host identity,
active/disabled service, effective `--skip-installation` systemd command,
permanent-credential absence, and remote credential checksum against the
current fresh BootstrapKubeconfig), and prints a host-batched digest-bound
plan. **The command starts services only after a single typed `start <hash>`
confirmation.** Check the plan before authorizing. It revalidates the
selected host immediately before each start. It does NOT approve a CSR,
provision Kubernetes nodes or modify the infrastructure.

Do **not** run this target against already-started/partially registered hosts
as a retry. After a partial start, review the host service state and CSRs
independently before choosing an explicit recovery procedure. CSR approval
requires an independently reviewed requester/token identity contract;
`scripts/scaleway-e2e/byoh-e2e.sh approve-csr` remains disabled by design.

The existing 30-minute token TTL and 20-minute conservative start-age
restriction remain. If tokens have expired, first use the separately confirmed
`byoh-hosts-bootstrap-renew` on **stopped, unregistered** hosts.

## Bootstrap agent startup regression (2026-11-19)

The agent source in `agent/main.go` only enters initial certificate
bootstrap when `--bootstrap-kubeconfig` is explicitly passed and
`~/.byoh/config` does not exist. The `--skip-installation` flag only
disables Kubernetes installation; it does not enable bootstrap.

Both lab agents were observed restarting with `client config load failed`
and a missing `/root/.byoh/config`. They were stopped and remain disabled.

The enrollment-mode systemd drop-in now requires:

```ini
[Service]
WorkingDirectory=/root
ExecStart=
ExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation --bootstrap-kubeconfig=/root/.byoh/bootstrap.kubeconfig
```

Apply it only with `byoh-hosts-agent-prepare` while both agents are stopped,
the management identity is verified, and tokens are fresh. The preparation
verifies the installed binary's `--help` exposes that flag; it does not
start services. The registration preflight requires the effective exact
command. The start workflow checks agents remain active after an eight-second
observation; this is not proof of successful CSR enrollment.

The next live test must **not** reuse the previous `byoh-hosts-register`
result. Review the token age, perform an authorized bootstrap renewal if
needed, and then re-run the read-only registration plan before a fresh start.
CSR issuance, identity and authorization remain separate safety gates.

## Read-only N-host CSR identity verification

Once the agents are running and the CSRs have appeared, execute:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-hosts-csr-verify
```

The validator checks each CSR's name, requester, signer, groups, usages,
pending state, X.509 request signature and precise subject. It independently
derives the expected bootstrap token ID from the BootstrapKubeconfig UID
(the first six hexadecimal characters of SHA-256), checks the matching
`kube-system/bootstrap-token-<ID>` Secret and request UID annotation,
verifies unexpired token data matches the bootstrap kubeconfig, and compares
its SHA-256 to the credential file on the corresponding SSH host. It prints
only non-secret host identifiers, token IDs and CSR UIDs, **not credentials**.

The derived UID/token relationship is necessary but not sufficient proof of
physical host identity: access to an enrollment token could allow a claimant
to submit a CSR. Independent SSH/host provenance and an authorized approver
remain required. The tool never approves or signs certificates. If a token
expires, the check stops; do not renew tokens for an agent already running
with an outstanding CSR without an explicit recovery review.

Run `make byoh-hosts-idempotency-test` for offline CSR/identity regressions;
these tests are not live E2E approval evidence.

## CSR approval: SSH private-key challenge and optimistic concurrency

After `byoh-hosts-csr-verify` passes, the independent approval proof uses the
agent's temporary private key at `/root/byoh-client.key.tmp`, created by
`agent/registration/csr.go` while the agent waits for a certificate.

Run the read-only plan first:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-hosts-csr-approve-plan
```

For every inventoried host this checks the previous CSR/token/Secret bindings,
the pending CSR UID/resourceVersion and a fresh random nonce signed on the
SSH host, verified against the public key in the CSR. The private key never
leaves the host, and token/CSR payloads are not displayed. A successful SSH
challenge proves possession of the matching key on the authenticated SSH
endpoint, **not** a hardware-backed attestation of the bare-metal machine.

Once the owner has independently confirmed SSH host provenance, inventory,
management CA and plan contents, the mutating command is:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-hosts-csr-approve
```

This asks once for `approve <plan-hash>`, then rechecks all host keys,
credential bindings and CSR identity. Each approval PUT targets the Kubernetes
`certificatesigningrequests/<name>/approval` subresource with the **exact
CSR UID and resourceVersion** from that final read. Replacement, approval or
another status update causes a fail-closed conflict. A multi-host approval is
not transactional: if one later host fails, inspect already-approved CSRs
before retrying. Never approve unmatched or unexpectedly changed CSRs.

The approval target does not start agents or provision Kubernetes. After
successful approval, separately verify certificate issuance, permanent
`/root/.byoh/config` and ByoHost registration before revoking bootstrap
tokens. Existing bootstrap TTL remains 30 minutes. Do not rotate tokens of
running agents with pending CSRs merely because the TTL has expired.

**Testing status:** Newly implemented and unverified against live Scaleway
until the offline tests and the read-only approval plan have passed. Use
`make byoh-hosts-idempotency-test` for the regression suite before executing
the mutating approval target.

## Post-approval host registration verification (read-only)

After confirmed CSR approval and certificate issuance, run:

```sh
make BYOH_KIND_NAME=ok-byoh-make-test \
  BYOH_HOSTS_INVENTORY=config/hosts/scaleway.yaml \
  byoh-hosts-register-verify
```

This requires the expected approved and issued CSR for every inventory host,
verifies the issued certificate subject and public key against its CSR, checks
the expected registered unbound `ByoHost`, and reads each host's permanent
`/root/.byoh/config` over authenticated SSH without displaying credentials.
It validates 0600/root ownership, a non-token TLS client identity, public key
versus private key consistency, and the certificate's SHA-256 fingerprint
against the certificate issued for that exact CSR. Agents must be active.

The remote check relies only on Python 3 and OpenSSL rather than an installed
`kubectl` on the bare-metal hosts. It expects the agent-generated canonical
kubeconfig serialization, not arbitrary alternative YAML representations.
It performs NO revocation, service restart, CSR approval, host binding or
Kubernetes install. A failed check must be investigated before modifying
bootstrap credentials.

**Revocation remains separate.** Only after all hosts pass and the operator
authorizes it should we delete the exact owned BootstrapKubeconfig resources
(waiting for token finalizers and Secret deletion), then remove only matching
remote bootstrap files. Do not use the old pre-registration renewal target
against registered hosts.
