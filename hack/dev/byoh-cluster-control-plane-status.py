#!/usr/bin/env python3
"""Read-only post-install BYOH control-plane and pre-CNI status. No secret/log output."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import yaml

ROOT = Path(__file__).resolve().parents[2]
def load(path, label):
    spec = importlib.util.spec_from_file_location(label, ROOT / path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

V = load("hack/dev/byoh-cluster-control-plane-verify.py", "status_capi_verify")
P, I = V.P, V.I

REMOTE = r'''set -eu
test "$(systemctl is-active byoh-hostagent.service)" = active
test "$(systemctl is-enabled byoh-hostagent.service)" = disabled
test -s /etc/kubernetes/admin.conf
for name in etcd kube-apiserver kube-controller-manager kube-scheduler; do
  test -s "/etc/kubernetes/manifests/$name.yaml"
done
python3 - <<'PY'
import json, subprocess, sys
def command(argv):
    p = subprocess.run(argv, capture_output=True, timeout=20)
    if p.returncode:
        raise RuntimeError("remote status command failed")
    return p.stdout.decode().strip()
try:
    kc = ["kubectl", "--kubeconfig=/etc/kubernetes/admin.conf"]
    readyz = command(kc + ["get", "--raw=/readyz"])
    nodes = json.loads(command(kc + ["get", "nodes", "-o", "json"]))["items"]
    pods = json.loads(command(kc + ["-n", "kube-system", "get", "pods", "-o", "json"]))["items"]
    packages = {name: command(["dpkg-query", "-W", "-f=${Version}", name])
                for name in ("kubeadm", "kubelet", "kubectl")}
    print(json.dumps({"readyz": readyz, "packages": packages,
        "nodes": [{"name": n["metadata"]["name"],
                   "version": n.get("status", {}).get("nodeInfo", {}).get("kubeletVersion"),
                   "ready": next((c["status"] for c in n.get("status", {}).get("conditions", [])
                                 if c["type"] == "Ready"), None)}
                  for n in nodes],
        "pods": [{"name": p["metadata"]["name"], "phase": p.get("status", {}).get("phase"),
                  "ready": bool(p.get("status", {}).get("containerStatuses"))
                  and all(c.get("ready") for c in p.get("status", {}).get("containerStatuses", []))}
                 for p in pods]}, separators=(",", ":")))
except Exception:
    print("STOP: remote kubeadm/API read-only status failed", file=sys.stderr)
    sys.exit(41)
PY
'''

def stop(reason):
    raise SystemExit("STOP: " + reason)

def assess(data):
    if data.get("readyz") != "ok":
        stop("workload API /readyz not healthy")
    if any(not str(data.get("packages", {}).get(x, "")).startswith("1.36.6-")
           for x in ("kubeadm", "kubectl", "kubelet")):
        stop("Kubernetes package version drift")
    nodes = data.get("nodes")
    if not isinstance(nodes, list) or len(nodes) != 1:
        stop("unexpected Node count")
    if nodes[0].get("name") != V.HOST or nodes[0].get("version") != V.VERSION:
        stop("workload Node identity/version drift")
    if nodes[0].get("ready") not in ("True", "False"):
        stop("Node Ready condition missing")
    pods = data.get("pods")
    if not isinstance(pods, list):
        stop("invalid kube-system Pod inventory")
    for base in ("etcd-", "kube-apiserver-", "kube-controller-manager-", "kube-scheduler-"):
        matching = [p for p in pods if p.get("name") == base + V.HOST]
        if len(matching) != 1 or matching[0].get("phase") != "Running" or matching[0].get("ready") is not True:
            stop("control-plane static Pod not Running/Ready: " + base.rstrip("-"))
    coredns = [p for p in pods if str(p.get("name", "")).startswith("coredns-")]
    if not coredns:
        stop("CoreDNS Pods absent")
    dns_ready = all(x.get("ready") is True for x in coredns)
    return nodes[0]["ready"] == "True", dns_ready

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--inventory", required=True)
    args = ap.parse_args()
    path = Path(args.inventory)
    if not path.is_file() or path.is_symlink():
        stop("inventory missing or symlink")
    try:
        doc = yaml.safe_load(path.read_text())
    except (OSError, yaml.YAMLError):
        stop("invalid inventory YAML")
    kind, kc, _, hosts = I.validate(doc)
    if kind != V.MGMT or os.getenv("BYOH_KIND_NAME") != kind:
        stop("management target mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "config", "current-context"]) != "kind-" + kind:
        stop("kubeconfig context mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        stop("management API unavailable")
    cp = [h for h in hosts if h["role"] == "control-plane"]
    if len(cp) != 1 or cp[0]["name"] != V.HOST or cp[0]["ssh"]["address"] != "51.158.62.217":
        stop("control-plane inventory mismatch")
    cluster = P.fetch(kc, "cluster", V.CLUSTER, "default")
    kcp = P.fetch(kc, "kubeadmcontrolplane", V.CLUSTER + "-control-plane", "default")
    if cluster["metadata"].get("deletionTimestamp") or kcp["metadata"].get("deletionTimestamp"):
        stop("CAPI control-plane deleting")
    if kcp.get("spec", {}).get("version") != V.VERSION or kcp.get("spec", {}).get("replicas") != 1:
        stop("unexpected CAPI KCP specification")
    raw = P.run(["kubectl", "--kubeconfig", str(kc), "-n", "default", "get",
                 "machines", "-l", "cluster.x-k8s.io/cluster-name=" + V.CLUSTER, "-o", "json"])
    machines = json.loads(raw)["items"]
    if len(machines) != 1 or not machines[0].get("metadata", {}).get("name", "").startswith(V.CLUSTER + "-control-plane-"):
        stop("CAPI Machine count or identity mismatch")
    machine = machines[0]
    byom = P.fetch(kc, "byomachine", machine["metadata"]["name"], "default")
    host = P.fetch(kc, "byohost", V.HOST, "default")
    ref = host.get("spec", {}).get("byoMachineRef", {})
    if ref.get("name") != machine["metadata"]["name"] or ref.get("uid") != byom.get("metadata", {}).get("uid"):
        stop("ByoHost/ByoMachine binding drift")
    peer = cp[0]["ssh"]["user"] + "@" + cp[0]["ssh"]["address"]
    cmd = ["ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
           "-o", "ConnectTimeout=8", peer, "sudo", "-n", "bash", "-s"]
    try:
        result = subprocess.run(cmd, input=REMOTE.encode(), capture_output=True, timeout=65)
    except (OSError, subprocess.TimeoutExpired):
        stop("SSH remote status unavailable")
    if result.returncode:
        stop("remote status check failed (exit " + str(result.returncode) + ")")
    try:
        node_ready, dns_ready = assess(json.loads(result.stdout))
    except (ValueError, UnicodeDecodeError):
        stop("invalid remote status JSON")
    print("PASS: CAPI identity and host binding verified")
    print("PASS: BYOH agent active/disabled; Kubernetes v1.36.6 API /readyz=ok")
    print("PASS: etcd and three control-plane static Pods Running/Ready")
    if not node_ready and not dns_ready:
        print("WAIT_CNI: Node NotReady and CoreDNS pending (pre-CNI expected)")
    elif node_ready and dns_ready:
        print("PASS: Node Ready and CoreDNS ready")
    else:
        print("PENDING: Node Ready=" + str(node_ready) + ", CoreDNS Ready=" + str(dns_ready))
    print("PASS: read-only status; no join-token or Secret output")

if __name__ == "__main__":
    main()
