#!/usr/bin/env python3
"""Read-only Scaleway lab control-plane provisioning plan (no mutations).

Renders the existing BYOH CAPI KCP template, validates inventory/host ownership,
then performs a Kubernetes server-side dry-run against dedicated management.
"""
import argparse
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import yaml

ROOT = Path(__file__).resolve().parents[2]
TEMPLATE = ROOT / "test/e2e/data/infrastructure-provider-byoh/v1beta1/templates/vm/cluster-with-kcp.yaml"
CLUSTER = "ok-byoh-scw1"
VERSION = "v1.36.6"
NAMESPACE = "default"
ROLE_LABEL = "control-plane"


def stop(why):
    raise SystemExit("STOP: " + why)


def run(args, *, input_bytes=None, timeout=35):
    try:
        return subprocess.run(args, input=input_bytes, check=True, capture_output=True,
                              timeout=timeout, cwd=ROOT).stdout.decode().strip()
    except (OSError, subprocess.TimeoutExpired, subprocess.CalledProcessError):
        stop("read-only check failed: " + args[0])


def fetch(kc, kind, name, namespace=None, allow_missing=False):
    args = ["kubectl", "--kubeconfig", str(kc)]
    if namespace:
        args += ["-n", namespace]
    args += ["get", kind, name, "-o", "json"]
    if allow_missing:
        args.insert(-2, "--ignore-not-found")
    raw = run(args)
    if not raw and allow_missing:
        return None
    try:
        return json.loads(raw)
    except ValueError:
        stop("invalid Kubernetes API response: " + kind)


def verify_host(cp, kc, port):
    name = cp["name"]
    if fetch(kc, "byohost", name, "default").get("metadata", {}).get("deletionTimestamp"):
        stop("control-plane ByoHost deleting")
    host = fetch(kc, "byohost", name, "default")
    if host.get("spec", {}).get("byoMachineRef"):
        stop("control-plane host already bound to a Machine")
    if fetch(kc, "bootstrapkubeconfig", name, "default", allow_missing=True):
        stop("control-plane bootstrap resource not yet cleaned up")
    labels = host.get("metadata", {}).get("labels", {})
    if labels.get("role") not in (None, ROLE_LABEL):
        stop("control-plane host has unexpected role label; no overwrite")
    peer = cp["ssh"]["user"] + "@" + cp["ssh"]["address"]
    remote = """set -eu
test "$(systemctl is-active byoh-hostagent.service)" = active
test -s /root/.byoh/config
test ! -e /root/.byoh/bootstrap.kubeconfig
test ! -e /etc/kubernetes/admin.conf
test ! -e /etc/kubernetes/kubelet.conf
test ! -e /etc/kubernetes/manifests/kube-apiserver.yaml
echo PASS
"""
    result = run(["ssh", "-T", "-o", "BatchMode=yes", "-o",
                  "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                  peer, "sudo", "-n", "bash", "-s"],
                 input_bytes=remote.encode(), timeout=25)
    if result != "PASS":
        stop("unexpected control-plane host pre-installation state")


def render(ip):
    params = {
        "CLUSTER_NAME": CLUSTER,
        "CONTROL_PLANE_ENDPOINT_IP": ip,
        "NAMESPACE": NAMESPACE,
        "CONTROL_PLANE_MACHINE_COUNT": "1",
        "CONTROL_PLANE_ENDPOINT_PORT": "6443",
        "CONTROL_PLANE_MAX_SURGE": "1",
        "KUBERNETES_VERSION": VERSION,
    }
    template = TEMPLATE.read_text(encoding="utf-8")
    def expand(match):
        key = match.group(1)
        if key not in params:
            stop("unrecognized control-plane template parameter: " + key)
        return params[key]
    rendered = re.sub(r"\$\{([A-Z_]+)(?::=[^}]*)?\}", expand, template)
    if "${" in rendered:
        stop("unresolved CAPI placeholders")
    try:
        docs = list(yaml.safe_load_all(rendered))
    except yaml.YAMLError:
        stop("invalid rendered CAPI YAML")
    kinds = [x.get("kind") for x in docs if isinstance(x, dict)]
    expected = ["Cluster", "ByoCluster", "ByoMachineTemplate",
                "KubeadmControlPlane", "K8sInstallerConfigTemplate"]
    if kinds != expected:
        stop("control-plane manifest kind set drift")
    if docs[1]["spec"]["controlPlaneEndpoint"] != {"host": ip, "port": 6443}:
        stop("unexpected control-plane endpoint")
    if docs[3]["spec"]["version"] != VERSION or docs[3]["spec"]["replicas"] != 1:
        stop("unexpected KCP version or replica count")
    if docs[2]["spec"]["template"]["spec"]["selector"]["matchLabels"] != {"role": "control-plane"}:
        stop("unexpected host selector")
    return rendered


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    args = parser.parse_args()
    path = Path(args.inventory)
    if not path.is_file() or path.is_symlink():
        stop("inventory missing/symlink")
    spec = importlib.util.spec_from_file_location("byoh_cluster_inventory", ROOT / "hack/dev/byoh-hosts-preflight.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    try:
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError):
        stop("invalid inventory")
    kind, kc, port, hosts = module.validate(data)
    if kind != "ok-byoh-make-test" or os.getenv("BYOH_KIND_NAME") != kind:
        stop("unexpected or unselected management cluster")
    if run(["kubectl", "--kubeconfig", str(kc), "config", "current-context"]) != "kind-" + kind:
        stop("management context mismatch")
    if run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        stop("management API unavailable")
    if fetch(kc, "cluster", CLUSTER, NAMESPACE, allow_missing=True):
        stop("workload cluster already exists; manual state review")
    cp_hosts = [h for h in hosts if h["role"] == "control-plane"]
    if len(cp_hosts) != 1:
        stop("lab provisioning requires exactly one control-plane host")
    cp = cp_hosts[0]
    try:
        ip = str(ipaddress.IPv4Address(cp["ssh"]["address"]))
    except ipaddress.AddressValueError:
        stop("lab control-plane endpoint must be explicit IPv4 address")
    verify_host(cp, kc, port)
    rendered = render(ip)
    with tempfile.TemporaryDirectory(prefix="ok-byoh-cp-plan-") as folder:
        dest = Path(folder) / "control-plane.yaml"
        dest.write_text(rendered, encoding="utf-8")
        run(["kubectl", "--kubeconfig", str(kc), "-n", NAMESPACE,
             "apply", "--dry-run=server", "-f", str(dest)], timeout=50)
    print("PASS: read-only CAPI control-plane plan; server dry-run succeeded")
    print("Management: " + kind)
    print("Workload cluster: " + CLUSTER + " (" + VERSION + ")")
    print("Control-plane host: " + cp["name"] + " " + ip)
    print("Endpoint: " + ip + ":6443 (existing public-IP lab topology)")
    print("Host selector: role=control-plane")
    print("No Machine, host label, tunnel, firewall, or Kubernetes package changes.")
    print("STOP BEFORE APPLY: existing tunnel/NAT network paths must be reviewed.")


if __name__ == "__main__":
    main()
