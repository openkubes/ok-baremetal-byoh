#!/usr/bin/env python3
"""Read-only N-host BYOH CSR identity, token ownership and CSR signature verification.

Never print bootstrap kubeconfigs, Secrets, or CSR payloads. Never approve CSRs.
"""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import yaml

ROOT = Path(__file__).resolve().parents[2]
VALID_ID = re.compile(r"^[0-9a-f]{6}$")
TOKEN_PATTERN = re.compile(r"^[0-9a-f]{6}\.[0-9a-z]{16}$")


def stop(message):
    raise SystemExit("STOP: " + message)


def call(argv, *, input_bytes=None):
    try:
        p = subprocess.run(argv, input=input_bytes, stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE, check=True, cwd=ROOT, timeout=25)
        return p.stdout
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
        stop("read-only validation command failed: " + argv[0])


def kget(kc, kind, name, namespace=None):
    args = ["kubectl", "--kubeconfig", str(kc)]
    if namespace:
        args += ["-n", namespace]
    args += ["get", kind, name, "-o", "json"]
    try:
        return json.loads(call(args))
    except (ValueError, TypeError):
        stop("malformed Kubernetes object: " + kind + "/" + name)


def b64(value):
    try:
        return base64.b64decode(value, validate=True)
    except (ValueError, TypeError, base64.binascii.Error):
        stop("invalid base64 field")


def timestamp(value):
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00"))
        if result.tzinfo is None:
            raise ValueError("timezone absent")
        return result
    except (ValueError, AttributeError, TypeError):
        stop("invalid timestamp in CSR or bootstrap token")


def evaluate_identity(*, host, csr, bootstrap, secret, now):
    """Pure metadata/credential checks; may throw on drift or mismatch."""
    name = host["name"]
    prefix = "byoh-csr-default."
    if csr.get("metadata", {}).get("name") != prefix + name:
        stop("CSR name mismatch for " + name)
    if bootstrap.get("metadata", {}).get("name") != name:
        stop("bootstrap object mismatch for " + name)
    if bootstrap["metadata"].get("deletionTimestamp"):
        stop("bootstrap deleting for " + name)
    uid = bootstrap["metadata"].get("uid")
    if not uid:
        stop("missing bootstrap UID for " + name)
    token_id = hashlib.sha256(uid.encode()).hexdigest()[:6]
    if not VALID_ID.fullmatch(token_id):
        stop("invalid token ID")
    if secret.get("metadata", {}).get("name") != "bootstrap-token-" + token_id:
        stop("unexpected bootstrap Secret for " + name)
    if secret.get("metadata", {}).get("namespace") != "kube-system":
        stop("token Secret must be in kube-system")
    if secret.get("type") != "bootstrap.kubernetes.io/token":
        stop("unexpected bootstrap Secret type")
    if secret.get("metadata", {}).get("annotations", {}).get(
            "byoh.openkubes.io/bootstrap-request-uid") != uid:
        stop("token Secret not owned by request UID: " + name)

    encoded = secret.get("data", {})
    sid = b64(encoded.get("token-id"))
    svalue = b64(encoded.get("token-secret"))
    expires = timestamp(b64(encoded.get("expiration")).decode("ascii"))
    if not hmac.compare_digest(sid, token_id.encode()):
        stop("token ID and Secret mismatch for " + name)
    token = token_id.encode() + b"." + svalue
    if not TOKEN_PATTERN.fullmatch(token.decode("ascii")):
        stop("invalid issued bootstrap token for " + name)
    if now >= expires:
        stop("bootstrap token expired for " + name)

    status = bootstrap.get("status", {}).get("bootstrapKubeconfigData")
    if not isinstance(status, str) or not status:
        stop("missing bootstrap kubeconfig for " + name)
    try:
        kube = yaml.safe_load(status)
        auths = kube["users"]
        configured_tokens = [i["user"]["token"] for i in auths if "token" in i.get("user", {})]
    except (KeyError, TypeError, yaml.YAMLError):
        stop("malformed bootstrap kubeconfig for " + name)
    if len(configured_tokens) != 1 or not hmac.compare_digest(
            configured_tokens[0].encode(), token):
        stop("bootstrap kubeconfig token differs from owned Secret for " + name)

    spec = csr.get("spec", {})
    expected_requester = "system:bootstrap:" + token_id
    if spec.get("username") != expected_requester:
        stop("CSR requester not bound to expected token ID for " + name)
    if spec.get("signerName") != "kubernetes.io/kube-apiserver-client":
        stop("unexpected CSR signer for " + name)
    if spec.get("usages") != ["client auth"]:
        stop("unexpected CSR usages for " + name)
    expected_groups = {"system:bootstrappers", "system:bootstrappers:byoh",
                       "system:authenticated"}
    groups = spec.get("groups")
    if not isinstance(groups, list) or len(groups) != len(expected_groups) or set(groups) != expected_groups:
        stop("unexpected requester groups for " + name)
    if csr.get("status", {}).get("conditions") or csr.get("status", {}).get("certificate"):
        stop("CSR already approved/denied/issued for " + name)
    csr_created = timestamp(csr["metadata"]["creationTimestamp"])
    token_created = timestamp(secret["metadata"]["creationTimestamp"])
    if csr_created < token_created or csr_created > expires:
        stop("CSR issued outside bootstrap token lifetime for " + name)
    return token_id, hashlib.sha256(status.encode()).hexdigest()


