#!/usr/bin/env python3
"""Fail-closed pinned Cilium installation for the disposable Scaleway BYOH lab.

Modes: plan (read-only), install (explicit confirmation), verify (read-only).
A fresh workload client identity is read from the designated CP using SSH and
never printed. The kubeconfig is TLS-verified over the existing 16444 tunnel.
No pre-existing workload kubeconfig or management kubeconfig is trusted blindly.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import socket
import subprocess
import tarfile
import tempfile
import yaml

ROOT = Path(__file__).resolve().parents[2]
CP = "51.158.62.217"
MGMT = "ok-byoh-make-test"
CLUSTER = "ok-byoh-scw1"
NS = "default"
LOCAL_PORT = 16444
EXPECTED_CHART_SHA = "21c43cf53841f9ab0375047d95aa4c64051ea52bbd2c679416e6408f5f1c9179"
VERSION = "1.19.6"
CHART = Path.home() / "temp/kubernauts/ok/ok-cluster/.tools/cilium-1.19.6.tgz"
TARGET = Path.home() / ".config/ok/byoh-scw1/kubeconfig-tunnel"
VALUES = {
    "operator": {"replicas": 1},
    "kubeProxyReplacement": False,
    "k8sServiceHost": CP,
    "k8sServicePort": 6443,
}


def stop(reason):
    raise SystemExit("STOP: " + reason)


def load(path, label):
    spec = importlib.util.spec_from_file_location(label, ROOT / path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


I = load("hack/dev/byoh-hosts-preflight.py", "cilium_hosts_inventory")
P = load("hack/dev/byoh-cluster-control-plane-plan.py", "cilium_cp_plan")


def command(argv, *, stdin=None, timeout=25, check=True):
    try:
        p = subprocess.run(argv, input=stdin, capture_output=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        stop("command unavailable or timed out: " + argv[0])
    if check and p.returncode:
        # Never dump stderr: may contain kubeconfig details or credentials.
        stop("command failed (exit " + str(p.returncode) + "): " + argv[0])
    return p


def output(argv, *, timeout=25):
    return command(argv, timeout=timeout).stdout.decode().strip()


def validate_management(inventory):
    if inventory.is_symlink() or not inventory.is_file():
        stop("inventory missing or symlink")
    raw = inventory.read_bytes()
    try:
        obj = yaml.safe_load(raw)
    except yaml.YAMLError:
        stop("malformed inventory")
    kind, kc, _, hosts = I.validate(obj)
    if kind != MGMT or os.environ.get("BYOH_KIND_NAME") != MGMT:
        stop("explicit management cluster selection required")
    cps = [h for h in hosts if h["role"] == "control-plane"]
    if len(cps) != 1 or cps[0]["name"] != "ok-bm-lab-01" or cps[0]["ssh"]["address"] != CP:
        stop("unexpected Scaleway CP identity")
    if output(["kubectl", "--kubeconfig", str(kc), "config", "current-context"]) != "kind-" + MGMT:
        stop("management context mismatch")
    if output(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        stop("management cluster unavailable")
    cluster = P.fetch(kc, "cluster", CLUSTER, NS)
    kcp = P.fetch(kc, "kubeadmcontrolplane", CLUSTER + "-control-plane", NS)
    if (cluster.get("metadata", {}).get("deletionTimestamp") or
            kcp.get("metadata", {}).get("deletionTimestamp")):
        stop("CAPI Cluster/KCP deleting")
    if (kcp.get("spec", {}).get("version") != "v1.36.6" or
            kcp.get("spec", {}).get("replicas") != 1 or
            kcp.get("status", {}).get("initialized") is not True):
        stop("expected initialized v1.36.6 singleton KCP")
    return raw, cps[0]


def validate_chart():
    if CHART.is_symlink() or not CHART.is_file():
        stop("pinned Cilium chart missing: run make verify-cilium-chart in ok-cluster")
    digest = hashlib.sha256(CHART.read_bytes()).hexdigest()
    if digest != EXPECTED_CHART_SHA:
        stop("Cilium chart SHA-256 differs from previously verified artifact")
    try:
        with tarfile.open(CHART, "r:gz") as archive:
            members = [m for m in archive.getmembers() if m.name.endswith("/Chart.yaml") and m.name.count("/") == 1]
            if len(members) != 1 or members[0].size > 20000:
                stop("unexpected Helm chart archive layout")
            chart = yaml.safe_load(archive.extractfile(members[0]).read())
    except (OSError, tarfile.TarError, yaml.YAMLError):
        stop("invalid pinned chart archive")
    if chart.get("name") != "cilium" or str(chart.get("version")) != VERSION:
        stop("pinned Helm chart identity/version mismatch")
    return digest


def remote_config(cp):
    peer = cp["ssh"]["user"] + "@" + cp["ssh"]["address"]
    p = command(["ssh", "-T", "-o", "BatchMode=yes", "-o",
                 "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                 peer, "sudo", "-n", "cat", "/etc/kubernetes/admin.conf"])
    try:
        cfg = yaml.safe_load(p.stdout)
        if not isinstance(cfg, dict):
            raise ValueError
        clusters, users, contexts = cfg["clusters"], cfg["users"], cfg["contexts"]
        if len(clusters) != 1 or len(users) != 1 or len(contexts) != 1:
            raise ValueError
        cluster = clusters[0]["cluster"]
        user = users[0]["user"]
        if cluster["server"] != "https://" + CP + ":6443":
            stop("kubeadm admin endpoint differs from reviewed Scaleway CP")
        if not isinstance(cluster.get("certificate-authority-data"), str):
            raise ValueError
        if not isinstance(user.get("client-certificate-data"), str) or not isinstance(user.get("client-key-data"), str):
            raise ValueError
        if "exec" in user or "token" in user or "insecure-skip-tls-verify" in cluster:
            stop("unexpected workload authentication configuration")
        cluster["server"] = "https://127.0.0.1:" + str(LOCAL_PORT)
        cluster["tls-server-name"] = CP
        cfg["current-context"] = contexts[0]["name"]
        return cfg
    except (KeyError, IndexError, TypeError, ValueError, yaml.YAMLError):
        stop("unsupported kubeadm admin kubeconfig structure")


def canonical(cfg):
    return yaml.safe_dump(cfg, sort_keys=True).encode()


def verify_existing(expected):
    if TARGET.is_symlink():
        stop("workload kubeconfig is a symlink")
    if not TARGET.exists():
        return "new"
    if not TARGET.is_file():
        stop("workload kubeconfig exists but is not a regular file")
    if TARGET.stat().st_mode & 0o077:
        stop("existing workload kubeconfig permissions are not private")
    try:
        observed = yaml.safe_load(TARGET.read_bytes())
    except (OSError, yaml.YAMLError):
        stop("invalid existing workload kubeconfig")
    if canonical(observed) != canonical(expected):
        stop("existing workload kubeconfig differs from fresh CP identity; archive it manually before proceeding")
    return "matching"


def call_with_kubeconfig(cfg, callback):
    with tempfile.TemporaryDirectory(prefix="byoh-cilium-") as d:
        path = Path(d) / "kubeconfig"
        path.write_bytes(canonical(cfg))
        path.chmod(0o600)
        return callback(path)


def validate_workload(cfg):
    def check(kc):
        if output(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"], timeout=20) != "ok":
            stop("TLS-verified workload API /readyz failed")
        nodes = json.loads(output(["kubectl", "--kubeconfig", str(kc), "get", "nodes", "-o", "json"]))
        items = nodes["items"]
        if len(items) != 1 or items[0]["metadata"]["name"] != "ok-bm-lab-01":
            stop("unexpected workload Node identity/count")
        if items[0]["status"]["nodeInfo"]["kubeletVersion"] != "v1.36.6":
            stop("workload Node Kubernetes version drift")
        ns = json.loads(output(["kubectl", "--kubeconfig", str(kc), "get", "namespace", "kube-system", "-o", "json"]))
        if ns["metadata"]["name"] != "kube-system":
            stop("workload kube-system namespace missing")
        return kubeconfig_helm_state(kc)
    return call_with_kubeconfig(cfg, check)


def kubeconfig_helm_state(kc):
    releases = json.loads(output(["helm", "list", "--kubeconfig", str(kc),
                                  "-n", "kube-system", "--all", "-o", "json"]))
    selected = [x for x in releases if x.get("name") == "cilium"]
    daemon = json.loads(output(["kubectl", "--kubeconfig", str(kc), "-n", "kube-system",
                                "get", "daemonsets", "-o", "json"]))["items"]
    deployment = json.loads(output(["kubectl", "--kubeconfig", str(kc), "-n", "kube-system",
                                    "get", "deployments", "-o", "json"]))["items"]
    has_cilium = any(d["metadata"]["name"] == "cilium" for d in daemon)
    has_operator = any(d["metadata"]["name"] == "cilium-operator" for d in deployment)
    return selected, has_cilium, has_operator


def plan_digest(inventory_raw, cfg, chart_hash, state):
    body = {
        "cluster": CLUSTER, "management": MGMT,
        "inventory_sha256": hashlib.sha256(inventory_raw).hexdigest(),
        "admin_identity_sha256": hashlib.sha256(canonical(cfg)).hexdigest(),
        "chart_sha256": chart_hash, "values": VALUES,
        "kubeconfig_state": state,
    }
    return hashlib.sha256(json.dumps(body, sort_keys=True).encode()).hexdigest()


def inspect(inventory):
    inventory_raw, cp = validate_management(inventory)
    chart_hash = validate_chart()
    cfg = remote_config(cp)
    state = verify_existing(cfg)
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.settimeout(2)
        if sock.connect_ex(("127.0.0.1", LOCAL_PORT)) != 0:
            stop("workload SSH tunnel 127.0.0.1:16444 not listening")
    helm_releases, has_daemon, has_operator = validate_workload(cfg)
    digest = plan_digest(inventory_raw, cfg, chart_hash, state)
    return inventory_raw, cfg, state, digest, helm_releases, has_daemon, has_operator


def store_kubeconfig(cfg):
    if TARGET.exists():
        if verify_existing(cfg) != "matching":
            stop("workload kubeconfig identity mismatch")
        return
    TARGET.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    if TARGET.parent.is_symlink() or TARGET.is_symlink():
        stop("workload kubeconfig directory/path must not be symlinks")
    fd = os.open(TARGET, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as dest:
            dest.write(canonical(cfg))
    except Exception:
        TARGET.unlink(missing_ok=True)
        raise


def verify_installed(cfg):
    def inspect_kc(kc):
        selected, daemon, operator = kubeconfig_helm_state(kc)
        if len(selected) != 1 or selected[0].get("status") != "deployed":
            stop("Cilium Helm release missing or not deployed")
        if not daemon or not operator:
            stop("Cilium DaemonSet or Operator Deployment missing")
        installed = json.loads(output(["helm", "--kubeconfig", str(kc), "-n", "kube-system",
                                       "get", "values", "cilium", "-o", "json"]))
        if (installed.get("operator", {}).get("replicas") != 1 or
                installed.get("kubeProxyReplacement") is not False or
                installed.get("k8sServiceHost") != CP or
                installed.get("k8sServicePort") != 6443):
            stop("Cilium Helm values differ from reviewed configuration")
        nodes = json.loads(output(["kubectl", "--kubeconfig", str(kc), "get", "nodes", "-o", "json"]))["items"]
        if len(nodes) != 1 or nodes[0]["metadata"]["name"] != "ok-bm-lab-01":
            stop("unexpected Node state after Cilium")
        if not any(c.get("type") == "Ready" and c.get("status") == "True"
                   for c in nodes[0].get("status", {}).get("conditions", [])):
            stop("workload Node not Ready after Cilium")
        pods = json.loads(output(["kubectl", "--kubeconfig", str(kc), "-n", "kube-system",
                                  "get", "pods", "-o", "json"]))["items"]
        for prefix in ("cilium-", "cilium-operator-", "coredns-"):
            candidates = [p for p in pods if p["metadata"]["name"].startswith(prefix)]
            if not candidates or not all(p.get("status", {}).get("phase") == "Running" and
                                         p.get("status", {}).get("containerStatuses") and
                                         all(x.get("ready") for x in p["status"]["containerStatuses"])
                                         for p in candidates):
                stop("CNI/CoreDNS Pod readiness missing: " + prefix)
    call_with_kubeconfig(cfg, inspect_kc)
    print("PASS: pinned Cilium release deployed with expected values; Node/CoreDNS Ready")
    print("NOTE: multi-node connectivity and network policy still require worker E2E")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    group = parser.add_mutually_exclusive_group()
    group.add_argument("--install", action="store_true")
    group.add_argument("--verify", action="store_true")
    args = parser.parse_args()
    inventory = Path(args.inventory)
    raw, cfg, local_state, digest, releases, daemon, operator = inspect(inventory)
    if args.verify:
        verify_installed(cfg)
        return
    if releases or daemon or operator:
        stop("Cilium or partial Cilium resources already present; verify/recovery required, no automatic upgrade")
    print("BYOH CILIUM " + ("INSTALL" if args.install else "READ-ONLY PLAN"))
    print("Workload: " + CLUSTER + " on " + CP + " (TLS-validated through 127.0.0.1:16444)")
    print("Management context: " + MGMT + " (never a Helm target)")
    print("Pinned chart: Cilium " + VERSION + "; SHA-256=" + EXPECTED_CHART_SHA)
    print("Local workload kubeconfig: " + local_state + " (never reused without identity comparison)")
    print("Values: kubeProxyReplacement=false, operator.replicas=1, service host=" + CP + ":6443")
    print("No Scaleway, OpenTofu, server firewall, or other kind cluster changes")
    print("Plan SHA-256: " + digest, flush=True)
    if not args.install:
        print("PASS: Cilium installation preflight; no persistent writes")
        return
    phrase = "cilium " + digest[:12]
    try:
        answer = input("Type '" + phrase + "' to install Cilium on " + CLUSTER + ": ")
    except EOFError:
        stop("interactive confirmation required")
    if answer != phrase:
        stop("Cilium installation not authorized")
    raw2, cfg2, state2, digest2, releases2, daemon2, operator2 = inspect(inventory)
    if (raw2 != raw or digest2 != digest or state2 != local_state or
            releases2 or daemon2 or operator2):
        stop("Cilium plan/target state drift after confirmation")
    store_kubeconfig(cfg)
    command(["helm", "--kubeconfig", str(TARGET), "-n", "kube-system", "install",
             "cilium", str(CHART), "--set", "operator.replicas=1",
             "--set", "kubeProxyReplacement=false",
             "--set", "k8sServiceHost=" + CP, "--set", "k8sServicePort=6443",
             "--wait", "--timeout", "5m"], timeout=350)
    print("PASS: Helm install completed; verify actual Node/Pod readiness with cilium-verify")
    # This may fail for genuine readiness issues; do not automatically retry.
    verify_installed(cfg)


if __name__ == "__main__":
    main()
