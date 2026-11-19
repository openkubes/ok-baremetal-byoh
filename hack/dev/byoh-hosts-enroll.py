#!/usr/bin/env python3
"""Prepare verified BYOH host bootstrap credentials using the existing E2E helper.

Phase 1 only: create and securely transfer per-host bootstrap credentials.
Never start agents or approve CSRs; stop before the identity trust boundary.
"""
import argparse
import os
from pathlib import Path
import subprocess
import sys

import yaml
from importlib.machinery import SourceFileLoader
from importlib.util import module_from_spec, spec_from_loader

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/scaleway-e2e/byoh-e2e.sh"
PREFLIGHT = ROOT / "hack/dev/byoh-hosts-preflight.py"


def fail(message):
    sys.exit("STOP: " + message)


def call(argv, *, env=None):
    try:
        subprocess.run(argv, check=True, cwd=ROOT, env=env)
    except (OSError, subprocess.CalledProcessError) as exc:
        fail("workflow halted (" + argv[0] + "): " + type(exc).__name__)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    args = parser.parse_args()
    inventory = Path(args.inventory)
    if not inventory.is_file() or inventory.is_symlink():
        fail("inventory missing or symlink")
    try:
        data = yaml.safe_load(inventory.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError):
        fail("invalid inventory")

    loader = SourceFileLoader("byoh_hosts_preflight", str(PREFLIGHT))
    spec = spec_from_loader(loader.name, loader)
    mod = module_from_spec(spec)
    loader.exec_module(mod)
    kind, kubeconfig, port, hosts = mod.validate(data)
    if os.environ.get("BYOH_KIND_NAME", kind) != kind:
        fail("BYOH_KIND_NAME does not match inventory")
    if kind != "ok-byoh-make-test":
        fail("remote enrollment is restricted to the disposable test management cluster")

    # Re-check all hosts and exact management tunnel identity before any write.
    call(["bash", "hack/dev/python-toolchain.sh",
          "hack/dev/byoh-hosts-preflight.py", "--inventory", str(inventory)])

    # Fail closed if any bootstrap resource already exists on either host.
    for host in hosts:
        result = subprocess.run(
            ["kubectl", "--kubeconfig", str(kubeconfig), "-n", "default",
             "get", "bootstrapkubeconfig", host["name"],
             "--ignore-not-found", "-o", "name"],
            cwd=ROOT, capture_output=True, text=True, check=False,
        )
        if result.returncode != 0 or result.stdout.strip():
            fail("existing or unreadable bootstrap resource for " + host["name"])

    print("Enrollment preparation: two hosts; no agent start or CSR approval.", flush=True)
    print("Each bootstrap creation and transfer requires confirmation.", flush=True)
    for host in hosts:
        env = dict(os.environ)
        env.update({
            "HOST_WORKER": host["name"],
            "WORKER_SSH": host["ssh"]["user"] + "@" + host["ssh"]["address"],
            "MGMT_KUBECONFIG": str(kubeconfig),
            "MGMT_FORWARD_URL": "https://127.0.0.1:" + str(port),
            "NAMESPACE": "default",
        })
        # Existing helper retains its interactive explicit confirmations,
        # credentials handling, transport checksum, and restrictive permissions.
        call(["bash", str(SCRIPT), "enroll-create"], env=env)
        call(["bash", str(SCRIPT), "enroll-transfer"], env=env)
        print("PASS: staged bootstrap credential for " + host["name"], flush=True)

    print("STOP AT TRUST BOUNDARY: Both credentials staged, agents remain disabled.", flush=True)
    print("Next: independently validate agent unit flags, CSR identities and authorization", flush=True)
    print("before manually starting enrollment agents. No CSR was approved.", flush=True)


if __name__ == "__main__":
    main()
