# 12 – Validation & Limitations

## Evidence hierarchy

Distinguish implementation, unit/envtest coverage, container-based installer CI, isolated real-host E2E and production readiness. A PASS at one level does not imply PASS at another.

| Capability | Evidence / maturity |
|---|---|
| Ubuntu 24.04 / 26.04, amd64 / arm64 | Documented default installer support and container CI |
| Kubernetes v1.36.6, v1.37.2 (container CI); v1.35 line in version-skew unit tests | Documented installer and CI tests |
| CAPI-driven in-place upgrade | One successful five-host real-host E2E |
| Manual host approvals and forward recovery | Exercised in that run |
| Automated HOTL host release | Pending validation |
| Controlled host reuse and decommission | Pending full E2E |
| Management pivot and disaster recovery | Pending dedicated E2E |
| Guard production security | NO-GO; hardening incomplete |

## Reference E2E: 24 November 2026

CAPI v1.14.4; three stacked-etcd control planes and two workers; Ubuntu 24.04.5; containerd 2.3.7; Kubernetes v1.36.6 to v1.37.2; Cilium 1.21.0-pre.4 under a lab-only exception. The project evidence reports 5/5 Nodes Ready, 20/20 Machine/ByoMachine/ByoHost/Node UIDs retained, unchanged etcd member IDs, 3/3 healthy etcd members and no Machine replacement. The supplied project briefing reports 70/70 netcheck PASS and 2,216 worker-phase API probes without error. Control-plane API probes did experience failures; do not claim zero downtime. Worker timings are lab observations, not performance guarantees.

The published protocol records component revisions and hashes, including changes made during recovery. It distinguishes scripts actually used from later fixes. Independently rebuilt artifacts were bit-identical in the lab, but were not signed. PR #40 agent was merged after the run and is not part of the validated historical artifact set.

## Known security and operational limits

Current host registration documentation identifies broad Secret reads, unbound bootstrap tokens, hostname collisions, renewal constraints and lack of individual client certificate revocation. Guard limitations include privileged administrative bypass, protection namespace writer risks, trusted initiators, and non-atomic host serialization. Production deployment requires security hardening and independent review. No universal hypervisor, mixed architecture, air-gap or workload compatibility certification is implied.

## Next validation gates

Run complete applicable lab suites (`tests/run.sh` in the lab context, confirm path and scope), validate stable Cilium compatibility and pin artifacts, automate executed-script evidence, verify agent #40, exercise controlled decommission and host reuse with independent backups, then HOTL and management pivot/DR. Record failed tests and deviations rather than silently converting them into PASS.

## Documentation review gate

Claude should verify all CLI flags, manifests, provider packaging and template variables against pinned code; resolve placeholders; validate provider neutrality and SVG links; review security assertions and check execution examples in an isolated environment. No Codex review is required. Do not merge or publish before explicit approval.

References: [README](../../README.md), [E2E protocol](../scaleway-kubeadm-e2e/inplace-upgrade-testprotocol.md), [Host registration](../host_registration.md). <!-- provider-neutrality: allow (link to the labeled Scaleway E2E evidence protocol) -->
