#!/usr/bin/env python3
"""Explicitly confirmed single-CP Scaleway CAPI apply from the pinned BYOH KCP template.

Creates only inventory-bound metadata label and CAPI resources; does not start
kubeadm or change systemd. The existing agent remains in --skip-installation.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import yaml

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location(
    "cp_read_only_plan", ROOT / "hack/dev/byoh-cluster-control-plane-plan.py")
P = importlib.util.module_from_spec(spec)
spec.loader.exec_module(P)
regspec = importlib.util.spec_from_file_location(
    "cp_registration", ROOT / "hack/dev/byoh-hosts-register-verify.py")
R = importlib.util.module_from_spec(regspec)
regspec.loader.exec_module(R)
invspec = importlib.util.spec_from_file_location(
    "cp_inventory", ROOT / "hack/dev/byoh-hosts-preflight.py")
I = importlib.util.module_from_spec(invspec)
invspec.loader.exec_module(I)


def identity(path):
    if not path.is_file() or path.is_symlink():
        P.stop("inventory missing or symlink")
    raw = path.read_bytes()
    try:
        inventory = yaml.safe_load(raw)
    except yaml.YAMLError:
        P.stop("invalid inventory")
    kind, kc, _, hosts = I.validate(inventory)
    if kind != "ok-byoh-make-test" or os.environ.get("BYOH_KIND_NAME") != kind:
        P.stop("explicit lab management target required")
    if P.run(["kubectl", "--kubeconfig", str(kc), "config",
              "current-context"]) != "kind-" + kind:
        P.stop("management context mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        P.stop("management API not ready")
    if P.fetch(kc, "cluster", P.CLUSTER, P.NAMESPACE, allow_missing=True):
        P.stop("workload cluster already exists; no automatic replay")
    cp = [h for h in hosts if h["role"] == "control-plane"]
    if len(cp) != 1 or cp[0]["name"] != "ok-bm-lab-01":
        P.stop("lab CP host selection drift")
    cp = cp[0]
    if cp["ssh"]["address"] != "51.158.62.217":
        P.stop("lab CP address drift")
    P.verify_host(cp, kc, None)
    R.verify_one(kc, cp)
    current = P.fetch(kc, "byohost", cp["name"], "default")
    if current.get("metadata", {}).get("labels", {}).get("role") not in (None, "control-plane"):
        P.stop("existing label conflicts")
    manifest = P.render(cp["ssh"]["address"])
    with tempfile.TemporaryDirectory(prefix="ok-byoh-cp-check-") as td:
        file = Path(td) / "cp.yaml"
        file.write_text(manifest, encoding="utf-8")
        P.run(["kubectl", "--kubeconfig", str(kc), "-n", "default", "apply",
               "--dry-run=server", "-f", str(file)], timeout=55)
    proof = {
        "cluster": P.CLUSTER, "management": kind, "host": cp["name"],
        "host_uid": current["metadata"]["uid"],
        "host_resourceVersion": current["metadata"]["resourceVersion"],
        "manifest_sha256": hashlib.sha256(manifest.encode()).hexdigest(),
        "inventory_sha256": hashlib.sha256(raw).hexdigest(),
        "action": "label control-plane and apply five exact BYOH CAPI KCP objects",
    }
    digest = hashlib.sha256(json.dumps(
        proof, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return raw, kind, kc, cp, manifest, digest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    args = parser.parse_args()
    path = Path(args.inventory)
    original, kind, kc, cp, manifest, digest = identity(path)
    print("BYOH CAPI CONTROL PLANE APPLY (5 resources, single host)")
    print("Cluster: " + P.CLUSTER + " (" + P.VERSION + ")")
    print("Host: " + cp["name"] + " " + cp["ssh"]["address"] + ":6443")
    print("Agent start: NO; kubeadm: NO; network/firewall/OpenTofu changes: NO")
    print("Plan SHA-256: " + digest, flush=True)
    phrase = "apply " + digest[:12]
    try:
        typed = input("Type '" + phrase + "' to apply this control-plane plan: ")
    except EOFError:
        P.stop("explicit interactive approval required")
    if typed != phrase:
        P.stop("plan not authorized")
    if path.read_bytes() != original:
        P.stop("inventory modified after approval")
    _, _, _, _, manifest2, digest2 = identity(path)
    if digest2 != digest or manifest2 != manifest:
        P.stop("host, manifest, or management state changed after approval")
    # Labeling is exact, no --overwrite, and a previously correct label
    # must not trigger a needless write or fail the first apply.
    live_host = P.fetch(kc, "byohost", cp["name"], "default")
    if live_host["metadata"]["uid"] != P.fetch(kc, "byohost", cp["name"], "default")["metadata"]["uid"]:
        P.stop("ByoHost changed during apply")
    role = live_host.get("metadata", {}).get("labels", {}).get("role")
    if role is None:
        P.run(["kubectl", "--kubeconfig", str(kc), "-n", "default",
               "label", "byohost", cp["name"], "role=control-plane"],
              timeout=25)
        print("PASS: selected host labelled role=control-plane")
    elif role == "control-plane":
        print("PASS: existing host role=control-plane preserved")
    else:
        P.stop("unexpected role label")
    with tempfile.TemporaryDirectory(prefix="ok-byoh-cp-apply-") as td:
        file = Path(td) / "cp.yaml"
        file.write_text(manifest, encoding="utf-8")
        P.run(["kubectl", "--kubeconfig", str(kc), "-n", "default",
               "apply", "-f", str(file)], timeout=60)
    print("PASS: CAPI CP desired state submitted. Agent is not armed.")
    print("STOP AT INSTALL BOUNDARY: verify CAPI ByoMachine/Host binding and network path.")


if __name__ == "__main__":
    main()
