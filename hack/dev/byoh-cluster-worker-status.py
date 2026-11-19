#!/usr/bin/env python3
"""Read-only post-install Scaleway BYOH worker status; no agent/Secret access.

This gate proves both expected Nodes and CAPI MachineDeployment are Ready,
and Cilium is 2/2. It does not claim cross-node connectivity/policy E2E.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[2]

def load(path, name):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

W = load("hack/dev/byoh-cluster-worker-plan.py", "worker_status_plan")
P = W.P
C = W.C
stop = W.stop
EXPECTED = {"ok-bm-lab-01", "ok-bm-lab-02"}

def is_ready(obj):
    conditions = obj.get("status", {}).get("conditions", [])
    return any(c.get("type") == "Ready" and c.get("status") == "True"
               for c in conditions)

def check_capi(kc):
    md = P.fetch(kc, "machinedeployment", W.MACHINE, "default")
    if (md["metadata"].get("deletionTimestamp")
            or md.get("spec", {}).get("clusterName") != W.CLUSTER
            or md.get("spec", {}).get("replicas") != 1
            or md.get("spec", {}).get("template", {}).get("spec", {}).get("version") != W.VERSION):
        stop("worker MachineDeployment identity/spec drift")
    status = md.get("status", {})
    if any(status.get(field) != 1 for field in (
            "replicas", "readyReplicas", "availableReplicas", "upToDateReplicas")):
        stop("worker MachineDeployment not 1/1 Ready/Available/UpToDate")
    raw = P.run(["kubectl", "--kubeconfig", str(kc), "-n", "default",
                 "get", "machines", "-l", "cluster.x-k8s.io/cluster-name=" + W.CLUSTER,
                 "-o", "json"])
    machines = json.loads(raw)["items"]
    if len(machines) != 2:
        stop("expected exactly two CAPI Machines")
    observed = {}
    for m in machines:
        if (m["metadata"].get("deletionTimestamp")
                or m.get("spec", {}).get("clusterName") != W.CLUSTER
                or m.get("spec", {}).get("version") != W.VERSION
                or not is_ready(m)):
            stop("CAPI Machine not Ready or identity drift")
        name = m.get("status", {}).get("nodeRef", {}).get("name")
        if name not in EXPECTED or name in observed:
            stop("CAPI Machine NodeRef identity mismatch")
        observed[name] = m["metadata"]["name"]
    if set(observed) != EXPECTED:
        stop("CAPI Machine NodeRef inventory mismatch")
    for host_name, machine_name in observed.items():
        host = P.fetch(kc, "byohost", host_name, "default")
        byo = P.fetch(kc, "byomachine", machine_name, "default")
        binding = host.get("spec", {}).get("byoMachineRef", {})
        if (binding.get("name") != machine_name
                or binding.get("uid") != byo["metadata"].get("uid")
                or host["metadata"].get("deletionTimestamp")
                or byo["metadata"].get("deletionTimestamp")):
            stop("post-install ByoHost/ByoMachine UID binding drift")
    return observed

def check_workload(kc):
    def observe(work_kc):
        if C.output(["kubectl", "--kubeconfig", str(work_kc),
                     "get", "--raw=/readyz"]) != "ok":
            stop("workload API not Ready")
        nodes = json.loads(C.output(["kubectl", "--kubeconfig", str(work_kc),
                                     "get", "nodes", "-o", "json"]))["items"]
        if len(nodes) != 2 or {n["metadata"]["name"] for n in nodes} != EXPECTED:
            stop("expected exactly two named workload Nodes")
        if any(n.get("status", {}).get("nodeInfo", {}).get("kubeletVersion") != W.VERSION
               or not is_ready(n) for n in nodes):
            stop("workload Node not Ready or Kubernetes version mismatch")
        daemon = json.loads(C.output(["kubectl", "--kubeconfig", str(work_kc),
                                      "-n", "kube-system", "get", "daemonset",
                                      "cilium", "-o", "json"]))
        ds = daemon.get("status", {})
        if any(ds.get(field) != 2 for field in (
                "desiredNumberScheduled", "currentNumberScheduled",
                "numberReady", "numberAvailable", "updatedNumberScheduled")):
            stop("Cilium DaemonSet not 2/2 ready/available")
        operator = json.loads(C.output(["kubectl", "--kubeconfig", str(work_kc),
                                        "-n", "kube-system", "get", "deployment",
                                        "cilium-operator", "-o", "json"]))
        os_ = operator.get("status", {})
        if os_.get("readyReplicas") != 1 or os_.get("availableReplicas") != 1:
            stop("Cilium operator not Ready")
        pods = json.loads(C.output(["kubectl", "--kubeconfig", str(work_kc),
                                    "-n", "kube-system", "get", "pods",
                                    "-o", "json"]))["items"]
        dns = [p for p in pods if p["metadata"]["name"].startswith("coredns-")]
        if not dns or any(p.get("status", {}).get("phase") != "Running" or
                          not p.get("status", {}).get("containerStatuses") or
                          not all(c.get("ready") is True for c in p["status"]["containerStatuses"])
                          for p in dns):
            stop("CoreDNS Pods not Ready")
    # Fresh CP credentials, CA, and server TLS name are still enforced.
    cp = {"ssh": {"user": "ubuntu", "address": "51.158.62.217"}}
    cfg = C.remote_config(cp)
    if C.verify_existing(cfg) != "matching":
        stop("current workload kubeconfig missing; identity review required")
    C.call_with_kubeconfig(cfg, observe)

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--inventory", required=True)
    args = ap.parse_args()
    path = Path(args.inventory)
    if path.is_symlink() or not path.is_file():
        stop("inventory missing/symlink")
    kind, kc, _, hosts = W.I.validate(yaml.safe_load(path.read_bytes()))
    if os.environ.get("BYOH_KIND_NAME") != "ok-byoh-make-test" or kind != "ok-byoh-make-test":
        stop("wrong management inventory/context")
    if {h["name"] for h in hosts} != EXPECTED or len(hosts) != 2:
        stop("unexpected host inventory")
    if P.run(["kubectl", "--kubeconfig", str(kc), "config",
              "current-context"]) != "kind-" + kind:
        stop("management context mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        stop("management API not Ready")
    observed = check_capi(kc)
    check_workload(kc)
    print("PASS: CAPI MachineDeployment " + W.MACHINE + " 1/1 Ready/Available/UpToDate")
    print("PASS: two CAPI Machines and UID-bound BYOH hosts: " + ", ".join(sorted(observed)))
    print("PASS: two workload Kubernetes " + W.VERSION + " Nodes Ready")
    print("PASS: Cilium DaemonSet 2/2, Operator 1/1, CoreDNS Ready")
    print("PASS: post-install worker status read-only; no credentials or tokens output")
    print("NOTE: cross-node pod connectivity and Cilium NetworkPolicy E2E still pending")

if __name__ == "__main__":
    main()
