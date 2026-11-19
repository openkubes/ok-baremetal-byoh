#!/usr/bin/env python3
"""Read-only worker pre-install binding and installer Secret verification.

After worker-apply, before authorization of agent installation. No writes.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import yaml

ROOT = Path(__file__).resolve().parents[2]


def load(path, label):
    spec = importlib.util.spec_from_file_location(label, ROOT / path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


W = load("hack/dev/byoh-cluster-worker-plan.py", "worker_bind_plan")
P = W.P
CP = load("hack/dev/byoh-cluster-control-plane-install.py", "worker_bind_cp_install")


def stop(reason):
    W.stop(reason)


def reference(ref, name, kind, namespace="default"):
    return (
        isinstance(ref, dict)
        and ref.get("name") == name
        and ref.get("kind", kind) == kind
        and ref.get("namespace", namespace) == namespace
    )


def secret_ref(ref, name, secret):
    return (
        reference(ref, name, "Secret")
        and ref.get("apiVersion", "v1") == "v1"
        and (not ref.get("uid") or ref["uid"] == secret["metadata"].get("uid"))
    )


def verify_objects(kc):
    md = P.fetch(kc, "machinedeployment", W.MACHINE, "default")
    if md["metadata"].get("deletionTimestamp") or md.get("spec", {}).get("replicas") != 1:
        stop("worker MD deleting or replicas drift")
    if md.get("spec", {}).get("clusterName") != W.CLUSTER:
        stop("worker MD cluster drift")
    template = md.get("spec", {}).get("template", {}).get("spec", {})
    if (template.get("version") != W.VERSION or
            not reference(template.get("infrastructureRef"), W.MACHINE, "ByoMachineTemplate") or
            not reference(template.get("bootstrap", {}).get("configRef"), W.MACHINE, "KubeadmConfigTemplate")):
        stop("worker MD template reference drift")
    for kind in ("byomachinetemplate", "kubeadmconfigtemplate", "k8sinstallerconfigtemplate"):
        template_obj = P.fetch(kc, kind, W.MACHINE, "default")
        if template_obj["metadata"].get("deletionTimestamp"):
            stop("worker template deleting")
    infra_template = P.fetch(kc, "byomachinetemplate", W.MACHINE, "default")
    selector = infra_template.get("spec", {}).get("template", {}).get("spec", {}).get("selector", {}).get("matchLabels")
    if selector != {"role": W.ROLE}:
        stop("worker ByoMachineTemplate selector mismatch")
    raw = P.run(["kubectl", "--kubeconfig", str(kc), "-n", "default",
                 "get", "machines", "-l", "cluster.x-k8s.io/cluster-name=" + W.CLUSTER,
                 "-o", "json"])
    try:
        machines = json.loads(raw)["items"]
    except (ValueError, KeyError, TypeError):
        stop("invalid Machine list")
    workers = [m for m in machines if m.get("metadata", {}).get("labels", {}).get(
        "cluster.x-k8s.io/deployment-name") == W.MACHINE]
    if len(workers) != 1:
        stop("expected exactly one worker CAPI Machine, got " + str(len(workers)))
    machine = workers[0]
    name = machine["metadata"]["name"]
    if (machine["metadata"].get("deletionTimestamp") or
            not re.fullmatch(re.escape(W.MACHINE) + r"-[a-z0-9-]+", name)):
        stop("worker Machine deleting or unexpected identity")
    if (machine.get("spec", {}).get("clusterName") != W.CLUSTER or
            machine.get("spec", {}).get("version") != W.VERSION or
            not reference(machine.get("spec", {}).get("infrastructureRef"), name, "ByoMachine") or
            not reference(machine.get("spec", {}).get("bootstrap", {}).get("configRef"), name, "KubeadmConfig")):
        stop("worker Machine cluster/version/refs mismatch")
    byom = P.fetch(kc, "byomachine", name, "default")
    if byom["metadata"].get("deletionTimestamp") or not reference(
        byom.get("spec", {}).get("installerRef"), W.MACHINE, "K8sInstallerConfigTemplate"):
        stop("worker ByoMachine installer template reference mismatch")
    host = P.fetch(kc, "byohost", "ok-bm-lab-02", "default")
    binding = host.get("spec", {}).get("byoMachineRef")
    if (host["metadata"].get("deletionTimestamp") or
            host.get("metadata", {}).get("labels", {}).get("role") != W.ROLE or
            not reference(binding, name, "ByoMachine") or
            binding.get("uid") != byom["metadata"].get("uid")):
        stop("worker host binding name/UID/label mismatch")
    boot = P.fetch(kc, "kubeadmconfig", name, "default")
    boot_secret_name = boot.get("status", {}).get("dataSecretName")
    if not isinstance(boot_secret_name, str) or not boot_secret_name:
        stop("worker bootstrap Secret not yet reconciled")
    boot_secret = P.fetch(kc, "secret", boot_secret_name, "default")
    if boot_secret["metadata"].get("deletionTimestamp") or not boot_secret.get("data"):
        stop("worker bootstrap Secret unavailable")
    if not secret_ref(host.get("spec", {}).get("bootstrapSecret"), boot_secret_name, boot_secret):
        stop("worker host bootstrap Secret reference mismatch")
    installer = P.fetch(kc, "k8sinstallerconfig", name, "default")
    if installer["metadata"].get("deletionTimestamp") or installer.get("status", {}).get("ready") is not True:
        stop("worker K8sInstallerConfig not yet Ready")
    desired = installer.get("status", {}).get("installationSecret")
    observed = host.get("spec", {}).get("installationSecret")
    if not isinstance(desired, dict) or not isinstance(observed, dict):
        stop("worker installer Secret not published to ByoHost")
    secret_name = desired.get("name")
    if not isinstance(secret_name, str) or not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", secret_name):
        stop("worker installation Secret name missing")
    installation_secret = P.fetch(kc, "secret", secret_name, "default")
    if (installation_secret["metadata"].get("deletionTimestamp") or
            not installation_secret.get("data") or
            not secret_ref(desired, secret_name, installation_secret) or
            not secret_ref(observed, secret_name, installation_secret)):
        stop("worker installation Secret identity/data mismatch")
    return name, boot_secret_name, secret_name, byom["metadata"]["uid"]


def remote_worker():
    # Pre-install state from the existing worker planner is reused verbatim.
    peer = "ubuntu@51.158.62.216"
    if W.ssh(peer, W.REMOTE_WORKER) != "PASS":
        stop("worker pre-install agent state drift")
    return peer


def management_kubeconfig(path):
    if path.is_symlink() or not path.is_file():
        stop("inventory missing/symlink")
    kind, kc, _, hosts = W.I.validate(yaml.safe_load(path.read_bytes()))
    if kind != "ok-byoh-make-test" or os.environ.get("BYOH_KIND_NAME") != kind:
        stop("wrong management target")
    if P.run(["kubectl", "--kubeconfig", str(kc), "config", "current-context"]) != "kind-" + kind:
        stop("management context mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        stop("management API unavailable")
    workers = [x for x in hosts if x["role"] == "worker"]
    if len(workers) != 1 or workers[0]["name"] != "ok-bm-lab-02" or workers[0]["ssh"]["address"] != "51.158.62.216":
        stop("worker inventory mismatch")
    return kc


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--inventory", required=True)
    args = ap.parse_args()
    kc = management_kubeconfig(Path(args.inventory))
    machine, bootstrap, installer, uid = verify_objects(kc)
    remote_worker()
    print("PASS: worker MachineDeployment 1/1 desired, references match " + W.CLUSTER)
    print("PASS: ByoHost ok-bm-lab-02 bound to ByoMachine " + machine + " (UID verified)")
    print("PASS: KubeadmConfig bootstrap Secret exists: " + bootstrap + " (contents withheld)")
    print("PASS: Ready K8sInstallerConfig and matching installation Secret: " + installer + " (contents withheld)")
    print("PASS: worker agent remains inactive/disabled with --skip-installation")
    print("PASS: worker pre-installation checks read-only; no writes")
    print("STOP AT INSTALL BOUNDARY: verify an independent worker arm plan before authorizing Kubernetes installation")


if __name__ == "__main__":
    main()
