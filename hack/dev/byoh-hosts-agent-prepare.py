#!/usr/bin/env python3
"""Prepare BYOH systemd enrollment mode on inventory hosts; never start an agent.

Run only after staged bootstrap credentials have been transferred. Every host
write requires an explicit host-specific confirmation.
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
PREFLIGHT = ROOT / "hack/dev/byoh-hosts-preflight.py"
DROPIN = "/etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf"

# No unsanitized shell arguments. Invocation is via ssh -T with a literal sudo
# bash -s command, sending only our constant, versioned script on stdin.
CHECK = r'''set -eu
unit=byoh-hostagent.service
test "$(systemctl is-active "$unit" || true)" = inactive
test "$(systemctl is-enabled "$unit" || true)" = disabled
test -x /usr/local/bin/byoh-hostagent
test ! -e /root/.byoh/config
test -s /root/.byoh/bootstrap.kubeconfig
test "$(stat -c '%a %U:%G' /root/.byoh/bootstrap.kubeconfig)" = '600 root:root'
test -f /etc/systemd/system/byoh-hostagent.service
test ! -L /etc/systemd/system/byoh-hostagent.service
# Reject surprising ExecStart overrides; checked against the effective unit.
current="$(systemctl show -p ExecStart --value "$unit")"
case "$current" in
  *'/usr/local/bin/byoh-hostagent --namespace default --skip-installation'*)
    echo 'READY' ;;
  *'/usr/local/bin/byoh-hostagent --namespace default'*)
    echo 'NEEDS_UPDATE' ;;
  *) echo 'STOP: unexpected effective ExecStart' >&2; exit 1 ;;
esac
path=/etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf
if test -e "$path" || test -L "$path"; then
  test ! -L "$path"
  cmp -s "$path" /dev/stdin
else
  # Consume the desired drop-in even when it is absent.
  cat >/dev/null
fi
'''
# Both SSH checks/updates use a constant desired drop-in. It is not executable.
DESIRED = """[Service]
WorkingDirectory=/root
ExecStart=
ExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation
"""
# CHECK receives no user data; the expected drop-in is appended to its stdin.
# Bash 'cmp /dev/stdin' consumes the appended file; CHECK above is provided
# separately from the drop-in.
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
  *'/usr/local/bin/byoh-hostagent --namespace default --skip-installation'*) ;;
  *'/usr/local/bin/byoh-hostagent --namespace default'*) ;;
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
  *'/usr/local/bin/byoh-hostagent --namespace default --skip-installation'*) ;;
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


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--inventory", required=True)
    args = p.parse_args()
    path = Path(args.inventory)
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

    peers = [(h["name"], h["ssh"]["user"] + "@" + h["ssh"]["address"]) for h in hosts]
    for name, peer in peers:
        print(name + ": " + run_ssh(peer, PREFLIGHT_REMOTE), flush=True)

    print("Will only install a systemd enrollment drop-in on these two hosts.", flush=True)
    print("No agent start, CSR approval, kubeadm installation, or infra changes.", flush=True)
    for name, peer in peers:
        expected_confirmation = "prepare-agent-" + name
        entered = input("Type " + expected_confirmation + " to proceed: ")
        if entered != expected_confirmation:
            fail("confirmation not given; no change to " + name)
        print(name + ": " + run_ssh(peer, APPLY_REMOTE), flush=True)

    print("PASS: both agents configured for --skip-installation and remain disabled.", flush=True)


if __name__ == "__main__":
    main()
