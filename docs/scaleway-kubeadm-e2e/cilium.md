# Cilium on kubeadm/BYOH

In the observed run, `kubeadm init` deployed kube-proxy and did **not** install a CNI. Node remained `NotReady` with `NetworkPluginNotReady` until Cilium was installed.

The local `ok-cluster` artifact passed `make verify-cilium-chart`:

- Cilium chart `1.19.6`, path: `~/temp/kubernauts/ok/ok-cluster/.tools/cilium-1.19.6.tgz`
- SHA-256: `21c43cf53841f9ab0375047d95aa4c64051ea52bbd2c679416e6408f5f1c9179` (as reported by the user's actual Mac; verify afresh when replaying)
- Chart was installed against the **workload** tunneled kubeconfig, not kind management.

Observed Helm command (only for a **new cluster**; do not reinstall on healthy `ok-byoh-scw1`):

```sh
helm upgrade --install cilium "$HOME/temp/kubernauts/ok/ok-cluster/.tools/cilium-1.19.6.tgz" \
  --kubeconfig "$HOME/.config/ok/byoh-scw1/kubeconfig-tunnel" \
  --namespace kube-system \
  --set operator.replicas=1 \
  --set kubeProxyReplacement=false \
  --set k8sServiceHost=51.158.62.217 \
  --set k8sServicePort=6443 \
  --wait --timeout 5m
```

Do **not** call generic `make install-cni` from `ok-cluster` on this BYOH cluster: its Ubuntu path used `kubeProxyReplacement=true`, and it pulled an unpinned chart from the Helm repo in the observed Makefile. No KubePrism/Talos-specific values belong on these Ubuntu hosts.

Follow up with an actual cross-node connectivity test and egress/network policy smoke. `Node Ready` alone does not prove working pod-to-pod communication.
