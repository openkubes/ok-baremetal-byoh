# BYOH In-Place Upgrade — Phase 1 Implementation Summary (Gate B10.22)

Builds on Phase 0 ([PHASE0-SUMMARY.md](PHASE0-SUMMARY.md): grant contract, guard M1–M6, CAPD run 9).
Base: `main` @ `15b668d` plus the Phase-0 working tree. CAPI source of truth: v1.14.4.
No commits, pushes or live deployments were made.

## 1. What Phase 1 adds
| Area | Files | Purpose |
|---|---|---|
| API | `apis/infrastructure/v1beta1/byohost_inplace_types.go`, `byohost_types.go`, CRD, deepcopy | `ByoHost.spec.inPlaceUpgrade` (request bound to Cluster/Machine/ByoMachine UID, node, role, from/to, pinned debs) and `status.inPlaceUpgrade` (phase, step, observed kubelet/Node versions) |
| Admission | `byohost_inplace_webhook.go`, `byohost_webhook.go` | request valid and bound to `spec.byoMachineRef.uid`; forward ≤ 1 minor; pkgs.k8s.io repo of the target minor; immutable while Running (except `attempt`↑); agents report only the request in their spec; `Succeeded` needs the observed target |
| Host executor | `agent/inplaceupgrade/` | Preflight → Repository → Download → Kubeadm → KubeadmUpgrade → Kubelet → Verify; argv commands (no shell); durable checkpoint bound to Machine UID/version/role; per-step timeouts; never `kubeadm reset` |
| Agent | `agent/reconciler/inplace_upgrade.go`, `host_reconciler.go`, `agent/main.go` | Running before host changes; resume after restart; Failed is final until `attempt` grows |
| Extension | `internal/inplace/extension/{handlers,byoh,nodes}.go`, `internal/inplace/packages.go`, `cmd/inplace-extension` | real UpdateMachine: identity chain, role, CP-before-worker, one host per cluster, drain/uncordon (client-go, PDB-respecting), request write (optimistic lock), success only when agent **and** Node verify; CanUpdate declines unsupported/unpinned; DockerMachine simulation only behind `--simulate-docker-machines` |
| Guard | `internal/safety/noreplacement/intent.go`, `webhookpaths.go`, `inplace_setup.go`, `cmd/no-replacement-guard` | KCP/MD intent: only a granted version change and maxSurge→0; replicas (also `/scale`), templates, kubeadm config, CREATE/DELETE denied |
| Deployment | `config/inplace/base`, `config/inplace/overlays/ha1` | guard, extension, 5 fail-closed webhooks, ExtensionConfig, package lock, name-scoped RBAC |
| Tooling | `hack/inplace/` | preflight (five hosts, Cilium gate), progress, monitor, etcd snapshot + restore verification, UID snapshot/compare, registry rendering |
| Docs | `docs/scaleway-kubeadm-e2e/inplace-upgrade-runbook.md`, `inplace-upgrade-ha1-rollout.md` | mechanism, forward recovery, decommission, approvals, ha1 plan |

## 2. Validation status
PASS: executor (fake host, incl. resume, timeouts, tampered package, foreign key, NotReady, skew);
reconciler (envtest); ByoHost webhook rules; extension (fake management + workload clients: roles,
sequencing, identity, timeout, ambiguous reports, drain, conflicts); package lock; intent guard
(unit) and **guard behind a real API server** (envtest: routing, `/scale`, dry-run, Machine create,
MD delete, webhook outage fails closed); `uids.py`; etcd restore verification with the cluster's
etcd image; live read-only preflight of ha1 (BLOCKED as expected).

PENDING: guarded CAPD rerun (dedicated daemon not available: a foreign kind cluster runs on
`colima-okx-guarded`); a real host executor run (BYOH-on-docker or a lab host); KCP in-place with
ByoMachines (CAPD covers DockerMachines only); `pass_denied_controller_fallback`.

Pre-existing, unrelated: `agent` suite needs a Docker host image (fails identically on `15b668d`);
`agent/registration` has one permission spec that fails as root (Linux container) and five
Linux-only specs on macOS.

## 3. Open security-review findings
See the B10.22 evidence `40-security-findings.md`; ADR-047 acceptance stays OPEN.
