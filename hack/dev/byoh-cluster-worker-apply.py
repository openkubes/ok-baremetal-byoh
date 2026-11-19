#!/usr/bin/env python3
"""Explicitly confirmed, fail-closed one-worker BYOH MD apply.

Creates four reviewed CAPI objects and (if necessary) a non-overwriting
worker-role label. Does NOT start the agent, run kubeadm, or modify hosts.
"""
import argparse
import importlib.util
from pathlib import Path
import tempfile
import yaml

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location(
    "worker_read_only", ROOT / "hack/dev/byoh-cluster-worker-plan.py")
W = importlib.util.module_from_spec(spec)
spec.loader.exec_module(W)


def target_kubeconfig(inventory):
    """Bind write target to the exact management context selected in plan."""
    kind, kc, _, _ = W.I.validate(yaml.safe_load(inventory.read_bytes()))
    if kind != "ok-byoh-make-test":
        W.stop("unexpected management cluster target")
    return kc


def guard_host(kc, proof):
    host = W.P.fetch(kc, "byohost", proof["worker"], "default")
    md = host.get("metadata", {})
    if (md.get("uid") != proof["worker_uid"] or
            md.get("resourceVersion") != proof["worker_rv"] or
            md.get("deletionTimestamp") or
            host.get("spec", {}).get("byoMachineRef")):
        W.stop("worker host UID/RV/binding drift immediately before apply")
    role = md.get("labels", {}).get("role")
    if role not in (None, W.ROLE):
        W.stop("worker host label conflict")
    return role


def apply(kc, manifest, proof):
    """The only write section: precise label and four new CAPI objects."""
    role = guard_host(kc, proof)
    if role is None:
        W.P.run(["kubectl", "--kubeconfig", str(kc), "-n", "default",
                 "label", "byohost", proof["worker"], "role=" + W.ROLE], timeout=25)
        print("PASS: worker host labelled role=" + W.ROLE, flush=True)
    else:
        print("PASS: existing correct worker role label preserved", flush=True)

    # A controller may reconcile the host as soon as its CAPI objects
    # appear. The enrollment-only agent must still be fully stopped.
    worker_peer = "ubuntu@51.158.62.216"
    if W.ssh(worker_peer, W.REMOTE_WORKER) != "PASS":
        W.stop("worker agent state changed after host label, CAPI apply blocked")

    with tempfile.TemporaryDirectory(prefix="ok-byoh-worker-apply-") as td:
        file = Path(td) / "worker.yaml"
        file.write_text(manifest, encoding="utf-8")
        W.P.run(["kubectl", "--kubeconfig", str(kc), "-n", "default",
                 "apply", "-f", str(file)], timeout=65)
    print("PASS: four worker CAPI objects submitted, agent remains stopped", flush=True)
    print("STOP AT INSTALL BOUNDARY: verify BYOH binding and installer Secret before arming worker")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--inventory", required=True)
    args = ap.parse_args()
    path = Path(args.inventory)
    original = path.read_bytes() if path.is_file() and not path.is_symlink() else None
    if original is None:
        W.stop("inventory missing or symlink")
    manifest, proof, digest = W.compute(path)
    kc = target_kubeconfig(path)
    print("BYOH WORKER CAPI APPLY ACTION (four resources; single worker)")
    print("Workload: " + W.CLUSTER + " (" + W.VERSION + ")")
    print("Worker: " + proof["worker"] + "; host label=" + W.ROLE)
    print("Agent start: NO; kubeadm: NO; host network/firewall/OpenTofu: NO")
    print("Plan SHA-256: " + digest, flush=True)
    phrase = "apply " + digest[:12]
    try:
        answer = input("Type '" + phrase + "' to apply this exact worker CAPI plan: ")
    except EOFError:
        W.stop("interactive plan approval required")
    if answer != phrase:
        W.stop("worker apply not authorized")
    if path.read_bytes() != original:
        W.stop("worker inventory changed after approval")
    manifest2, proof2, digest2 = W.compute(path)
    if digest != digest2 or proof != proof2 or manifest != manifest2:
        W.stop("worker CAPI plan changed after confirmation")
    apply(kc, manifest, proof)


if __name__ == "__main__":
    main()
