# Open Security Questions for the Codex Review (Phase 0)

Two selector-bypass questions must be reviewed explicitly. Both concern the fact
that admission webhook selectors decide whether the API server INVOKES the guard
at all — a check inside the handler is worthless if the handler is never called.

## Q1 — namespace-label bypass (namespaceSelector)
The Machine/MachineSet/ByoMachine webhooks route only to namespaces labelled
`safety.openkubes.org/in-place-only=true` (namespaceSelector). Removing that label
from a protected namespace is a separate Namespace UPDATE that the workload
webhooks never see; afterwards Machine DELETE/UPDATE in that namespace is no
longer routed to the guard — a bypass.

Status / smallest safe mitigation (no new infrastructure):
- Primary control is RBAC: protected namespaces and their labels are platform-
  managed; tenants must NOT hold `patch namespaces`. The trusted registry
  (`/etc/openkubes/no-replacement/registry.json`, not cluster-mutable) remains the
  Cluster-UID authority; the label is only a routing filter.
- Optional defense-in-depth on the SAME guard server (one extra path, not new
  infra): a Namespace UPDATE/DELETE webhook that denies removal of
  `in-place-only=true` for any namespace present in the trusted registry.
Open question for Codex: add the Namespace-protection webhook in Phase 0, or rely
on RBAC and defer it? (It is NOT built yet.)

## Q2 — grant-ConfigMap objectSelector bypass (webhook-invocation gap)
The grant-ConfigMap webhook previously carried
`objectSelector: matchLabels{safety.openkubes.org/grant: "true"}`. That selector
decides whether the API server CALLS the webhook. If a grant object is created
WITHOUT the label, or the label is removed, the API server does not invoke the
webhook at all — so the handler's name-based recognition (`okx-grant-*`) and its
label-removal denial never execute. **The handler-level name check alone is
insufficient when the webhook is not invoked.** This is the sharper framing of
the M3 finding.

Applied fix (smallest safe solution, config only — see
`config/webhook/no-replacement/pending-m6-webhooks.yaml`):
- REMOVE the objectSelector from the grant-ConfigMap webhook. The webhook now
  fires for EVERY ConfigMap CREATE/UPDATE/DELETE in the (dedicated, few)
  protected namespaces, so it is always invoked for any object that could be a
  grant — including one with no label or a removed label.
- The handler (`GrantConfigMapAdmissionHandler`) recognizes a grant by name OR
  label (`IsGrantObject`) and PASSES THROUGH ordinary ConfigMaps (Allowed), so
  the broader invocation does not disturb non-grant ConfigMaps.
- Net effect: name-based recognition + label-removal denial are now reachable,
  closing the evasion. Cost: with `failurePolicy: Fail`, ConfigMap writes in a
  protected namespace fail-closed while the guard is down — acceptable because
  protected namespaces are dedicated and few; to be confirmed in review.

Open questions for Codex:
1. Accept the no-objectSelector approach (always-invoked in protected namespaces),
   or keep a selector and accept the invocation gap under RBAC?
2. Confirm `failurePolicy: Fail` blast radius for ConfigMap writes in protected
   namespaces is acceptable (dedicated namespaces), or scope the webhook more
   narrowly another way.
3. Is name-based recognition (`okx-grant-*`) plus mandatory grant naming an
   acceptable convention to enforce (e.g. via the approver tooling/RBAC)?

## Cross-cutting principle for both
A selector is a ROUTING decision, never the trust boundary. Security-critical
denial must not depend on an attacker-mutable label being present. Where routing
must stay narrow (namespaceSelector for unprotected-namespace safety), the gap is
closed by RBAC + the trusted registry, not by the selector itself.
