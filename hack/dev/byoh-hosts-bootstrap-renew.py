#!/usr/bin/env python3
"""Renew staged, unconsumed BYOH bootstrap credentials (explicit batch approval).

This is NOT for registered hosts or live agents. Old token objects are deleted
with their revocation finalizer before the corresponding remote file is removed.
A partial failure stops: never silently reissue or overwrite credentials.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time

import yaml

ROOT = Path(__file__).resolve().parents[2]
E2E = ROOT / "scripts/scaleway-e2e/byoh-e2e.sh"
PREFLIGHT = ROOT / "hack/dev/byoh-hosts-preflight.py"

def stop(why):
    raise SystemExit("STOP: " + why)

def run(argv, *, input=None, timeout=30):
    try:
        return subprocess.run(argv, input=input, text=True, capture_output=True,
                              check=True, timeout=timeout, cwd=ROOT).stdout.strip()
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
        stop("command failed (" + argv[0] + "); inspect partial state before retry")

def ssh(peer, script, *, timeout=20):
    return run(["ssh", "-T", "-o", "BatchMode=yes", "-o",
                "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                peer, "sudo", "-n", "bash", "-s"], input=script, timeout=timeout)

def kc_cmd(kc, *args):
    return run(["kubectl", "--kubeconfig", str(kc), *args])

def lookup(kc, resource, name, *, namespace=None):
    args = ["-n", namespace] if namespace else []
    raw = kc_cmd(kc, *args, "get", resource, name,
                 "--ignore-not-found", "-o", "json")
    if not raw:
        return None
    try:
        obj = json.loads(raw)
    except ValueError:
        stop("malformed response for " + resource + "/" + name)
    return obj

def fingerprint(data):
    return hashlib.sha256(json.dumps(
        data, sort_keys=True, separators=(",", ":")).encode()).hexdigest()

def remote_info(peer):
    return ssh(peer, r'''set -eu
unit=byoh-hostagent.service
test "$(systemctl is-active "$unit" || true)" = inactive
test "$(systemctl is-enabled "$unit" || true)" = disabled
test ! -e /root/.byoh/config
file=/root/.byoh/bootstrap.kubeconfig
test ! -L "$file"
test -f "$file"
test "$(stat -c '%a %U:%G' "$file")" = '600 root:root'
sha256sum "$file" | cut -d ' ' -f 1
''')

def wait_ready(kc, name):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        obj = lookup(kc, "bootstrapkubeconfig", name, namespace="default")
        if obj and obj.get("status", {}).get("bootstrapKubeconfigData"):
            return obj
        time.sleep(2)
    stop("new bootstrap status not ready for " + name)

def confirm_helper(command, env, expected):
    # The existing helper's fail-closed checks and typed confirmation remain
    # in effect, supplied only after the operator approved the exact batch.
    result = subprocess.run(["bash", str(E2E), command], input=expected + "\n",
                            text=True, env=env, cwd=ROOT, check=False)
    if result.returncode:
        stop(command + " failed; partial renewal requires review")

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--inventory", required=True)
    a = ap.parse_args()
    path = Path(a.inventory)
    if path.is_symlink() or not path.is_file():
        stop("inventory file unavailable")
    try:
        original = path.read_bytes()
        data = yaml.safe_load(original)
    except (OSError, yaml.YAMLError):
        stop("invalid inventory")

    # Reuse the validated inventory/endpoint contract from host preflight.
    import importlib.util
    spec = importlib.util.spec_from_file_location("byoh_preflight", PREFLIGHT)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    kind, kc, port, hosts = mod.validate(data)
    if kind != "ok-byoh-make-test" or os.getenv("BYOH_KIND_NAME", kind) != kind:
        stop("renewal permitted only on explicitly selected disposable test management")
    run(["bash", "hack/dev/python-toolchain.sh",
         "hack/dev/byoh-hosts-preflight.py", "--inventory", str(path), "--allow-staged"])
    ca = kc_cmd(kc, "config", "view", "--raw", "--minify",
                "-o", "jsonpath={.clusters[0].cluster.certificate-authority-data}")
    if not ca:
        stop("management CA absent")
    plan = []
    for host in hosts:
        name = host["name"]
        peer = host["ssh"]["user"] + "@" + host["ssh"]["address"]
        obj = lookup(kc, "bootstrapkubeconfig", name, namespace="default")
        if not obj or obj.get("metadata", {}).get("deletionTimestamp"):
            stop("bootstrap request missing/deleting for " + name + "; manual recovery required")
        if (obj.get("spec", {}).get("apiserver") != "https://127.0.0.1:" + str(port)
                or obj.get("spec", {}).get("certificate-authority-data") != ca):
            stop("bootstrap target mismatch for " + name)
        status = obj.get("status", {}).get("bootstrapKubeconfigData")
        if not isinstance(status, str) or not status:
            stop("bootstrap credential missing for " + name)
        if lookup(kc, "byohost", name, namespace="default"):
            stop("host is already registered: " + name)
        if lookup(kc, "csr", "byoh-csr-default." + name):
            stop("host has a CSR; inspect enrollment before renewal: " + name)
        digest = hashlib.sha256(status.encode()).hexdigest()
        if remote_info(peer) != digest:
            stop("remote credential does not match management object: " + name)
        uid = obj.get("metadata", {}).get("uid")
        if not uid:
            stop("bootstrap object UID missing for " + name)
        plan.append({"name": name, "role": host["role"], "peer": peer,
                     "uid": uid, "old_sha256": digest})

    approval_plan = {"management": kind, "port": port, "hosts": plan,
                     "operation": "revoke-old-then-issue-new",
                     "starts_agents": False, "approves_csrs": False}
    digest = fingerprint(approval_plan)
    print("BYOH bootstrap renewal plan: " + str(len(plan)) + " hosts", flush=True)
    for h in plan:
        print("  " + h["name"] + " (" + h["role"] + ") " + h["peer"] +
              " -> revoke old / install new", flush=True)
    print("Plan SHA-256: " + digest, flush=True)
    print("Agent start: NO; CSR approval: NO; cluster provisioning: NO.", flush=True)
    phrase = "renew " + digest[:12]
    try:
        answer = input("Type '" + phrase + "' to authorize this renewal: ")
    except EOFError:
        stop("interactive approval required")
    if answer != phrase:
        stop("renewal not authorized")
    if path.read_bytes() != original:
        stop("inventory changed after approval")

    for h in plan:
        name, peer = h["name"], h["peer"]
        obj = lookup(kc, "bootstrapkubeconfig", name, namespace="default")
        if not obj or obj["metadata"].get("uid") != h["uid"]:
            stop("bootstrap identity changed before revocation: " + name)
        if remote_info(peer) != h["old_sha256"]:
            stop("remote credential changed before revocation: " + name)
        # Kubernetes deletion waits for token-revocation finalizer.
        kc_cmd(kc, "-n", "default", "delete", "bootstrapkubeconfig", name,
               "--wait=true", "--timeout=120s")
        if lookup(kc, "bootstrapkubeconfig", name, namespace="default"):
            stop("old bootstrap object not fully removed: " + name)
        # Atomic comparison / unlink; no credential contents leave the host.
        delete = r'''set -eu
test "$(systemctl is-active byoh-hostagent.service || true)" = inactive
test "$(systemctl is-enabled byoh-hostagent.service || true)" = disabled
test ! -e /root/.byoh/config
file=/root/.byoh/bootstrap.kubeconfig
test ! -L "$file"
test -f "$file"
test "$(stat -c '%a %U:%G' "$file")" = '600 root:root'
test "$(sha256sum "$file" | cut -d ' ' -f 1)" = "$1"
rm -- "$file"
test ! -e "$file"
'''
        # Explicit quoted digest; only SHA-256 hex values from Python.
        if len(h["old_sha256"]) != 64 or not all(c in "0123456789abcdef" for c in h["old_sha256"]):
            stop("invalid credential digest")
        ssh(peer, "set -- " + h["old_sha256"] + "\n" + delete)
        env = dict(os.environ)
        env.update({"HOST_WORKER": name, "WORKER_SSH": peer,
                    "MGMT_KUBECONFIG": str(kc), "NAMESPACE": "default",
                    "MGMT_FORWARD_URL": "https://127.0.0.1:" + str(port)})
        confirm_helper("enroll-create", env, "create-bootstrap-" + name)
        new = wait_ready(kc, name)
        if new["metadata"]["uid"] == h["uid"]:
            stop("new bootstrap UID unchanged: " + name)
        confirm_helper("enroll-transfer", env, "transfer-bootstrap-" + name)
        new_sha = hashlib.sha256(new["status"]["bootstrapKubeconfigData"].encode()).hexdigest()
        if remote_info(peer) != new_sha:
            stop("new credential verification failed: " + name)
        print("PASS: renewed bootstrap credential for " + name, flush=True)
    print("PASS: all bootstrap credentials renewed; agents remain disabled.", flush=True)
    print("STOP AT TRUST BOUNDARY: no agent start or CSR approval.", flush=True)

if __name__ == "__main__":
    main()
