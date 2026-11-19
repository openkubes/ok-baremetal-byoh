#!/usr/bin/env python3
"""Scaleway E2E workload API tunnel + isolated kind NAT plan / confirmed up.

Only for the disposable ok-byoh-make-test lab. This is NOT the supported
WireGuard/Private Network architecture. Never touches Scaleway firewall/OpenTofu.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import time
import yaml

ROOT = Path(__file__).resolve().parents[2]
CP_ADDR = "51.158.62.217"
MGMT_KIND = "ok-byoh-make-test"
CONTAINER = "ok-byoh-make-test-control-plane"
DOCKER_CONTEXT = "colima-ok-cli-kvm"
LOCAL_PORT = 16444
GUEST_IP = "192.168.5.2"
TARGET_PORT = 6443


def stop(message):
    raise SystemExit("STOP: " + message)


def run(cmd, timeout=20, *, allow_fail=False):
    try:
        p = subprocess.run(cmd, cwd=ROOT, stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        stop("command timed out or unavailable: " + cmd[0])
    if p.returncode and not allow_fail:
        stop("command failed (exit " + str(p.returncode) + "): " + cmd[0])
    return p


def out(cmd, timeout=20):
    return run(cmd, timeout=timeout).stdout.decode().strip()


def container_cmd(*args, allow_fail=False):
    return run(["docker", "--context", DOCKER_CONTEXT, "exec",
                CONTAINER, *args], timeout=12, allow_fail=allow_fail)


def port_open():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.settimeout(1)
        return sock.connect_ex(("127.0.0.1", LOCAL_PORT)) == 0


def route_rule(chain):
    return ["-t", "nat", "-d", CP_ADDR + "/32", "-p", "tcp",
            "--dport", str(TARGET_PORT), "-j", "DNAT",
            "--to-destination", GUEST_IP + ":" + str(LOCAL_PORT)]


def rule_present(chain):
    return container_cmd("iptables", "-t", "nat", "-C", chain,
                         "-d", CP_ADDR + "/32", "-p", "tcp",
                         "--dport", str(TARGET_PORT), "-j", "DNAT",
                         "--to-destination", GUEST_IP + ":" + str(LOCAL_PORT),
                         allow_fail=True).returncode == 0


def verify_guardrails(path):
    if path.is_symlink() or not path.is_file():
        stop("inventory missing or symlink")
    raw = path.read_bytes()
    try:
        data = yaml.safe_load(raw)
    except yaml.YAMLError:
        stop("malformed inventory")
    spec = importlib.util.spec_from_file_location(
        "network_hosts_inventory", ROOT / "hack/dev/byoh-hosts-preflight.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    kind, kc, _, hosts = module.validate(data)
    if kind != MGMT_KIND or os.environ.get("BYOH_KIND_NAME") != MGMT_KIND:
        stop("only explicitly selected disposable kind management is supported")
    cp = [h for h in hosts if h["role"] == "control-plane"]
    if len(cp) != 1 or cp[0]["name"] != "ok-bm-lab-01" or cp[0]["ssh"]["address"] != CP_ADDR:
        stop("lab control-plane host identity drift")
    if out(["docker", "context", "show"]) != DOCKER_CONTEXT:
        stop("unexpected Docker context: select " + DOCKER_CONTEXT)
    if MGMT_KIND not in out(["kind", "get", "clusters"]).splitlines():
        stop("target kind absent")
    if out(["kubectl", "--kubeconfig", str(kc),
            "config", "current-context"]) != "kind-" + MGMT_KIND:
        stop("management kubeconfig context mismatch")
    if out(["kubectl", "--kubeconfig", str(kc),
            "get", "--raw=/readyz"]) != "ok":
        stop("management API unavailable")
    if out(["docker", "--context", DOCKER_CONTEXT, "inspect", "-f",
            "{{.Name}}", CONTAINER]) != "/" + CONTAINER:
        stop("unexpected kind container")
    gateway = container_cmd("getent", "ahostsv4", "host.lima.internal")
    if not gateway.stdout.decode().startswith(GUEST_IP + " "):
        stop("kind host.lima.internal no longer resolves to historical gateway")
    # Detect existing foreign port mapping and unexpected NAT redirects.
    nat = container_cmd("iptables", "-t", "nat", "-S")
    lines = nat.stdout.decode().splitlines()
    for line in lines:
        if CP_ADDR in line and "--dport " + str(TARGET_PORT) in line and "-j DNAT" in line:
            if (GUEST_IP + ":" + str(LOCAL_PORT)) not in line:
                stop("conflicting kind workload API DNAT rule")
    return raw, cp[0], {name: rule_present(name) for name in ("PREROUTING", "OUTPUT")}


def probe():
    # A 200 is connectivity only; do not treat -k as TLS trust validation.
    res = container_cmd("curl", "-ksS", "--connect-timeout", "2", "--max-time", "5",
                        "-o", "/dev/null", "-w", "%{http_code}",
                        "https://host.lima.internal:16444/readyz", allow_fail=True)
    return res.returncode == 0 and res.stdout.decode().strip() == "200"


def plan_hash(raw, cp, rules):
    data = {
        "management": MGMT_KIND, "container": CONTAINER,
        "cp": cp["name"], "endpoint": CP_ADDR + ":" + str(TARGET_PORT),
        "ssh": cp["ssh"]["user"] + "@" + CP_ADDR,
        "forward": "127.0.0.1:16444 -> 127.0.0.1:6443",
        "dnat": GUEST_IP + ":16444", "nat_rules": rules,
        "inventory_sha256": hashlib.sha256(raw).hexdigest(),
    }
    return hashlib.sha256(json.dumps(
        data, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    parser.add_argument("--up", action="store_true")
    args = parser.parse_args()
    path = Path(args.inventory)
    raw, cp, rules = verify_guardrails(path)
    digest = plan_hash(raw, cp, rules)
    print("BYOH workload API network " +
          ("UP" if args.up else "READ-ONLY PLAN") + ": " + MGMT_KIND)
    print("SSH forwarding: local 127.0.0.1:16444 -> " + CP_ADDR + ":127.0.0.1:6443")
    print("kind-only DNAT: " + CP_ADDR + ":6443 -> " + GUEST_IP + ":16444")
    print("Local TCP listener present: " + str(port_open()))
    print("Existing kind NAT rules: " + json.dumps(rules, sort_keys=True))
    print("No changes to hosts, other kind clusters, Scaleway or OpenTofu.")
    print("Plan SHA-256: " + digest, flush=True)
    if not args.up:
        print("PASS: network plan inspected without writes.")
        return
    phrase = "network " + digest[:12]
    try:
        entered = input("Type '" + phrase + "' to configure lab network: ")
    except EOFError:
        stop("interactive authorization required")
    if entered != phrase:
        stop("plan not authorized")
    raw2, cp2, rules2 = verify_guardrails(path)
    if plan_hash(raw2, cp2, rules2) != digest:
        stop("network or inventory drift after approval")
    if port_open():
        stop("local TCP port occupied; do not take over a foreign listener")
    # Detached SSH forward. No remote commands, remote firewall writes, or
    # agent restarts. Existing SSH key verification remains enabled.
    peer = cp["ssh"]["user"] + "@" + CP_ADDR
    run(["ssh", "-f", "-N", "-T", "-o", "BatchMode=yes",
         "-o", "StrictHostKeyChecking=yes", "-o",
         "ExitOnForwardFailure=yes", "-o", "ConnectTimeout=8",
         "-L", "127.0.0.1:16444:127.0.0.1:6443", peer], timeout=20)
    for _ in range(10):
        if port_open():
            break
        time.sleep(0.3)
    if not port_open():
        stop("SSH forward not listening after start; inspect process")
    print("PASS: isolated local SSH forward now listening.")
    # A future API may not yet exist. If absent, leave NAT unchanged; rerun
    # later only after reviewing this partial tunnel state.
    if not probe():
        stop("workload API not reachable from kind via existing gateway yet; "
             "local tunnel exists, NAT unchanged. Review before continuing")
    # Only install exact rules into the dedicated kind container, no foreign
    # containers or Scaleway hosts are changed.
    for chain in ("PREROUTING", "OUTPUT"):
        if not rule_present(chain):
            args = ["iptables", "-t", "nat", "-I", chain, "1",
                    "-d", CP_ADDR + "/32", "-p", "tcp",
                    "--dport", str(TARGET_PORT), "-j", "DNAT",
                    "--to-destination", GUEST_IP + ":16444"]
            result = container_cmd(*args)
            if result.returncode:
                stop("failed to insert isolated NAT rule: " + chain)
    if not all(rule_present(c) for c in ("PREROUTING", "OUTPUT")):
        stop("kind-only NAT not verified")
    print("PASS: exact NAT rules installed on isolated kind container.")
    print("Network forwarding ready at TCP level; TLS trust still requires normal CAPI verification.")


if __name__ == "__main__":
    main()
