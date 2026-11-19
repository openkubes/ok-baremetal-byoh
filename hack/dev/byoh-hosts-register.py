#!/usr/bin/env python3
"""Start prepared BYOH enrollment agents under one confirmed N-host plan.

No CSR approval, host installation, token renewal or infrastructure mutations.
An interrupted run is not auto-resumed: review active units and CSRs first.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import time

import yaml

ROOT = Path(__file__).resolve().parents[2]
PREFLIGHT = ROOT / "hack/dev/byoh-hosts-preflight.py"
AGENT_PREP = ROOT / "hack/dev/byoh-hosts-agent-prepare.py"

def stop(why):
    raise SystemExit("STOP: " + why)

def run(argv, *, input=None, timeout=25):
    try:
        return subprocess.run(argv, input=input, text=True, capture_output=True,
                              check=True, timeout=timeout, cwd=ROOT).stdout.strip()
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
        stop("check/action failed (" + argv[0] + "); inspect partial state before retry")

def load(path, label):
    spec = importlib.util.spec_from_file_location(label, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

def kc(kubeconfig, *args):
    return run(["kubectl", "--kubeconfig", str(kubeconfig), *args])

def ssh(peer, body):
    return run(["ssh", "-T", "-o", "BatchMode=yes", "-o",
                "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                peer, "sudo", "-n", "bash", "-s"], input=body)

# Single SSH invocation checks the effective unit and credential integrity.
# Only the start phase uses remote script that writes to systemd.
HOST_CHECK = r'''set -eu
unit=byoh-hostagent.service
test "$(systemctl is-active "$unit" || true)" = inactive
test "$(systemctl is-enabled "$unit" || true)" = disabled
test ! -e /root/.byoh/config
test -x /usr/local/bin/byoh-hostagent
file=/root/.byoh/bootstrap.kubeconfig
test ! -L "$file"
test -f "$file"
test "$(stat -c '%a %U:%G' "$file")" = '600 root:root'
test -f /etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf
test ! -L /etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf
test "$(cat /etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf)" = "$(printf '%s' '[Service]
WorkingDirectory=/root
ExecStart=
ExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation')"
case "$(systemctl show -p ExecStart --value "$unit")" in
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default --skip-installation ;'*) ;;
  *) exit 20 ;;
esac
test "$(systemctl show -p WorkingDirectory --value "$unit")" = /root
sha256sum "$file" | cut -d ' ' -f 1
'''
HOST_START = r'''set -eu
unit=byoh-hostagent.service
test "$(systemctl is-active "$unit" || true)" = inactive
test "$(systemctl is-enabled "$unit" || true)" = disabled
test ! -e /root/.byoh/config
test -f /root/.byoh/bootstrap.kubeconfig
test ! -L /root/.byoh/bootstrap.kubeconfig
test "$(stat -c '%a %U:%G' /root/.byoh/bootstrap.kubeconfig)" = '600 root:root'
test "$(sha256sum /root/.byoh/bootstrap.kubeconfig | cut -d ' ' -f 1)" = "$1"
case "$(systemctl show -p ExecStart --value "$unit")" in
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default --skip-installation ;'*) ;;
  *) exit 21 ;;
esac
test "$(systemctl show -p WorkingDirectory --value "$unit")" = /root
systemctl start "$unit"
test "$(systemctl is-active "$unit")" = active
echo 'PASS: enrollment agent started (installation still disabled)'
'''

def lookup(kc_path, kind, name, namespace=None):
    args = ["-n", namespace] if namespace else []
    raw = kc(kc_path, *args, "get", kind, name, "--ignore-not-found", "-o", "json")
    return json.loads(raw) if raw else None

def verify_host(kc_path, ca, port, h):
    name = h["name"]
    peer = h["ssh"]["user"] + "@" + h["ssh"]["address"]
    if lookup(kc_path, "byohost", name, "default"):
        stop("already registered: " + name)
    if lookup(kc_path, "csr", "byoh-csr-default." + name):
        stop("existing BYOH CSR requires separate review: " + name)
    obj = lookup(kc_path, "bootstrapkubeconfig", name, "default")
    if not obj or obj["metadata"].get("deletionTimestamp"):
        stop("missing/deleting bootstrap object for " + name)
    if obj["spec"].get("apiserver") != "https://127.0.0.1:" + str(port):
        stop("management URL mismatch for " + name)
    if obj["spec"].get("certificate-authority-data") != ca:
        stop("management CA mismatch for " + name)
    created = datetime.fromisoformat(
        obj["metadata"]["creationTimestamp"].replace("Z", "+00:00"))
    if created.tzinfo is None:
        stop("invalid bootstrap creation timestamp for " + name)
    age = (datetime.now(timezone.utc) - created).total_seconds()
    if age < -60 or age >= 20 * 60:
        stop("bootstrap token stale: " + name + "; renew before registration")
    status = obj.get("status", {}).get("bootstrapKubeconfigData")
    if not isinstance(status, str) or not status:
        stop("bootstrap status missing: " + name)
    expected = hashlib.sha256(status.encode()).hexdigest()
    observed = ssh(peer, HOST_CHECK)
    if observed != expected:
        stop("remote bootstrap checksum mismatch for " + name)
    return {"name": name, "role": h["role"], "peer": peer,
            "bootstrap_uid": obj["metadata"]["uid"],
            "credential_sha256": expected}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    args = parser.parse_args()
    path = Path(args.inventory)
    if not path.is_file() or path.is_symlink():
        stop("inventory missing or symlink")
    original = path.read_bytes()
    try:
        data = yaml.safe_load(original)
    except yaml.YAMLError:
        stop("invalid inventory")
    preflight = load(PREFLIGHT, "byoh_preflight_register")
    kind, kubeconfig, port, hosts = preflight.validate(data)
    if kind != "ok-byoh-make-test" or os.environ.get("BYOH_KIND_NAME", kind) != kind:
        stop("only explicitly selected disposable management cluster allowed")
    run(["bash", "hack/dev/python-toolchain.sh", "hack/dev/byoh-hosts-preflight.py",
         "--inventory", str(path), "--allow-staged"])
    ca = kc(kubeconfig, "config", "view", "--raw", "--minify", "-o",
            "jsonpath={.clusters[0].cluster.certificate-authority-data}")
    if not ca:
        stop("management CA missing")
    plan = [verify_host(kubeconfig, ca, port, h) for h in hosts]
    digest = hashlib.sha256(json.dumps(
        {"management": kind, "port": port, "hosts": plan,
         "operation": "start enrollment-only agent", "csr_approval": False,
         "kubeadm": False}, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    print("BYOH registration START plan (no CSR approval), " + str(len(plan)) + " hosts", flush=True)
    for h in plan:
        print("  " + h["name"] + " (" + h["role"] + ") " + h["peer"] +
              " -> start --skip-installation", flush=True)
    print("Plan SHA-256: " + digest, flush=True)
    phrase = "start " + digest[:12]
    try:
        entered = input("Type '" + phrase + "' to start all checked agents: ")
    except EOFError:
        stop("interactive approval required")
    if entered != phrase:
        stop("start plan not authorized")
    if path.read_bytes() != original:
        stop("inventory changed after plan approval")
    # Recheck every host immediately before writing. Do not blindly resume
    # if a previous host already started or any token has expired.
    for h, inventory_host in zip(plan, hosts):
        current = verify_host(kubeconfig, ca, port, inventory_host)
        if current != h:
            stop("host identity changed before start: " + h["name"])
        print(h["name"] + ": " + ssh(h["peer"], "set -- " + h["credential_sha256"] +
                                    "\n" + HOST_START), flush=True)
    print("Enrollment agents started. CSRs MUST be reviewed independently.", flush=True)
    print("Read-only CSR inventory (request payloads and tokens withheld):", flush=True)
    for h in plan:
        csr_name = "byoh-csr-default." + h["name"]
        csr = lookup(kubeconfig, "csr", csr_name)
        if not csr:
            print("PENDING CSR: " + csr_name, flush=True)
            continue
        spec = csr.get("spec", {})
        print("CSR " + csr_name + ": signer=" + str(spec.get("signerName")) +
              ", requester=" + str(spec.get("username")) +
              ", groups=" + str(spec.get("groups")) +
              ", usages=" + str(spec.get("usages")) +
              ", conditions=" + str(csr.get("status", {}).get("conditions", [])), flush=True)
    print("STOP AT TRUST BOUNDARY: no CSR was approved, no kubeadm installation.", flush=True)

if __name__ == "__main__":
    main()
