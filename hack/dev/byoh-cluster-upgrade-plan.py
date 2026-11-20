#!/usr/bin/env python3
"""Read-only, fail-closed upgrade preflight for the fixed two-host Scaleway lab.

Exit 0 means the observations completed, NOT that an upgrade is authorized.
No package changes, CAPI patches, approval prompts, or Secret reads are made.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import yaml

import importlib.util

ROOT = Path(__file__).resolve().parents[2]
def load(path, name):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

S = load("hack/dev/byoh-cluster-worker-status.py", "upgrade_worker_status")
W, P, C = S.W, S.P, S.C
BASE = "v1.36.6"
TARGET = "v1.37.2"
HOSTS = {"ok-bm-lab-01": "51.158.62.217", "ok-bm-lab-02": "51.158.62.216"}
PACKAGE = "1.37.2"
REMOTE = r"""set -eu
python3 - <<'PY'
import json, subprocess, sys
def call(args):
    p = subprocess.run(args, capture_output=True, timeout=12)
    if p.returncode:
        raise RuntimeError('read-only package query failed')
    return p.stdout.decode(errors='replace').strip()
try:
    installed = {name: call(['dpkg-query', '-W', '-f=${Version}', name])
                 for name in ('kubeadm', 'kubelet', 'kubectl')}
    candidates = {}
    for name in ('kubeadm', 'kubelet', 'kubectl'):
        text = call(['apt-cache', 'madison', name])
        candidates[name] = any(
            line.split('|')[1].strip().startswith('1.37.2-')
            for line in text.splitlines() if len(line.split('|')) >= 3)
    print(json.dumps({'installed': installed, 'target_available': candidates}))
except Exception:
    print('STOP: remote package inventory failed', file=sys.stderr)
    sys.exit(41)
PY
"""

def stop(message):
    raise SystemExit("STOP: " + message)

def remote_packages(host):
    peer = "ubuntu@" + HOSTS[host]
    args = ["ssh", "-T", "-o", "BatchMode=yes", "-o",
            "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
            peer, "sudo", "-n", "bash", "-s"]
    try:
        result = subprocess.run(args, input=REMOTE.encode(), capture_output=True, timeout=60)
    except (OSError, subprocess.TimeoutExpired):
        stop("read-only SSH package inventory unavailable: " + host)
    if result.returncode:
        stop("read-only package inventory failed: " + host)
    try:
        data = json.loads(result.stdout)
    except (ValueError, UnicodeError):
        stop("invalid package inventory response: " + host)
    installed, available = data.get("installed"), data.get("target_available")
    if not isinstance(installed, dict) or not isinstance(available, dict):
        stop("incomplete package inventory: " + host)
    for name in ("kubeadm", "kubelet", "kubectl"):
        if not re.fullmatch(r"1\.36\.6-[A-Za-z0-9.+:~_-]+", str(installed.get(name, ""))):
            stop("unexpected installed Kubernetes package: " + host + "/" + name)
        if not isinstance(available.get(name), bool):
            stop("unknown target package availability: " + host + "/" + name)
    return {name: available[name] for name in ("kubeadm", "kubelet", "kubectl")}

def assess_cluster(cluster, kcp):
    if (cluster.get("metadata", {}).get("deletionTimestamp")
            or kcp.get("metadata", {}).get("deletionTimestamp")):
        stop("CAPI Cluster/KCP is deleting")
    if cluster.get("spec", {}).get("paused") is not None:
        stop("Cluster spec.paused is explicitly set; manual review required")
    if cluster.get("metadata", {}).get("annotations", {}).get("cluster.x-k8s.io/paused") is not None:
        stop("Cluster pause annotation present")
    if kcp.get("spec", {}).get("version") != BASE or kcp.get("spec", {}).get("replicas") != 1:
        stop("KCP version/replica drift")
    if not C.kcp_initialized(kcp):
        stop("KCP is not initialized")
    if any(kcp.get("status", {}).get(k) != 1
           for k in ("replicas", "readyReplicas", "availableReplicas", "upToDateReplicas")):
        stop("KCP not 1/1 Ready/Available/UpToDate")
    conditions = cluster.get("status", {}).get("conditions", [])
    if not any(c.get("type") == "Available" and c.get("status") == "True"
               for c in conditions):
        # Not all CAPI API versions use the same condition representation.
        # Refuse an ambiguous state instead of assuming healthy.
        stop("CAPI Cluster Available condition missing or false")

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    args = parser.parse_args()
    path = Path(args.inventory)
    if not path.is_file() or path.is_symlink():
        stop("inventory missing or symlink")
    try:
        doc = yaml.safe_load(path.read_bytes())
    except (OSError, yaml.YAMLError):
        stop("invalid inventory")
    kind, kc, _, hosts = W.I.validate(doc)
    if kind != "ok-byoh-make-test" or os.getenv("BYOH_KIND_NAME") != kind:
        stop("expected explicit ok-byoh-make-test selection")
    if len(hosts) != 2 or {h["name"]: h["ssh"]["address"] for h in hosts} != HOSTS:
        stop("unexpected inventory host identity")
    if any(h["ssh"]["user"] != "ubuntu" for h in hosts):
        stop("unexpected SSH user")
    if P.run(["kubectl", "--kubeconfig", str(kc), "config", "current-context"]) != "kind-" + kind:
        stop("management kubeconfig context mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        stop("management API not Ready")
    cluster = P.fetch(kc, "cluster", W.CLUSTER, "default")
    kcp = P.fetch(kc, "kubeadmcontrolplane", W.CLUSTER + "-control-plane", "default")
    assess_cluster(cluster, kcp)
    S.check_capi(kc)
    S.check_workload(kc)
    availability = {host: remote_packages(host) for host in sorted(HOSTS)}
    fingerprint = hashlib.sha256(json.dumps(
        {"cluster_uid": cluster["metadata"]["uid"],
         "kcp_uid": kcp["metadata"]["uid"], "target": TARGET,
         "package_availability": availability}, sort_keys=True).encode()).hexdigest()
    print("BYOH KUBERNETES UPGRADE PLAN (READ-ONLY)")
    print("Management: " + kind + " | Workload: " + W.CLUSTER)
    print("Observed Kubernetes/CAPI: " + BASE + " | Requested target: " + TARGET)
    print("PASS: management identity, CAPI health and exact BYOH host bindings")
    print("PASS: workload API, both Nodes, Cilium and CoreDNS Ready")
    for host, packages in availability.items():
        missing = [name for name, available in packages.items() if not available]
        print(("PASS" if not missing else "BLOCKER") + ": " + host +
              " target packages " + ("available" if not missing else "unavailable: " + ", ".join(missing)))
    print("Observation SHA-256: " + fingerprint)
    print("BLOCKED: CAPI in-place version alignment and safe unpause are unvalidated.")
    print("BLOCKED: backup/recovery, disruption policy and plan-bound authorization are unvalidated.")
    print("NO UPGRADE AUTHORIZED; no CAPI, package, host or network writes performed.")

if __name__ == "__main__":
    main()
