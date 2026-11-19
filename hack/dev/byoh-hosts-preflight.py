#!/usr/bin/env python3
"""Read-only, fail-closed BYOH host inventory and connectivity preflight."""
import argparse
import hashlib
import os
from pathlib import Path
import re
import socket
import ssl
import subprocess
import sys

try:
    import yaml
except ImportError:
    sys.exit("STOP: PyYAML required (install it in the selected Python environment)")

ROOT = Path(__file__).resolve().parents[2]
NAME = re.compile(r"^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$")
USER = re.compile(r"^[a-z_][a-z0-9_-]*$")
ADDRESS = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]*$")
ALLOWED_KINDS = {"ok-byoh-dev", "ok-byoh-make-test"}


def stop(reason):
    sys.exit("STOP: " + reason)


def run(args, *, input=None, timeout=15):
    try:
        return subprocess.run(args, input=input, text=True, capture_output=True,
                              check=True, timeout=timeout).stdout.strip()
    except (OSError, subprocess.TimeoutExpired, subprocess.CalledProcessError) as exc:
        stop("check failed (" + args[0] + "): " + type(exc).__name__)


def validate(data):
    if not isinstance(data, dict) or data.get("apiVersion") != "openkubes.io/v1alpha1" or data.get("kind") != "BYOHHostInventory":
        stop("unsupported inventory apiVersion/kind")
    spec = data.get("spec")
    if not isinstance(spec, dict) or not isinstance(spec.get("provider"), str) or not NAME.fullmatch(spec["provider"]):
        stop("invalid provider")
    mgmt = spec.get("management")
    if not isinstance(mgmt, dict):
        stop("missing management")
    kind = mgmt.get("kindCluster")
    if kind not in ALLOWED_KINDS or mgmt.get("accessMode") != "ssh-reverse-tunnel":
        stop("unexpected management kind or access mode")
    port = mgmt.get("remoteAPIPort")
    if type(port) is not int or not 1024 <= port <= 65535:
        stop("invalid remote API port")
    cfg = mgmt.get("kubeconfig")
    if not isinstance(cfg, str) or not cfg.startswith("~/.config/ok/"):
        stop("management kubeconfig must use dedicated ~/.config/ok/ location")
    resolved = Path(cfg).expanduser()
    if resolved != Path.home() / ".config" / "ok" / ("byoh-dev" if kind == "ok-byoh-dev" else "byoh-make-test") / "kubeconfig":
        stop("kubeconfig does not match kind target")
    if resolved.is_symlink() or not resolved.is_file():
        stop("dedicated kubeconfig missing or symlink")
    hosts = spec.get("hosts")
    if not isinstance(hosts, list) or not (2 <= len(hosts) <= 100):
        stop("inventory must contain 2 to 100 hosts")
    names, endpoints, roles = set(), set(), set()
    cp_count = 0
    worker_count = 0
    for h in hosts:
        if not isinstance(h, dict) or not isinstance(h.get("name"), str) or not NAME.fullmatch(h["name"]):
            stop("invalid host name")
        role = h.get("role")
        if role not in ("control-plane", "worker") or h["name"] in names:
            stop("duplicate/unsupported host role or name")
        cp_count += role == "control-plane"
        worker_count += role == "worker"
        ssh = h.get("ssh")
        if not isinstance(ssh, dict):
            stop("missing ssh host data")
        user, addr = ssh.get("user"), ssh.get("address")
        if not isinstance(user, str) or not USER.fullmatch(user):
            stop("invalid SSH user")
        if not isinstance(addr, str) or not ADDRESS.fullmatch(addr) or addr.startswith("REPLACE_"):
            stop("invalid SSH address")
        if (user, addr) in endpoints:
            stop("duplicate SSH endpoint")
        names.add(h["name"])
        endpoints.add((user, addr))
    if cp_count < 1 or worker_count < 1:
        stop("inventory needs at least one control-plane and one worker")
    return kind, resolved, port, hosts


REMOTE = r'''set -eu
test "$(systemctl is-active byoh-hostagent.service || true)" = inactive
test "$(systemctl is-enabled byoh-hostagent.service || true)" = disabled
sudo -n test -x /usr/local/bin/byoh-hostagent
sudo -n test ! -e /root/.byoh/config
sudo -n test ! -e /root/.byoh/bootstrap.kubeconfig
sudo -n sh -c 'set -- /root/.byoh/config.pre-reenroll-*; test -f "$1"'
curl --silent --show-error --insecure --fail --connect-timeout 5 --max-time 8 "https://127.0.0.1:$1/readyz" >/dev/null
openssl s_client -connect "127.0.0.1:$1" </dev/null 2>/dev/null | openssl x509 -outform DER | sha256sum | cut -d' ' -f1
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    args = parser.parse_args()
    path = Path(args.inventory)
    if not path.is_file() or path.is_symlink():
        stop("inventory missing or symlink")
    try:
        with path.open(encoding="utf-8") as f:
            data = yaml.safe_load(f)
    except (OSError, yaml.YAMLError) as exc:
        stop("invalid inventory: " + type(exc).__name__)
    kind, kubeconfig, port, hosts = validate(data)
    if os.getenv("BYOH_KIND_NAME") and os.environ["BYOH_KIND_NAME"] != kind:
        stop("BYOH_KIND_NAME does not match inventory")
    if kind not in run(["kind", "get", "clusters"]).splitlines():
        stop("target kind cluster absent")
    ctx = run(["kubectl", "--kubeconfig", str(kubeconfig), "config", "current-context"])
    if ctx != "kind-" + kind:
        stop("unexpected management context")
    server = run(["kubectl", "--kubeconfig", str(kubeconfig), "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}"])
    expected = yaml.safe_load(run(["kind", "get", "kubeconfig", "--name", kind]))
    expected_server = expected["clusters"][0]["cluster"]["server"]
    if server != expected_server:
        stop("management kubeconfig endpoint differs from kind")
    run(["kubectl", "--kubeconfig", str(kubeconfig), "get", "--raw=/readyz"])
    from urllib.parse import urlsplit
    target = urlsplit(server)
    if target.hostname not in ("127.0.0.1", "localhost") or target.scheme != "https":
        stop("unexpected kind API endpoint")
    try:
        with socket.create_connection((target.hostname, target.port), timeout=5) as sock:
            with ssl._create_unverified_context().wrap_socket(sock, server_hostname=target.hostname) as tls:
                fingerprint = hashlib.sha256(tls.getpeercert(binary_form=True)).hexdigest()
    except (OSError, ssl.SSLError) as exc:
        stop("management API certificate unavailable: " + type(exc).__name__)
    print("PASS: management " + kind + " verified (read-only)")
    for h in hosts:
        peer = h["ssh"]["user"] + "@" + h["ssh"]["address"]
        digest = run(["ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
                      "-o", "ConnectTimeout=8", peer, "bash", "-s", "--", str(port)],
                     input=REMOTE, timeout=25)
        if digest.lower() != fingerprint:
            stop("tunnel endpoint certificate mismatch on " + h["name"])
        print("PASS: " + h["name"] + " (" + h["role"] + "): agent disabled, old credentials retired, API tunnel matches management")
    print("PASS: BYOH hosts preflight; no cluster or host writes")


if __name__ == "__main__":
    main()
