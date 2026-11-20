# ADR-001: BYOH No-Replacement Safety Contract

- Status: Proposed
- Date: 2026-11-20
- Scope: OpenKubes BYOH Kubernetes lifecycle
- Applies to: in-place-only BYOH clusters
- CAPI reference: v1.14.4

## Context

OpenKubes manages existing physical hosts through Cluster API
and the Bring Your Own Host infrastructure provider.

For in-place-only BYOH upgrades, replacing a CAPI Machine
is not an acceptable fallback.

A disposable CAPD experiment with CAPI v1.14.4 demonstrated
that a KubeadmControlPlane upgrade from v1.33.0 to v1.34.0
can create a replacement Machine and delete the original.

CAPI source inspection confirmed:

- KCP RollingUpdate may scale up before attempting in-place updates.
- `CanUpdateMachine=false` falls back to scale-down.
- The fallback may delete the original Machine.
- A successful KCP rollout does not prove Machine identity preservation.

The CAPD experiment proves replacement behavior, but does
not independently establish the exact runtime-hook response.

## Decision

An in-place-only BYOH upgrade MUST preserve:

1. The original CAPI Machine UID.
2. The infrastructure Machine identity.
3. The assigned physical host identity.
4. The expected control-plane and worker topology.

An upgrade MUST NOT authorize replacement implicitly.

The following conditions MUST stop the upgrade rather
than trigger Machine replacement:

- In-place update is unsupported.
- The eligibility check rejects the update.
- A required runtime extension is unavailable.
- An update step fails or times out.
- Upgrade evidence cannot be verified.

## Enforcement boundaries

### Upgrade preflight

The upgrade controller MUST validate the requested target,
supported upgrade path, cluster identity, host inventory,
Machine identity, and authorization before changing desired
Kubernetes versions.

### Independent admission guard

A fail-closed admission mechanism MUST protect in-place-only
clusters from unauthorized lifecycle mutations.

The guard MUST address:

- Machine CREATE requests introducing replacement Machines.
- Machine DELETE requests.
- Identity-changing Machine UPDATE requests.
- Removal or alteration of protection metadata.
- Changes to relevant owner references.
- Unauthorized changes to KCP or MachineDeployment intent.
- BYOH infrastructure-object deletion or reassignment.
- Subresource and admission-bypass scenarios where applicable.

Protection MUST be derived from a trusted cluster-scoped
policy, not solely from labels supplied by a new Machine.

### Explicit lifecycle authorization

Provisioning, decommissioning, and approved replacement
are separate operations.

An upgrade authorization MUST NOT double as permission
to replace or decommission a Machine.

An authorized exception MUST be narrowly scoped,
auditable, time-bounded, and bound to the exact operation
and affected identities.

### Failure behavior

Webhook unavailability MUST fail closed for protected
operations.

An admission denial MUST surface an actionable reason.

The system MUST NOT report a successful upgrade merely
because CAPI eventually becomes Available.

## Evidence and acceptance criteria

The implementation MUST demonstrate:

1. Baseline Machine and physical-host identities recorded.
2. Unauthorized replacement CREATE denied.
3. Unauthorized Machine DELETE denied.
4. Protection-label removal or mutation denied.
5. Unauthorized ownership and infrastructure reassignment denied.
6. Admission webhook outage fails closed.
7. Explicitly authorized lifecycle operations remain possible.
8. In-place upgrade preserves original identities.
9. Audit evidence distinguishes requests, denials, and outcomes.
10. A refused in-place upgrade leaves original Machines intact.

A blocked replacement attempt is a successful safety test,
but it is not a successful Kubernetes upgrade.

## Implementation approach

The initial implementation will reuse the existing BYOH
controller-runtime admission infrastructure where practical.

Admission rules for core CAPI resources require their own
explicit webhook registrations and Kubernetes RBAC review.

Before enabling protection on real hosts, the guard will
be verified in an isolated disposable management cluster.

Webhook deployment, CA injection, matching rules, failure
policy, and bypass conditions must be tested end-to-end.

## Limitations

Kubernetes admission alone cannot prevent direct host
access, privileged changes outside the API server, or
destructive actions performed through other infrastructure
control planes.

Those boundaries require separate authorization and
operational controls.

## Validation

The original CAPD experiment is retained as negative
evidence demonstrating the unsafe fallback.

A subsequent isolated test MUST verify that the same
replacement path is blocked under the new contract.

No production or Scaleway hosts are in scope for
the initial admission regression tests.
