#!/usr/bin/env python3
"""Prepare verified BYOH host bootstrap credentials using the existing E2E helper.

Phase 1 only: create and securely transfer bootstrap credentials for inventory hosts.
Never start agents or approve CSRs; stop before the identity trust boundary.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from datetime import datetime, timezone

import yaml
from importlib.machinery import SourceFileLoader
from importlib.util import module_from_spec, spec_from_loader

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/scaleway-e2e/byoh-e2e.sh"
PREFLIGHT = ROOT / "hack/dev/byoh-hosts-preflight.py"
AGENT_PREP = ROOT / "hack/dev/byoh-hosts-agent-prepare.py"


def fail(message):
    sys.exit("STOP: " + message)


def call(argv, *, env=None, confirmation=None):
    try:
        subprocess.run(argv, check=True, cwd=ROOT, env=env,
                       input=(confirmation + "\n") if confirmation else None, text=True)
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

    # Read-only identity/freshness checks before reusing an existing object.
    # The controller's token lifetime is 30 minutes. Do not resume old tokens.
    ca = subprocess.run(
        ["kubectl", "--kubeconfig", str(kubeconfig), "config", "view",
         "--raw", "--minify", "-o",
         "jsonpath={.clusters[0].cluster.certificate-authority-data}"],
        capture_output=True, text=True, check=False,
    )
    if ca.returncode != 0 or not ca.stdout.strip():
        fail("cannot verify management CA")

    def kubectl(host, template):
        result = subprocess.run(
            ["kubectl", "--kubeconfig", str(kubeconfig), "-n", "default",
             "get", "bootstrapkubeconfig", host, "-o", "go-template=" + template],
            cwd=ROOT, capture_output=True, text=True, check=False,
        )
        if result.returncode != 0:
            fail("cannot inspect bootstrap object for " + host)
        return result.stdout.strip()

    def exists(host):
        result = subprocess.run(
            ["kubectl", "--kubeconfig", str(kubeconfig), "-n", "default",
             "get", "bootstrapkubeconfig", host, "--ignore-not-found",
             "-o", "name"],
            cwd=ROOT, capture_output=True, text=True, check=False,
        )
        if result.returncode != 0:
            fail("bootstrap resource lookup failed for " + host)
        return bool(result.stdout.strip())

    def verify_existing(host):
        actual_url = kubectl(host, "{{.spec.apiserver}}")
        actual_ca = kubectl(host, '{{index .spec "certificate-authority-data"}}')
        if actual_url != "https://127.0.0.1:" + str(port) or actual_ca != ca.stdout.strip():
            fail("existing bootstrap target or CA mismatch on " + host)
        created = kubectl(host, "{{.metadata.creationTimestamp}}")
        try:
            age = (datetime.now(timezone.utc) -
                   datetime.fromisoformat(created.replace("Z", "+00:00"))).total_seconds()
        except ValueError:
            fail("invalid bootstrap object creation time on " + host)
        if age < -60 or age >= 20 * 60:
            fail("bootstrap object older than 20 minutes; inspect token expiration and revoke explicitly: " + host)
        print("PASS: existing bootstrap object matches current management CA and endpoint: " + host, flush=True)

    def wait_for_credential(host):
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            if kubectl(host, "{{if .status.bootstrapKubeconfigData}}ready{{end}}") == "ready":
                print("PASS: bootstrap credential populated for " + host, flush=True)
                return
            time.sleep(2)
        fail("bootstrap status not populated after 90s for " + host +
             "; inspect controller logs and existing object, do not recreate automatically")

    # One operator decision authorizes the exact plan, never an unknown host.
    # The existing helper still performs its own pre-write checks and confirmation;
    # its expected phrase is supplied only after explicit plan authorization.
    actions = [{"host": h["name"], "role": h["role"],
                "ssh": h["ssh"]["user"] + "@" + h["ssh"]["address"],
                "bootstrap": ("resume" if exists(h["name"]) else "create")}
               for h in hosts]
    plan = {"management": kind, "api_port": port, "inventory": str(inventory),
            "hosts": actions, "operations": ["create-if-absent", "transfer-if-absent", "prepare-agent-systemd"],
            "agent_start": False, "csr_approval": False}
    fingerprint = hashlib.sha256(
        json.dumps(plan, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    print("BYOH bootstrap plan (" + str(len(hosts)) + " hosts):", flush=True)
    for action in actions:
        print("  " + action["host"] + " " + action["role"] + " " +
              action["ssh"] + " bootstrap=" + action["bootstrap"], flush=True)
    print("Plan sha256: " + fingerprint, flush=True)
    print("Changes: create missing BootstrapKubeconfigs, transfer credentials, prepare stopped agent units.", flush=True)
    print("Agent start: NO; CSR approval: NO; Kubernetes installation: NO.", flush=True)
    expected_phrase = "ok " + fingerprint[:12]
    try:
        approved = input("Type '" + expected_phrase + "' to approve this exact plan: ")
    except EOFError:
        fail("interactive confirmation required")
    if approved != expected_phrase:
        fail("plan not authorized; no bootstrap changes")
    # No inventory edits may be accepted between authorization and writes.
    if yaml.safe_load(inventory.read_text(encoding="utf-8")) != data:
        fail("inventory changed since plan approval")

    for host in hosts:
        env = dict(os.environ)
        env.update({
            "HOST_WORKER": host["name"],
            "WORKER_SSH": host["ssh"]["user"] + "@" + host["ssh"]["address"],
            "MGMT_KUBECONFIG": str(kubeconfig),
            "MGMT_FORWARD_URL": "https://127.0.0.1:" + str(port),
            "NAMESPACE": "default",
        })
        if exists(host["name"]):
            verify_existing(host["name"])
            print("Resuming existing bootstrap object for " + host["name"], flush=True)
        else:
            call(["bash", str(SCRIPT), "enroll-create"], env=env,
                 confirmation="create-bootstrap-" + host["name"])
            verify_existing(host["name"])
        wait_for_credential(host["name"])
        # Reuse the existing secure transfer helper, which refuses to overwrite
        # preexisting remote credentials and requires a typed confirmation.
        call(["bash", str(SCRIPT), "enroll-transfer"], env=env,
             confirmation="transfer-bootstrap-" + host["name"])
        print("PASS: staged bootstrap credential for " + host["name"], flush=True)

    # The single approval also covered agent-unit preparation. Import the
    # existing checked implementation directly; never expose a bypass flag.
    agent_loader = SourceFileLoader("byoh_hosts_agent_prepare", str(AGENT_PREP))
    agent_spec = spec_from_loader(agent_loader.name, agent_loader)
    agent_module = module_from_spec(agent_spec)
    agent_loader.exec_module(agent_module)
    agent_module.prepare_hosts(str(inventory), authorized_inventory=data,
                               authorized_hosts=hosts)

    print("STOP AT TRUST BOUNDARY: Bootstrap credentials staged for " + str(len(hosts)) +
          " hosts, agents remain disabled.", flush=True)
    print("Next: independently validate agent unit flags, CSR identities and authorization", flush=True)
    print("before manually starting enrollment agents. No CSR was approved.", flush=True)


if __name__ == "__main__":
    main()
