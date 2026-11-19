#!/usr/bin/env python3
"""Prepare BYOH systemd enrollment mode on inventory hosts; never start an agent.

Run only after staged bootstrap credentials have been transferred. Every host
write requires an explicit host-specific confirmation.
"""
import argparse
import hashlib
import json
from datetime import datetime, timezone
import os
from pathlib import Path
import subprocess
import sys
import yaml

from importlib.machinery import SourceFileLoader
from importlib.util import module_from_spec, spec_from_loader

ROOT = Path(__file__).resolve().parents[2]
PREFLIGHT = ROOT / "hack/dev/byoh-hosts-preflight.py"
DROPIN = "/etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf"

# No unsanitized shell arguments. Invocation is via ssh -T with a literal sudo
# bash -s command, sending only our constant, versioned script on stdin.
PREFLIGHT_REMOTE = r'''set -eu
unit=byoh-hostagent.service
test "$(systemctl is-active "$unit" || true)" = inactive
test "$(systemctl is-enabled "$unit" || true)" = disabled
test -x /usr/local/bin/byoh-hostagent
test ! -e /root/.byoh/config
test -s /root/.byoh/bootstrap.kubeconfig
test "$(stat -c '%a %U:%G' /root/.byoh/bootstrap.kubeconfig)" = '600 root:root'
test -f /etc/systemd/system/byoh-hostagent.service
test ! -L /etc/systemd/system/byoh-hostagent.service
case "$(systemctl show -p ExecStart --value "$unit")" in
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default --skip-installation ;'*) ;;
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default ;'*) ;;
  *) echo 'STOP: unrecognized ExecStart' >&2; exit 1 ;;
esac
path=/etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf
if test -e "$path" || test -L "$path"; then
  test ! -L "$path"
  test -f "$path"
  test "$(cat "$path")" = "$(printf '%s' '[Service]
WorkingDirectory=/root
ExecStart=
ExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation')"
fi
echo 'PASS: stopped agent and bootstrap credential; unit safely modifiable'
'''

APPLY_REMOTE = r'''set -eu
unit=byoh-hostagent.service
test "$(systemctl is-active "$unit" || true)" = inactive
test "$(systemctl is-enabled "$unit" || true)" = disabled
test -x /usr/local/bin/byoh-hostagent
test ! -e /root/.byoh/config
test -s /root/.byoh/bootstrap.kubeconfig
test "$(stat -c '%a %U:%G' /root/.byoh/bootstrap.kubeconfig)" = '600 root:root'
case "$(systemctl show -p ExecStart --value "$unit")" in
  *'/usr/local/bin/byoh-hostagent --namespace default --skip-installation'*) ;;
  *'/usr/local/bin/byoh-hostagent --namespace default'*) ;;
  *) echo 'STOP: unrecognized ExecStart' >&2; exit 1 ;;
esac
dir=/etc/systemd/system/byoh-hostagent.service.d
path="$dir/90-ok-byoh-enrollment.conf"
install -d -m 755 "$dir"
test ! -L "$path"
if test -e "$path"; then
  test -f "$path"
  test "$(cat "$path")" = "$(printf '%s' '[Service]
WorkingDirectory=/root
ExecStart=
ExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation')"
else
  tmp="$(mktemp "$dir/.90-ok-byoh-enrollment.XXXXXXXX")"
  trap 'rm -f "$tmp"' EXIT
  printf '%s' '[Service]
WorkingDirectory=/root
ExecStart=
ExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation
' >"$tmp"
  chmod 644 "$tmp"
  ln "$tmp" "$path"
  rm -f "$tmp"
  trap - EXIT
fi
systemctl daemon-reload
effective="$(systemctl show -p ExecStart --value "$unit")"
case "$effective" in
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default --skip-installation ;'*) ;;
  *) echo 'STOP: effective enrollment ExecStart not verified' >&2; exit 1 ;;
esac
test "$(systemctl show -p WorkingDirectory --value "$unit")" = /root
test "$(systemctl is-active "$unit" || true)" = inactive
test "$(systemctl is-enabled "$unit" || true)" = disabled
echo 'PASS: enrollment-only systemd drop-in installed; agent stopped/disabled'
'''


def fail(reason):
    sys.exit("STOP: " + reason)


def run_ssh(peer, script):
    try:
        result = subprocess.run(
            ["ssh", "-T", "-o", "BatchMode=yes", "-o",
             "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
             peer, "sudo", "-n", "bash", "-s"],
            input=script, text=True, capture_output=True,
            check=True, timeout=25,
        )
    except (OSError, subprocess.TimeoutExpired, subprocess.CalledProcessError):
        fail("remote unit check/update failed for " + peer)
    return result.stdout.strip()


