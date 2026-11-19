#!/usr/bin/env python3
"""Read-only N-host BYOH registration verification after CSR approval.

Verify issued certificate, registered ByoHost and remote permanent TLS client
identity. Never print private keys, client certificates, bootstrap credentials.
"""
import argparse
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import yaml

ROOT = Path(__file__).resolve().parents[2]


def stop(msg):
    raise SystemExit("STOP: " + msg)


def call(args, input_data=None):
    try:
        return subprocess.run(args, input=input_data, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, check=True, timeout=30,
                              cwd=ROOT).stdout
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
        stop("read-only check failed: " + args[0])


def load(relative, label):
    spec = importlib.util.spec_from_file_location(label, ROOT / relative)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


inventory_module = load("hack/dev/byoh-hosts-preflight.py", "byoh_register_verify_inventory")
csr_module = load("hack/dev/byoh-hosts-csr-verify.py", "byoh_register_verify_csr")


def get(kubeconfig, resource, name, namespace=None):
    args = ["kubectl", "--kubeconfig", str(kubeconfig)]
    if namespace:
        args += ["-n", namespace]
    args += ["get", resource, name, "-o", "json"]
    try:
        return json.loads(call(args))
    except (ValueError, TypeError):
        stop("invalid API object: " + resource + "/" + name)


def issued_certificate(csr, name):
    if csr.get("metadata", {}).get("name") != "byoh-csr-default." + name:
        stop("CSR identity mismatch: " + name)
    if csr.get("spec", {}).get("signerName") != "kubernetes.io/kube-apiserver-client":
        stop("CSR signer mismatch: " + name)
    if csr.get("spec", {}).get("usages") != ["client auth"]:
        stop("unexpected CSR usages: " + name)
    conditions = csr.get("status", {}).get("conditions", [])
    if not any(c.get("type") == "Approved" and c.get("status") == "True"
               for c in conditions) or any(
                   c.get("type") == "Denied" for c in conditions):
        stop("CSR not approved or denied: " + name)
    cert_b64 = csr.get("status", {}).get("certificate")
    if not cert_b64:
        stop("CSR certificate not yet issued: " + name)
    cert_pem = csr_module.b64(cert_b64)
    request_pem = csr_module.b64(csr["spec"]["request"])
    csr_module.csr_subject_and_signature(csr["spec"]["request"], name)
    cert_der = call(["openssl", "x509", "-inform", "PEM", "-outform", "DER"],
                    input_data=cert_pem)
    subject = call(["openssl", "x509", "-inform", "PEM",
                    "-subject", "-noout", "-nameopt", "RFC2253"],
                   input_data=cert_pem).decode().strip()
    expected_subject = "subject=CN=byoh:host:default:" + name + ",O=byoh:hosts"
    if subject != expected_subject:
        stop("issued certificate subject mismatch: " + name)
    call(["openssl", "x509", "-checkend", "0", "-noout"], input_data=cert_pem)
    csr_key = call(["openssl", "req", "-inform", "PEM", "-pubkey", "-noout"],
                   input_data=request_pem)
    cert_key = call(["openssl", "x509", "-inform", "PEM", "-pubkey", "-noout"],
                    input_data=cert_pem)
    if csr_key != cert_key:
        stop("issued certificate key differs from original CSR: " + name)
    return hashlib.sha256(cert_der).hexdigest()


REMOTE = r'''set -eu
test "$(systemctl is-active byoh-hostagent.service)" = active
file=/root/.byoh/config
test ! -L "$file"
test -s "$file"
test "$(stat -c '%a %U:%G' "$file")" = "600 root:root"
python3 - <<'PY'
import base64, json, subprocess, sys
# kubectl client config view validates kubeconfig structure without printing it.
path = "/root/.byoh/config"
p = subprocess.run(["kubectl", "--kubeconfig", path, "config", "view",
                    "--raw", "-o", "json"], capture_output=True, text=True)
if p.returncode:
    sys.exit("STOP: cannot inspect permanent kubeconfig safely")
cfg = json.loads(p.stdout)
clusters = cfg.get("clusters", [])
if len(clusters) != 1 or clusters[0].get("cluster", {}).get("insecure-skip-tls-verify"):
    sys.exit("STOP: unexpected permanent kubeconfig TLS mode")
users = cfg.get("users", [])
if len(users) != 1:
    sys.exit("STOP: unexpected permanent kubeconfig users")
user = users[0]["user"]
if "token" in user or "exec" in user or "auth-provider" in user:
    sys.exit("STOP: unexpected permanent kubeconfig auth mechanism")
cert = user.get("client-certificate-data")
key = user.get("client-key-data")
if not cert or not key:
    sys.exit("STOP: permanent client cert/key missing")
pem = base64.b64decode(cert, validate=True)
cert_hash = subprocess.run(["openssl", "x509", "-inform", "PEM",
                            "-outform", "DER"], input=pem, capture_output=True, check=True).stdout
import hashlib
print(hashlib.sha256(cert_hash).hexdigest())
PY
'''


def verify_one(kc, host):
    name = host["name"]
    csr = get(kc, "csr", "byoh-csr-default." + name)
    expected_hash = issued_certificate(csr, name)
    byohost = get(kc, "byohost", name, "default")
    if byohost.get("metadata", {}).get("name") != name:
        stop("wrong registered ByoHost name: " + name)
    if byohost.get("metadata", {}).get("deletionTimestamp"):
        stop("registered ByoHost deleting: " + name)
    if byohost.get("status", {}).get("agentInfo") is None:
        stop("ByoHost lacks agent status evidence: " + name)
    if byohost.get("spec", {}).get("byoMachineRef"):
        stop("ByoHost unexpectedly already bound to a Machine: " + name)
    peer = host["ssh"]["user"] + "@" + host["ssh"]["address"]
    output = call(["ssh", "-T", "-o", "BatchMode=yes", "-o",
                   "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                   peer, "sudo", "-n", "bash", "-s"], input_data=REMOTE.encode()).decode().strip()
    if not re.fullmatch(r"[0-9a-f]{64}", output):
        stop("invalid host certificate fingerprint: " + name)
    if output != expected_hash:
        stop("permanent host certificate differs from issued CSR: " + name)
    return name, byohost["metadata"]["uid"], csr["metadata"]["uid"], expected_hash


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    args = parser.parse_args()
    path = Path(args.inventory)
    if path.is_symlink() or not path.is_file():
        stop("inventory missing/symlink")
    try:
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError):
        stop("invalid inventory")
    kind, kc, _, hosts = inventory_module.validate(data)
    if kind != "ok-byoh-make-test" or os.environ.get("BYOH_KIND_NAME") != kind:
        stop("explicit BYOH management target required")
    context = call(["kubectl", "--kubeconfig", str(kc), "config",
                    "current-context"]).decode().strip()
    if context != "kind-" + kind:
        stop("management kubeconfig context mismatch")
    if call(["kubectl", "--kubeconfig", str(kc), "get",
             "--raw=/readyz"]).decode().strip() != "ok":
        stop("management API not ready")
    results = [verify_one(kc, host) for host in hosts]
    for name, byoh_uid, csr_uid, _ in results:
        print("PASS: " + name + " registered, active, issued CSR UID=" +
              csr_uid + ", ByoHost UID=" + byoh_uid +
              ", permanent certificate fingerprint verified", flush=True)
    print("PASS: " + str(len(results)) + " BYOH hosts verified; no changes made.")
    print("Bootstrap token revocation is a separate confirmed operation.")


if __name__ == "__main__":
    main()
