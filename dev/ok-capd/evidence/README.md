# CAPD test evidence

Runtime evidence is written outside the repository under
`~/.local/state/ok/capd/` (planned for the workload-test phase). Do not
commit kubeconfigs, certificates, cluster dumps, logs containing credentials,
or host inventory.

The initial bootstrap smoke test reports directly to the terminal and does
not create an evidence bundle. A later test harness will add timestamped,
redacted evidence bundles with a manifest of tool and provider versions.