def prepare_hosts(inventory_path, *, authorized_inventory=None, authorized_hosts=None):
    path = Path(inventory_path)
    if not path.is_file() or path.is_symlink():
        fail("inventory missing or symlink")
    try:
        inventory = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError):
        fail("invalid inventory")

    loader = SourceFileLoader("byoh_hosts_preflight", str(PREFLIGHT))
    spec = spec_from_loader(loader.name, loader)
    module = module_from_spec(spec)
    loader.exec_module(module)
    kind, kc, _, hosts = module.validate(inventory)
    if authorized_inventory is not None and inventory != authorized_inventory:
        fail("inventory changed after batch authorization")
    if authorized_hosts is not None and hosts != authorized_hosts:
        fail("host list changed after batch authorization")
    if kind != "ok-byoh-make-test" or os.environ.get("BYOH_KIND_NAME", kind) != kind:
        fail("only the explicitly selected disposable kind management is supported")
    # Verify target management identity without running preflight, which expects
    # bootstrap.kubeconfig to be absent (this phase occurs after transfer).
    if module.run(["kind", "get", "clusters"]).splitlines().count(kind) != 1:
        fail("target management kind missing")
    if module.run(["kubectl", "--kubeconfig", str(kc), "config", "current-context"]) != "kind-" + kind:
        fail("management kubeconfig context mismatch")
    actual = module.run(["kubectl", "--kubeconfig", str(kc), "config", "view", "--minify",
                         "-o", "jsonpath={.clusters[0].cluster.server}"])
    expected = yaml.safe_load(module.run(["kind", "get", "kubeconfig", "--name", kind]))
    if actual != expected["clusters"][0]["cluster"]["server"]:
        fail("management API identity mismatch")
    module.run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"])

    # Bootstrap tokens are issued with a 30-minute TTL. Stop before any
    # remote write when either request is stale or bound to a different API.
    current_ca = module.run([
        "kubectl", "--kubeconfig", str(kc), "config", "view",
        "--raw", "--minify", "-o",
        "jsonpath={.clusters[0].cluster.certificate-authority-data}",
    ])
    port = inventory["spec"]["management"]["remoteAPIPort"]
    for h in hosts:
        name = h["name"]
        try:
            raw = module.run(["kubectl", "--kubeconfig", str(kc), "-n", "default",
                              "get", "bootstrapkubeconfig", name, "-o", "json"])
            obj = __import__("json").loads(raw)
            spec = obj["spec"]
            created = datetime.fromisoformat(
                obj["metadata"]["creationTimestamp"].replace("Z", "+00:00"))
            age = (datetime.now(timezone.utc) - created).total_seconds()
            if age < -60 or age >= 20 * 60:
                fail("bootstrap token too old to prepare agent safely for " + name)
            if (spec.get("apiserver") != "https://127.0.0.1:" + str(port) or
                    spec.get("certificate-authority-data") != current_ca or
                    not obj.get("status", {}).get("bootstrapKubeconfigData")):
                fail("bootstrap target, CA, or status not verified for " + name)
        except (KeyError, ValueError, TypeError):
            fail("malformed bootstrap resource for " + name)

    peers = [(h["name"], h["ssh"]["user"] + "@" + h["ssh"]["address"]) for h in hosts]
    for name, peer in peers:
        print(name + ": " + run_ssh(peer, PREFLIGHT_REMOTE), flush=True)

    plan = {"management": kind, "inventory": str(path),
            "hosts": [{"name": n, "ssh": p} for n, p in peers],
            "operation": "install enrollment-only systemd drop-in",
            "agent_start": False, "csr_approval": False}
    digest = hashlib.sha256(
        json.dumps(plan, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    print("BYOH agent preparation plan: " + str(len(peers)) + " hosts", flush=True)
    for name, peer in peers:
        print("  " + name + " " + peer + " -> --skip-installation", flush=True)
    print("Plan sha256: " + digest, flush=True)
    print("No agent start, CSR approval, kubeadm installation, or infra changes.", flush=True)
    if authorized_hosts is None:
        expected_phrase = "ok " + digest[:12]
        try:
            entered = input("Type '" + expected_phrase + "' to approve all hosts: ")
        except EOFError:
            fail("interactive confirmation required")
        if entered != expected_phrase:
            fail("plan not authorized; no unit changes")
    else:
        print("Approved by the enclosing confirmed bootstrap plan.", flush=True)
    if yaml.safe_load(path.read_text(encoding="utf-8")) != inventory:
        fail("inventory changed since approval")
    for name, peer in peers:
        print(name + ": " + run_ssh(peer, APPLY_REMOTE), flush=True)

    print("PASS: " + str(len(peers)) +
          " agents configured for --skip-installation and remain disabled.", flush=True)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--inventory", required=True)
    args = p.parse_args()
    prepare_hosts(args.inventory)


if __name__ == "__main__":
    main()
