#!/usr/bin/env python3
"""Confirmed N-host post-registration bootstrap revocation; no agent changes.

Requires verified permanent identities. Removes exactly owned token resources
then matching remote bootstrap files. Stops on partial failure; no blind retry.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import yaml

ROOT = Path(__file__).resolve().parents[2]
FINALIZER = "infrastructure.cluster.x-k8s.io/bootstrap-token-cleanup"


def stop(reason):
    raise SystemExit("STOP: " + reason)


def load(script, name):
    spec = importlib.util.spec_from_file_location(name, ROOT / script)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


R = load("hack/dev/byoh-hosts-register-verify.py", "register_verify_for_revocation")
I = load("hack/dev/byoh-hosts-preflight.py", "inventory_for_revocation")


def kubectl(kc, *args):
    return R.call(["kubectl", "--kubeconfig", str(kc), *args]).decode().strip()


def resource(kc, kind, name, namespace):
    text = kubectl(kc, "-n", namespace, "get", kind, name,
                   "--ignore-not-found", "-o", "json")
    if not text:
        return None
    try:
        return json.loads(text)
    except ValueError:
        stop("malformed API response for " + kind + "/" + name)


REMOTE_CHECK = r'''set -eu
test "$(systemctl is-active byoh-hostagent.service)" = active
test -s /root/.byoh/config
test ! -L /root/.byoh/config
test "$(stat -c '%a %U:%G' /root/.byoh/config)" = '600 root:root'
file=/root/.byoh/bootstrap.kubeconfig
test ! -L "$file"
if ! test -e "$file"; then echo ABSENT; exit 0; fi
test -f "$file"
test "$(stat -c '%a %U:%G' "$file")" = '600 root:root'
sha256sum "$file" | cut -d ' ' -f 1
'''

REMOTE_REMOVE = r'''set -eu
test "$(systemctl is-active byoh-hostagent.service)" = active
test -s /root/.byoh/config
test ! -L /root/.byoh/config
test "$(stat -c '%a %U:%G' /root/.byoh/config)" = '600 root:root'
file=/root/.byoh/bootstrap.kubeconfig
test ! -L "$file"
test -f "$file"
test "$(stat -c '%a %U:%G' "$file")" = '600 root:root'
test "$(sha256sum "$file" | cut -d ' ' -f 1)" = "$1"
rm -- "$file"
test ! -e "$file"
echo REMOVED
'''


def ssh(peer, script):
    return R.call(["ssh", "-T", "-o", "BatchMode=yes",
                   "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                   peer, "sudo", "-n", "bash", "-s"],
                  input_data=script.encode()).decode().strip()


def inspect(kc, host):
    name = host["name"]
    peer = host["ssh"]["user"] + "@" + host["ssh"]["address"]
    obj = resource(kc, "bootstrapkubeconfig", name, "default")
    remote_sha = ssh(peer, REMOTE_CHECK)
    if obj is None:
        if remote_sha != "ABSENT":
            stop("bootstrap API absent but host credential remains: " + name)
        return {"name": name, "peer": peer, "action": "already-revoked"}
    meta = obj.get("metadata", {})
    uid = meta.get("uid")
    if not uid or meta.get("deletionTimestamp") or FINALIZER not in meta.get("finalizers", []):
        stop("missing bootstrap UID/finalizer or deletion pending: " + name)
    token_id = hashlib.sha256(uid.encode()).hexdigest()[:6]
    token = resource(kc, "secret", "bootstrap-token-" + token_id, "kube-system")
    if token is None:
        stop("bootstrap Secret missing; manual review: " + name)
    if token.get("metadata", {}).get("annotations", {}).get(
            "byoh.openkubes.io/bootstrap-request-uid") != uid:
        stop("bootstrap Secret owner mismatch: " + name)
    credential = obj.get("status", {}).get("bootstrapKubeconfigData")
    if not credential or not isinstance(credential, str):
        stop("bootstrap credential status missing: " + name)
    expected = hashlib.sha256(credential.encode()).hexdigest()
    if remote_sha != expected:
        stop("remote bootstrap checksum differs from API credential: " + name)
    return {"name": name, "peer": peer, "action": "revoke", "bootstrap_uid": uid,
            "token_id": token_id, "credential_sha256": expected}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--inventory", required=True)
    p.add_argument("--revoke", action="store_true")
    args = p.parse_args()
    inv = Path(args.inventory)
    if not inv.is_file() or inv.is_symlink():
        stop("inventory missing or symlink")
    data_bytes = inv.read_bytes()
    try:
        data = yaml.safe_load(data_bytes)
    except yaml.YAMLError:
        stop("malformed inventory")
    kind, kc, _, hosts = I.validate(data)
    if kind != "ok-byoh-make-test" or os.getenv("BYOH_KIND_NAME") != kind:
        stop("explicit isolated kind target required")
    if kubectl(kc, "config", "current-context") != "kind-" + kind:
        stop("management context mismatch")
    if kubectl(kc, "get", "--raw=/readyz") != "ok":
        stop("management API not ready")
    # Strong post-registration evidence (no bootstrap-token dependency).
    for host in hosts:
        R.verify_one(kc, host)
    plan = [inspect(kc, host) for host in hosts]
    fingerprint = hashlib.sha256(json.dumps(
        {"management": kind, "hosts": plan, "operation": "revoke-owned-bootstrap"},
        sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    print("BYOH post-registration bootstrap " +
          ("REVOCATION" if args.revoke else "READ-ONLY PLAN") +
          ": " + str(len(plan)) + " hosts", flush=True)
    for item in plan:
        print("  " + item["name"] + " " + item["peer"] + " -> " + item["action"], flush=True)
    print("Plan SHA-256: " + fingerprint, flush=True)
    print("Permanent credentials, ByoHosts and systemd agents: PRESERVED.", flush=True)
    if not args.revoke:
        print("PASS: no changes made.", flush=True)
        return

    phrase = "revoke " + fingerprint[:12]
    try:
        answer = input("Type '" + phrase + "' to revoke these exact bootstrap credentials: ")
    except EOFError:
        stop("interactive confirmation required")
    if answer != phrase:
        stop("revocation not authorized")
    if inv.read_bytes() != data_bytes:
        stop("inventory changed after confirmation")
    # Recheck the complete plan BEFORE first write.
    for host, planned in zip(hosts, plan):
        R.verify_one(kc, host)
        if inspect(kc, host) != planned:
            stop("bootstrap state drift before revocation: " + host["name"])
    for host, planned in zip(hosts, plan):
        if planned["action"] == "already-revoked":
            print("PASS: already revoked for " + host["name"], flush=True)
            continue
        R.verify_one(kc, host)
        if inspect(kc, host) != planned:
            stop("bootstrap state changed during revocation: " + host["name"])
        name = host["name"]
        # The controller's finalizer must revoke the owned token first.
        kubectl(kc, "-n", "default", "delete", "bootstrapkubeconfig",
                name, "--wait=true", "--timeout=120s")
        if resource(kc, "bootstrapkubeconfig", name, "default"):
            stop("bootstrap object not removed: " + name)
        if resource(kc, "secret", "bootstrap-token-" + planned["token_id"], "kube-system"):
            stop("bootstrap token Secret not revoked: " + name)
        sha = planned["credential_sha256"]
        if not re.fullmatch(r"[0-9a-f]{64}", sha):
            stop("invalid planned SHA-256: " + name)
        if ssh(planned["peer"], "set -- " + sha + "\n" + REMOTE_REMOVE) != "REMOVED":
            stop("remote bootstrap cleanup unconfirmed: " + name)
        if ssh(planned["peer"], REMOTE_CHECK) != "ABSENT":
            stop("bootstrap file still present: " + name)
        R.verify_one(kc, host)
        print("PASS: bootstrap token revoked, exact host credential removed: " + name,
              flush=True)
    print("PASS: all bootstrap credentials revoked; permanent identities preserved.",
          flush=True)


if __name__ == "__main__":
    main()
