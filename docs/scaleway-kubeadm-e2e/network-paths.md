# Temporary E2E networking: NOT a production design

Validated Mac local time: 19 Nov 2026. Three *different* paths were required:

1. **Scaleway host -> management kind:** a separate SSH reverse tunnel from each bare-metal host at `127.0.0.1:16443` forwarding to Mac `127.0.0.1:61599` (kind API port-forward). Validate using the management CA, never `-k` for authentication flows. Keep sessions open. The tunnel on host 1 is **not** the tunnel on host 2.
2. **Mac -> Scaleway workload API:** SSH `-L 127.0.0.1:16444:127.0.0.1:6443` to CP; workload admin kubeconfig points at `https://127.0.0.1:16444` with `tls-server-name=51.158.62.217` to preserve certificate verification. This local tunnel does **not** give kind controllers a reverse path.
3. **kind -> Scaleway workload API:** a kind test-node NAT rewrite of TCP destination `51.158.62.217:6443` to `192.168.5.2:16444` where `host.lima.internal` resolves to `192.168.5.2`. Only after this did CAPI/ByoMachine initialize and show Available/Ready. This is an ephemeral test-specific change; original TLS SNI and cert verification stay as configured in the workload kubeconfig.

**Read-only validation** (from kind node):

```sh
docker --context colima-ok-cli-kvm exec ok-byoh-make-test-control-plane sh -c 'getent hosts host.lima.internal'
docker --context colima-ok-cli-kvm exec ok-byoh-make-test-control-plane sh -c 'curl -ksS --connect-timeout 3 --max-time 5 -o /dev/null -w "HTTP %{http_code}\n" https://host.lima.internal:16444/readyz'
```

`-k` above is **only** for a connectivity probe. A 200 proves a working path, not correct TLS trust or auth for controllers. Do not reuse on secure workload admin commands.

**The observed test-only NAT workaround**, restricted to one kind container (requires privileges). This intentionally changes networking. Review it before use:

```sh
docker --context colima-ok-cli-kvm exec ok-byoh-make-test-control-plane sh -c '
  for chain in PREROUTING OUTPUT; do
    iptables -t nat -C "$chain" -d 51.158.62.217/32 -p tcp --dport 6443 -j DNAT --to-destination 192.168.5.2:16444 2>/dev/null ||
    iptables -t nat -I "$chain" 1 -d 51.158.62.217/32 -p tcp --dport 6443 -j DNAT --to-destination 192.168.5.2:16444
  done
'
```

**Do not run the NAT command on Scaleway, Colima host, or reference kind cluster.** Only the disposable `ok-byoh-make-test-control-plane` node. Before removing rules, verify no live workload depends on them. Reverse exactly the target-specific rules using `iptables -t nat -D` with the same match once each in both chains. These NAT rules are not reboot-safe and the Mac tunnel is not a supported long-term management channel.

Also validate direct *worker -> CP* `https://51.158.62.217:6443/readyz` as it was reached during this test. **This does not prove Cilium cross-node pod networking**; test under real workload and restrict required host-to-host traffic with firewall rules outside this lab.
