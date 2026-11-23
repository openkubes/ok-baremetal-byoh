# No-Replacement Guard — Admission Webhook Configuration (M3)

This directory holds the ValidatingWebhookConfiguration that was previously
MISSING. It routes admission requests to the EXISTING standalone guard server
(`cmd/no-replacement-guard`) — no second admission infrastructure is introduced.

## Files
| File | Status | Contents |
|---|---|---|
| `machine-webhook.yaml` | ACTIVE | Machine webhook; handler exists today (`internal/safety/noreplacement`). |
| `pending-m6-webhooks.yaml` | PENDING (M6) | MachineSet / ByoMachine / Grant-ConfigMap webhooks; handlers land in M6. NOT applied. |
| `kustomization.yaml` | — | References only the active webhook. |

## Hard scope rules (GPT/ADR-047 rule 1 — no effect on unprotected clusters/namespaces)
- `failurePolicy: Fail`, `sideEffects: None`, `admissionReviewVersions: [v1]` on every webhook.
- `namespaceSelector matchLabels safety.openkubes.org/in-place-only=true` — ONLY labelled
  (protected) namespaces are intercepted. Unprotected namespaces are never routed to the guard.
- A protected namespace MUST ALSO appear in the trusted registry
  (`/etc/openkubes/no-replacement/registry.json`). The label routes; the registry binds the
  Cluster UID and is the authority. The guard fails closed for a labelled-but-unregistered namespace.
- `machines` only — NEVER `machines/status` (status is a disjoint authz path; node version lives there).
- Grant ConfigMaps are additionally narrowed by `objectSelector safety.openkubes.org/grant=true`,
  so ordinary ConfigMaps are never intercepted.
- Webhook paths are the single source of truth in `noreplacement/webhookpaths.go`, shared with
  `cmd/no-replacement-guard/main.go` to prevent YAML/Go drift.

## MachineSet admission — deliberate design (read before review)
A literal "deny-all MachineSet change" (M1 §3.5) would BREAK in-place upgrades: the MD move
legitimately scales `spec.replicas` on the source/target MachineSets and creates the target MS.
Therefore the M6 MachineSet handler is **allow-by-default** and denies ONLY the move-authorization
annotations (`acknowledged-move`, `receive-machines-from`) when they are set/changed WITHOUT a valid
MachineDeployment grant binding this exact target MS (UID) to the grant's MD UID. This still fully
gates the dangerous capability (authorizing machine moves) without blocking the lifecycle.

## PENDING (not part of M3; wired during full guard wiring after the joint M2+M3 review)
- M6 handlers for MachineSet / ByoMachine / Grant-ConfigMap (the three PENDING webhook paths).
- The grant-exception path in the Machine handler (today it still denies all in-place writes).
- Guard Deployment + `no-replacement-guard-webhook-service` Service + cert-manager Certificate
  (`no-replacement-guard-serving-cert`) + RBAC for the guard SA.
- Live Kubernetes verification (schema validation + apiserver admission behaviour).
