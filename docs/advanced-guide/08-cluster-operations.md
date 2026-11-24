# 08 – Cluster Operations

Operate through the native Kubernetes and CAPI APIs. Keep management and workload kubeconfigs separate and verify both identities before requests. Use the explicit `mg` function from Chapter 5 and a workload function `wk() { kubectl --kubeconfig "$WORKLOAD_KUBECONFIG" --context "$WORKLOAD_CONTEXT" "$@"; }` after validation.

## Observe desired and actual state

```bash
mg -n "$NS" get clusters,kubeadmcontrolplanes,machinedeployments,machines
mg -n "$NS" get byomachines,byohosts
wk get nodes -o wide
wk get pods -A
wk get --raw='/readyz?verbose'
```

Check conditions, observed generation, replica counts, target versions, node readiness and identity links. Distinguish a controller deployment being Ready from successful lifecycle reconciliation. Inspect events and controller/agent logs using the actual deployment names and namespaces. Avoid dumping Secrets or raw kubeconfigs.

## Operational change policy

In replacement-capable clusters, scaling and some template changes can create or delete Machines. Deletion may cause host reset. In protected in-place-only clusters, the Guard intentionally rejects unauthorized changes, including replica changes and replacement transitions. Do not bypass admission, remove finalizers or modify owner references to force convergence. Plan capacity changes separately and confirm applicable policy before applying desired-state changes.

## Troubleshooting matrix

| Symptom | First checks |
|---|---|
| Host not registered | Agent service, CSR, token lifetime, TLS and time |
| Machine waiting for host | Namespace, role selector, free host capacity |
| Bootstrap failure | ByoHost conditions, installation references, agent journal |
| Node NotReady | Kubelet, runtime, CNI, DNS and routing |
| Upgrade stalls | Grant validity, hook decision, per-host approval and checkpoint |
| Deletion waits | Host reset condition and finalizer; do not force removal |

## Operational evidence

Record intended changes, operator identity, source revision, manifest and script checksums, observed runtime artifacts, conditions and before/after resource identities. Redact credentials. Retain backups independently of the system under maintenance. Any failed acceptance gate is a STOP, not automatic authorization for retry or replacement.

References: [Host lifecycle](https://github.com/openkubes/ok-baremetal-byoh/blob/main/docs/host_lifecycle.md), [In-place runbook](https://github.com/openkubes/ok-baremetal-byoh/blob/main/docs/inplace-upgrade.md).
