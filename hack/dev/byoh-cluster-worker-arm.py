#!/usr/bin/env python3
"""Guarded one-worker BYOH installation arm.

Default read-only. --arm is one explicit, digest-bound systemd drop-in write
and service start on the verified worker only. Never replays kubeadm.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]


def load(relative, name):
    spec = importlib.util.spec_from_file_location(name, ROOT / relative)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


V = load("hack/dev/byoh-cluster-worker-verify.py", "worker_arm_verify")
P = V.P
W = V.W
CP = V.CP
HOST = "ok-bm-lab-02"
PEER = "ubuntu@51.158.62.216"
DROPIN = "/etc/systemd/system/byoh-hostagent.service.d/95-ok-byoh-worker-install.conf"

# Reuse the exact preflight and atomically-exclusive systemd workflow that
# previously succeeded for the control-plane. Only the drop-in path changes.
REMOTE_PREFLIGHT = CP.REMOTE_PREFLIGHT.replace(
    "95-ok-byoh-cp-install.conf", "95-ok-byoh-worker-install.conf"
)
REMOTE_ARM = CP.REMOTE_INSTALL.replace(
    "95-ok-byoh-cp-install.conf", "95-ok-byoh-worker-install.conf"
).replace(
    ".95-ok-byoh-cp-install.", ".95-ok-byoh-worker-install."
)


def stop(reason):
    W.stop(reason)


def ssh(peer, script):
    try:
        proc = subprocess.run(
            ["ssh", "-T", "-o", "BatchMode=yes", "-o",
             "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
             peer, "sudo", "-n", "bash", "-s"],
            input=script.encode(), capture_output=True, timeout=38, cwd=ROOT)
    except (OSError, subprocess.TimeoutExpired):
        stop("worker SSH preflight or arm timed out; inspect worker service before retry")
    if proc.returncode:
        # stdout/stderr may reveal kubeadm tokens; never echo them.
        stop("worker SSH operation failed (exit " + str(proc.returncode) +
             "); inspect worker service state before retry")
    return proc.stdout.decode().strip()


def compute(path):
    if path.is_symlink() or not path.is_file():
        stop("inventory missing or symlink")
    raw = path.read_bytes()
    kc = V.management_kubeconfig(path)
    machine, bootstrap, installer, byom_uid = V.verify_objects(kc)
    V.remote_worker()
    # No past test output can authorize a fresh write; verify exact service
    # drop-in bytes and ensure no prior arm attempt was performed.
    unit_digest = ssh(PEER, REMOTE_PREFLIGHT)
    if not re.fullmatch(r"[a-f0-9]{64}", unit_digest):
        stop("unexpected enrollment drop-in digest")
    host = P.fetch(kc, "byohost", HOST, "default")
    byom = P.fetch(kc, "byomachine", machine, "default")
    boot = P.fetch(kc, "kubeadmconfig", machine, "default")
    installer_obj = P.fetch(kc, "k8sinstallerconfig", machine, "default")
    inst_secret = P.fetch(kc, "secret", installer, "default")
    boot_secret = P.fetch(kc, "secret", bootstrap, "default")
    if host["spec"]["byoMachineRef"]["uid"] != byom_uid:
        stop("BYOH machine binding changed")
    proof = {
        "cluster": W.CLUSTER, "management": "ok-byoh-make-test",
        "host": HOST, "machine": machine,
        "inventory_sha256": hashlib.sha256(raw).hexdigest(),
        "unit_sha256": unit_digest,
        "host_uid": host["metadata"]["uid"],
        "host_rv": host["metadata"]["resourceVersion"],
        "machine_uid": byom["metadata"]["uid"],
        "machine_rv": byom["metadata"]["resourceVersion"],
        "bootstrap_config_uid": boot["metadata"]["uid"],
        "installer_uid": installer_obj["metadata"]["uid"],
        "bootstrap_secret_uid": boot_secret["metadata"]["uid"],
        "installation_secret_uid": inst_secret["metadata"]["uid"],
    }
    digest = hashlib.sha256(json.dumps(proof, sort_keys=True,
                                        separators=(",", ":")).encode()).hexdigest()
    return raw, unit_digest, proof, digest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    parser.add_argument("--install", action="store_true")
    args = parser.parse_args()
    path = Path(args.inventory)
    raw, unit_hash, proof, digest = compute(path)
    print("BYOH WORKER INSTALL " + ("ACTION" if args.install else "READ-ONLY PLAN"))
    print("Workload: " + W.CLUSTER + " (" + W.VERSION + ")")
    print("Host: " + HOST + " via " + PEER)
    print("Bound Machine: " + proof["machine"])
    print("Ready installation Secret: verified (contents withheld)")
    print("Agent action: remove enrollment-only flags via verified systemd override, then start once")
    print("Other hosts, Scaleway, OpenTofu, CAPI specs and cluster network: NO CHANGES")
    print("Plan SHA-256: " + digest, flush=True)
    if not args.install:
        print("PASS: worker arm plan ready; no changes made")
        return

    phrase = "install " + digest[:12]
    try:
        answer = input("Type '" + phrase + "' to start worker Kubernetes installation: ")
    except EOFError:
        stop("interactive worker arm confirmation required")
    if answer != phrase:
        stop("worker installation not authorized")

    raw2, unit_hash2, proof2, digest2 = compute(path)
    if (raw2 != raw or unit_hash2 != unit_hash or
            proof2 != proof or digest2 != digest):
        stop("worker binding/inventory/unit changed since approval")
    # The remote script checks stopped+disabled state, the original
    # enrollment file SHA, no installation markers, then creates a
    # non-overwriting drop-in and starts the systemd unit exactly once.
    script = "set -- " + unit_hash + "\n" + REMOTE_ARM
    if ssh(PEER, script) != "PASS":
        stop("worker agent activation was not verified; inspect remote unit state")
    print("PASS: worker BYOH agent started; CAPI-managed kubeadm join may now execute")
    print("STOP AT OBSERVATION BOUNDARY: verify Machine/Node Ready; do not run arm twice")


if __name__ == "__main__":
    main()