def csr_subject_and_signature(encoded_request, name):
    pem = b64(encoded_request)
    # OpenSSL returns nonzero on malformed CSR or invalid signature.
    call(["openssl", "req", "-in", "/dev/stdin", "-noout", "-verify"], input_bytes=pem)
    subject = call(["openssl", "req", "-in", "/dev/stdin", "-noout",
                    "-subject", "-nameopt", "RFC2253"], input_bytes=pem).decode().strip()
    expected = "subject=CN=byoh:host:default:" + name + ",O=byoh:hosts"
    if subject != expected:
        stop("CSR subject mismatch for " + name)


def read_remote_digest(host):
    peer = host["ssh"]["user"] + "@" + host["ssh"]["address"]
    script = b"""set -eu
test "$(systemctl is-active byoh-hostagent.service)" = active
test ! -e /root/.byoh/config
file=/root/.byoh/bootstrap.kubeconfig
test ! -L "$file"
test -f "$file"
test "$(stat -c '%a %U:%G' "$file")" = '600 root:root'
sha256sum "$file" | cut -d ' ' -f 1
"""
    digest = call(["ssh", "-T", "-o", "BatchMode=yes", "-o",
                   "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                   peer, "sudo", "-n", "bash", "-s"], input_bytes=script).decode().strip()
    if not re.fullmatch(r"[0-9a-f]{64}", digest):
        stop("invalid remote bootstrap checksum for " + host["name"])
    return digest


def main():
    from importlib.machinery import SourceFileLoader
    from importlib.util import module_from_spec, spec_from_loader
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    args = parser.parse_args()
    path = Path(args.inventory)
    if not path.is_file() or path.is_symlink():
        stop("invalid local inventory")
    try:
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError):
        stop("inventory invalid")
    loader = SourceFileLoader("hosts_preflight_csr", str(ROOT / "hack/dev/byoh-hosts-preflight.py"))
    spec = spec_from_loader(loader.name, loader)
    module = module_from_spec(spec)
    loader.exec_module(module)
    kind, kubeconfig, port, hosts = module.validate(data)
    if kind != "ok-byoh-make-test" or os.environ.get("BYOH_KIND_NAME", kind) != kind:
        stop("management cluster is not explicitly selected disposable kind")
    if call(["kubectl", "--kubeconfig", str(kubeconfig), "config",
             "current-context"]).decode().strip() != "kind-" + kind:
        stop("management kubeconfig context mismatch")
    if call(["kubectl", "--kubeconfig", str(kubeconfig), "get",
             "--raw=/readyz"]).decode().strip() != "ok":
        stop("management API not ready")
    management_ca = call(["kubectl", "--kubeconfig", str(kubeconfig),
                          "config", "view", "--raw", "--minify", "-o",
                          "jsonpath={.clusters[0].cluster.certificate-authority-data}"]).decode().strip()
    results = []
    for host in hosts:
        name = host["name"]
        bootstrap = kget(kubeconfig, "bootstrapkubeconfig", name, "default")
        if bootstrap.get("spec", {}).get("apiserver") != "https://127.0.0.1:" + str(port):
            stop("unexpected bootstrap API endpoint: " + name)
        if bootstrap.get("spec", {}).get("certificate-authority-data") != management_ca:
            stop("unexpected management CA: " + name)
        token_id = hashlib.sha256(bootstrap["metadata"]["uid"].encode()).hexdigest()[:6]
        secret = kget(kubeconfig, "secret", "bootstrap-token-" + token_id, "kube-system")
        csr = kget(kubeconfig, "csr", "byoh-csr-default." + name)
        identity, expected_digest = evaluate_identity(
            host=host, csr=csr, bootstrap=bootstrap,
            secret=secret, now=datetime.now(timezone.utc))
        csr_subject_and_signature(csr["spec"]["request"], name)
        if read_remote_digest(host) != expected_digest:
            stop("host credential differs from management bootstrap status: " + name)
        results.append((name, identity, csr["metadata"]["uid"]))
    print("BYOH CSR independent identity verification (read-only):")
    for name, identity, uid in results:
        print("PASS " + name + ": token ID=" + identity +
              ", CSR UID=" + uid + ", subject/signer/groups/usages/signature validated")
    print("PASS: " + str(len(results)) + " CSRs identity-bound to owned bootstrap Secrets.")
    print("STOP AT TRUST BOUNDARY: no CSR approval has been performed.")


if __name__ == "__main__":
    main()
