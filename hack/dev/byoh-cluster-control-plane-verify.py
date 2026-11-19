#!/usr/bin/env python3
"""Read-only Scaleway CAPI control-plane binding and installer readiness verification.

Runs after byoh-cluster-control-plane-apply, before authorizing kubeadm.
Inspects exact CAPI owner relationships, host binding, bootstrap Secret
existence (not content), installer references and the stopped CP agent.
No cluster writes, package changes or agent starts.
"""
import argparse
import importlib.util
import os
from pathlib import Path
import re
import yaml

ROOT = Path(__file__).resolve().parents[2]
CLUSTER = "ok-byoh-scw1"
MGMT = "ok-byoh-make-test"
NAMESPACE = "default"
HOST = "ok-bm-lab-01"
VERSION = "v1.36.6"


def module(path, label):
    spec = importlib.util.spec_from_file_location(label, ROOT / path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


P = module("hack/dev/byoh-cluster-control-plane-plan.py", "byoh_cp_bind_base")
I = module("hack/dev/byoh-hosts-preflight.py", "byoh_cp_bind_inventory")


def stop(message):
    P.stop(message)


def ref_name(ref, expected, label):
    if not isinstance(ref, dict) or ref.get("name") != expected:
        stop(label + " reference mismatch")


def verify_objects(kc):
    cluster = P.fetch(kc, "cluster", CLUSTER, NAMESPACE)
    cp = P.fetch(kc, "kubeadmcontrolplane", CLUSTER + "-control-plane", NAMESPACE)
    if cluster["metadata"].get("deletionTimestamp") or cp["metadata"].get("deletionTimestamp"):
        stop("Cluster/KCP is deleting")
    if cluster.get("spec", {}).get("paused") is True:
        stop("Cluster unexpectedly paused")
    ref_name(cluster.get("spec", {}).get("controlPlaneRef"),
             CLUSTER + "-control-plane", "Cluster controlPlane")
    ref_name(cluster.get("spec", {}).get("infrastructureRef"),
             CLUSTER, "Cluster infrastructure")
    if cp.get("spec", {}).get("version") != VERSION or cp.get("spec", {}).get("replicas") != 1:
        stop("KCP version/replicas drift")
    template = cp.get("spec", {}).get("machineTemplate", {})
    infra = template.get("infrastructureRef") if isinstance(template, dict) else None
    expected_name = CLUSTER + "-control-plane"
    if not isinstance(infra, dict) or infra.get("name") != expected_name or infra.get("kind") != "ByoMachineTemplate":
        # Show only non-secret, authoritative API reference fields. Do not
        # manufacture a fallback path or accept an unchecked reference.
        observed = infra if isinstance(infra, dict) else {}
        print("KCP template reference evidence (read-only):")
        print("  expected path: spec.machineTemplate.infrastructureRef")
        print("  expected name: " + expected_name)
        print("  expected kind: ByoMachineTemplate")
        print("  observed path exists: " + str(isinstance(infra, dict)))
        print("  observed name: " + str(observed.get("name", "<absent>")))
        print("  observed kind: " + str(observed.get("kind", "<absent>")))
        print("  observed machineTemplate keys: " + ", ".join(
            sorted(template)) if isinstance(template, dict) else "  observed machineTemplate: absent")
        stop("KCP machine template reference drift; no fallback accepted")

    # Fetch a list via explicit selector; never infer Machine names from a
    # previously observed random suffix.
    raw = P.run(["kubectl", "--kubeconfig", str(kc), "-n", NAMESPACE,
                 "get", "machines", "-l", "cluster.x-k8s.io/cluster-name=" + CLUSTER,
                 "-o", "json"])
    import json
    try:
        listing = json.loads(raw)
        candidates = listing["items"]
    except (TypeError, KeyError, ValueError):
        stop("invalid CAPI Machine list")
    if len(candidates) != 1:
        stop("expected one Machine for the lab control-plane, got " + str(len(candidates)))
    machine = candidates[0]
    machine_name = machine.get("metadata", {}).get("name", "")
    if not re.fullmatch(re.escape(CLUSTER + "-control-plane-") + r"[a-z0-9]+", machine_name):
        stop("unexpected CAPI Machine name")
    if machine["metadata"].get("deletionTimestamp"):
        stop("CAPI Machine deleting")
    if machine.get("spec", {}).get("clusterName") != CLUSTER or machine.get("spec", {}).get("version") != VERSION:
        stop("CAPI Machine cluster/version mismatch")
    infra_ref = machine.get("spec", {}).get("infrastructureRef")
    ref_name(infra_ref, machine_name, "Machine infrastructure")
    if infra_ref.get("kind") != "ByoMachine":
        stop("Machine is not backed by a ByoMachine")
    boot_ref = machine.get("spec", {}).get("bootstrap", {}).get("configRef")
    ref_name(boot_ref, machine_name, "Machine bootstrap")
    if boot_ref.get("kind") != "KubeadmConfig":
        stop("unexpected bootstrap kind")

    byom = P.fetch(kc, "byomachine", machine_name, NAMESPACE)
    if byom["metadata"].get("deletionTimestamp"):
        stop("ByoMachine deleting")
    host = P.fetch(kc, "byohost", HOST, NAMESPACE)
    if host["metadata"].get("deletionTimestamp"):
        stop("ByoHost deleting")
    if host.get("metadata", {}).get("labels", {}).get("role") != "control-plane":
        stop("control-plane host role mismatch")
    binding = host.get("spec", {}).get("byoMachineRef")
    if not isinstance(binding, dict) or binding.get("name") != machine_name:
        stop("ByoHost not bound to expected ByoMachine")
    if binding.get("namespace", NAMESPACE) != NAMESPACE:
        stop("ByoHost bound outside default namespace")

    kubeadm = P.fetch(kc, "kubeadmconfig", machine_name, NAMESPACE)
    if kubeadm["metadata"].get("deletionTimestamp"):
        stop("KubeadmConfig deleting")
    secret_name = kubeadm.get("status", {}).get("dataSecretName")
    if not isinstance(secret_name, str) or not secret_name:
        stop("KubeadmConfig data Secret not yet ready")
    secret = P.fetch(kc, "secret", secret_name, NAMESPACE)
    if secret["metadata"].get("deletionTimestamp"):
        stop("KubeadmConfig data Secret deleting")
    if not secret.get("data"):
        stop("bootstrap data Secret empty")
    # The secret content is deliberately not printed or parsed.
    installer = P.fetch(kc, "k8sinstallerconfig", machine_name, NAMESPACE)
    if installer["metadata"].get("deletionTimestamp"):
        stop("installer config deleting")
    installer_ref = byom.get("spec", {}).get("installerRef")
    if installer_ref and (installer_ref.get("name") != machine_name or
                          installer_ref.get("kind") != "K8sInstallerConfig"):
        stop("ByoMachine installerRef mismatches generated config")
    return machine_name, secret_name


REMOTE = """set -eu
if test "$(systemctl is-active byoh-hostagent.service || true)" != inactive; then
  echo 'STOP: CP agent must remain inactive' >&2; exit 21
fi
if test "$(systemctl is-enabled byoh-hostagent.service || true)" != disabled; then
  echo 'STOP: CP agent must remain disabled' >&2; exit 22
fi
test -s /root/.byoh/config || { echo 'STOP: missing permanent BYOH config' >&2; exit 23; }
test ! -e /root/.byoh/bootstrap.kubeconfig || { echo 'STOP: temporary credential still present' >&2; exit 24; }
test ! -e /etc/kubernetes/admin.conf || { echo 'STOP: kubeadm admin.conf already exists' >&2; exit 25; }
test ! -e /etc/kubernetes/manifests/kube-apiserver.yaml || { echo 'STOP: API static pod already present' >&2; exit 26; }
echo PASS
"""


def verify_remote(host):
    peer = host["ssh"]["user"] + "@" + host["ssh"]["address"]
    try:
        result = P.run(["ssh", "-T", "-o", "BatchMode=yes", "-o",
                        "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                        peer, "sudo", "-n", "bash", "-s"], input_bytes=REMOTE.encode(),
                       timeout=25)
    except SystemExit:
        stop("CP remote state failed safety check; inspect service and kubeadm state")
    if result != "PASS":
        stop("unexpected CP remote state")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    args = parser.parse_args()
    path = Path(args.inventory)
    if not path.is_file() or path.is_symlink():
        stop("inventory missing/symlink")
    try:
        inventory = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError):
        stop("invalid inventory")
    kind, kc, _, hosts = I.validate(inventory)
    if kind != MGMT or os.environ.get("BYOH_KIND_NAME") != MGMT:
        stop("unexpected management cluster")
    if P.run(["kubectl", "--kubeconfig", str(kc), "config",
              "current-context"]) != "kind-" + MGMT:
        stop("management context mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        stop("management API unavailable")
    cps = [host for host in hosts if host["role"] == "control-plane"]
    if len(cps) != 1 or cps[0]["name"] != HOST or cps[0]["ssh"]["address"] != "51.158.62.217":
        stop("control-plane host inventory mismatch")
    machine, secret = verify_objects(kc)
    verify_remote(cps[0])
    print("PASS: CAPI Cluster, KCP and Machine references verified: " + CLUSTER)
    print("PASS: ByoHost " + HOST + " bound to ByoMachine " + machine)
    print("PASS: KubeadmConfig bootstrap Secret exists: " + secret + " (contents withheld)")
    print("PASS: K8sInstallerConfig exists; CP agent remains inactive/disabled")
    print("PASS: control-plane pre-installation verification complete; no writes")
    print("STOP AT INSTALL BOUNDARY: do not arm agent until installer/network gates are reviewed")


if __name__ == "__main__":
    main()
