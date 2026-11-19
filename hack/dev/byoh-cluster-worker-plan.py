#!/usr/bin/env python3
"""Read-only, fail-closed Scaleway BYOH worker CAPI MachineDeployment plan."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import yaml

ROOT = Path(__file__).resolve().parents[2]
TEMPLATE = ROOT / "test/e2e/data/infrastructure-provider-byoh/v1beta1/templates/vm/md.yaml"
CLUSTER = "ok-byoh-scw1"
MACHINE = CLUSTER + "-md-0"
VERSION = "v1.36.6"
ROLE = "scaleway-worker"

def load(path, label):
    spec = importlib.util.spec_from_file_location(label, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

P = load("hack/dev/byoh-cluster-control-plane-plan.py", "worker_plan_base")
I = load("hack/dev/byoh-hosts-preflight.py", "worker_plan_inventory")
C = load("hack/dev/byoh-cluster-cilium.py", "worker_plan_cilium")
stop = P.stop

def render(source):
    variables = {"CLUSTER_NAME": CLUSTER, "NAMESPACE": "default",
                 "WORKER_MACHINE_COUNT": "1", "KUBERNETES_VERSION": VERSION}
    def substitute(match):
        key = match.group(1)
        if key not in variables:
            stop("unexpected worker template variable")
        return variables[key]
    manifest = re.sub(r"\$\{([A-Z_]+)\}", substitute, source)
    if "$" + "{" in manifest:
        stop("unresolved worker template parameters")
    docs = list(yaml.safe_load_all(manifest))
    if [x.get("kind") for x in docs] != ["ByoMachineTemplate", "KubeadmConfigTemplate",
                                        "MachineDeployment", "K8sInstallerConfigTemplate"]:
        stop("worker CAPI manifest kind drift")
    if any(x.get("metadata", {}).get("name") != MACHINE for x in docs):
        stop("worker CAPI manifest name drift")
    selector = docs[0]["spec"]["template"]["spec"]["selector"]["matchLabels"]
    if selector != {"role": "worker"}:
        stop("unexpected historical worker selector")
    selector["role"] = ROLE
    if docs[0]["spec"]["template"]["spec"]["installerRef"].get("name") != MACHINE:
        stop("worker installer template drift")
    md = docs[2]["spec"]
    if (md.get("clusterName") != CLUSTER or md.get("replicas") != 1 or
            md["template"]["spec"].get("version") != VERSION or
            md["template"]["spec"]["bootstrap"]["configRef"].get("name") != MACHINE or
            md["template"]["spec"]["infrastructureRef"].get("name") != MACHINE):
        stop("worker MD cluster/version/template drift")
    return yaml.safe_dump_all(docs, sort_keys=False, explicit_start=True)

REMOTE_WORKER = r'''set -eu
test "$(systemctl is-active byoh-hostagent.service || true)" = inactive || exit 21
test "$(systemctl is-enabled byoh-hostagent.service || true)" = disabled || exit 22
test -s /root/.byoh/config || exit 23
test ! -L /root/.byoh/config || exit 24
test "$(stat -c '%a %U:%G' /root/.byoh/config)" = '600 root:root' || exit 25
test ! -e /root/.byoh/bootstrap.kubeconfig || exit 26
test ! -e /etc/kubernetes/admin.conf || exit 27
test ! -e /etc/kubernetes/kubelet.conf || exit 28
test ! -e /etc/kubernetes/manifests/kube-apiserver.yaml || exit 29
test -f /etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf || exit 30
case "$(systemctl show -p ExecStart --value byoh-hostagent.service)" in
  *'--skip-installation'*) ;;
  *) exit 31 ;;
esac
test "$(systemctl show -p WorkingDirectory --value byoh-hostagent.service)" = /root || exit 32
echo PASS
'''

def ssh(peer, script, arg=None):
    args = ["ssh", "-T", "-o", "BatchMode=yes", "-o",
            "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8", peer,
            "sudo", "-n", "bash", "-s"]
    if arg:
        args += ["--", arg]
    try:
        proc = subprocess.run(args, input=script.encode(),
                              capture_output=True, timeout=28)
    except (OSError, subprocess.TimeoutExpired):
        stop("remote worker/CP check unavailable")
    if proc.returncode:
        stop("remote worker/CP check failed (exit " + str(proc.returncode) + ")")
    return proc.stdout.decode().strip()

REMOTE_CERT = r'''set -eu
echo | timeout 8 openssl s_client -connect "$1:6443" -servername "$1" 2>/dev/null \
  | openssl x509 -outform DER 2>/dev/null \
  | sha256sum | cut -d ' ' -f 1
'''

def certificate(peer, address):
    result = ssh(peer, REMOTE_CERT, address)
    if not re.fullmatch("[a-f0-9]{64}", result):
        stop("invalid workload API certificate fingerprint")
    return result

def compute(path):
    if path.is_symlink() or not path.is_file():
        stop("inventory missing/symlink")
    raw = path.read_bytes()
    kind, kc, _, hosts = I.validate(yaml.safe_load(raw))
    if kind != "ok-byoh-make-test" or os.getenv("BYOH_KIND_NAME") != kind:
        stop("wrong management inventory/context")
    workers = [x for x in hosts if x["role"] == "worker"]
    cps = [x for x in hosts if x["role"] == "control-plane"]
    if (len(workers) != 1 or len(cps) != 1 or
            workers[0]["name"] != "ok-bm-lab-02" or
            workers[0]["ssh"]["address"] != "51.158.62.216" or
            cps[0]["name"] != "ok-bm-lab-01" or
            cps[0]["ssh"]["address"] != "51.158.62.217"):
        stop("Scaleway lab inventory identity mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "config", "current-context"]) != "kind-" + kind:
        stop("management kubeconfig context mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        stop("management API unavailable")
    cluster = P.fetch(kc, "cluster", CLUSTER, "default")
    kcp = P.fetch(kc, "kubeadmcontrolplane", CLUSTER + "-control-plane", "default")
    if (cluster["metadata"].get("deletionTimestamp") or
            kcp["metadata"].get("deletionTimestamp") or
            cluster.get("spec", {}).get("paused") is True or
            kcp.get("spec", {}).get("version") != VERSION or
            kcp.get("spec", {}).get("replicas") != 1 or
            not C.kcp_initialized(kcp)):
        stop("unexpected live CAPI control-plane state")
    worker = workers[0]
    host = P.fetch(kc, "byohost", worker["name"], "default")
    if host["metadata"].get("deletionTimestamp") or host.get("spec", {}).get("byoMachineRef"):
        stop("worker deleting or already bound; no create replay")
    if host.get("metadata", {}).get("labels", {}).get("role") not in (None, ROLE):
        stop("worker role label conflict; no overwrite")
    if P.fetch(kc, "bootstrapkubeconfig", worker["name"], "default", allow_missing=True):
        stop("temporary worker bootstrap Secret still exists")
    for resource in ("machinedeployment", "byomachinetemplate", "kubeadmconfigtemplate",
                     "k8sinstallerconfigtemplate"):
        if P.fetch(kc, resource, MACHINE, "default", allow_missing=True):
            stop("worker CAPI resource already exists; review instead of replay")
    wpeer = worker["ssh"]["user"] + "@" + worker["ssh"]["address"]
    if ssh(wpeer, REMOTE_WORKER) != "PASS":
        stop("worker host not in expected stopped enrollment state")
    cpeer = cps[0]["ssh"]["user"] + "@" + cps[0]["ssh"]["address"]
    wcert, ccert = certificate(wpeer, cps[0]["ssh"]["address"]), certificate(cpeer, "127.0.0.1")
    if wcert != ccert:
        stop("worker-to-CP certificate differs from CP's own server certificate")
    C.verify_installed(C.remote_config(cps[0]))
    manifest = render(TEMPLATE.read_text(encoding="utf-8"))
    with tempfile.TemporaryDirectory(prefix="ok-byoh-worker-plan-") as folder:
        file = Path(folder) / "worker.yaml"
        file.write_text(manifest)
        P.run(["kubectl", "--kubeconfig", str(kc), "-n", "default",
               "apply", "--dry-run=server", "-f", str(file)], timeout=55)
    proof = {"cluster": CLUSTER, "management": kind, "worker": worker["name"],
             "worker_uid": host["metadata"]["uid"], "worker_rv": host["metadata"]["resourceVersion"],
             "inventory_sha256": hashlib.sha256(raw).hexdigest(),
             "template_sha256": hashlib.sha256(manifest.encode()).hexdigest(),
             "server_certificate_sha256": wcert}
    digest = hashlib.sha256(json.dumps(proof, sort_keys=True).encode()).hexdigest()
    return manifest, proof, digest

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--inventory", required=True)
    args = ap.parse_args()
    _, proof, digest = compute(Path(args.inventory))
    print("BYOH WORKER CAPI READ-ONLY PLAN")
    print("Management: ok-byoh-make-test; workload: " + CLUSTER + " (" + VERSION + ")")
    print("Worker: ok-bm-lab-02 (ubuntu@51.158.62.216), stopped/disabled")
    print("MachineDeployment: " + MACHINE + " (replicas=1, role=" + ROLE + ")")
    print("PASS: exact host, current Cilium/Node, worker→CP server certificate, CAPI template dry-run")
    print("PASS: no CAPI, agent, networking, Scaleway or OpenTofu writes")
    print("Plan SHA-256: " + digest)
    print("STOP AT APPLY BOUNDARY: worker binding and agent activation require separate approval")

if __name__ == "__main__":
    main()
