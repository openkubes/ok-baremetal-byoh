#!/usr/bin/env python3
"""Plan or explicitly approve N inventory-bound BYOH CSRs with host key proof.

The approval path uses an optimistic-concurrency PUT to the Kubernetes CSR
/approval subresource, NOT an unchecked 'kubectl certificate approve'.
No token, private key, CSR request body, or issued cert is printed.
"""
import argparse
import copy
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import yaml

ROOT = Path(__file__).resolve().parents[2]
VERIFY = ROOT / "hack/dev/byoh-hosts-csr-verify.py"
PREFLIGHT = ROOT / "hack/dev/byoh-hosts-preflight.py"


def stop(msg):
    raise SystemExit("STOP: " + msg)


def module(path, label):
    spec = importlib.util.spec_from_file_location(label, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


V = module(VERIFY, "byoh_hosts_csr_base")
I = module(PREFLIGHT, "byoh_hosts_inventory")


def ssh_sign(host, challenge):
    """Sign a fresh non-secret nonce on host; return signature bytes only."""
    peer = host["ssh"]["user"] + "@" + host["ssh"]["address"]
    # The Go agent writes its generated CSR private key in WorkingDirectory=/root
    # as byoh-client.key.tmp while it waits for CSR issuance.
    check = b"""set -eu
test "$(systemctl is-active byoh-hostagent.service)" = active
test ! -e /root/.byoh/config
key=/root/byoh-client.key.tmp
test ! -L "$key"
test -f "$key"
test "$(stat -c '%a %U:%G' "$key")" = '600 root:root'
"""
    V.call(["ssh", "-T", "-o", "BatchMode=yes", "-o",
            "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
            peer, "sudo", "-n", "bash", "-s"], input_bytes=check)
    # No shell: the key stays on the host. The signature is binary, not a key.
    return V.call(["ssh", "-T", "-o", "BatchMode=yes", "-o",
                   "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                   peer, "sudo", "-n", "openssl", "dgst", "-sha256",
                   "-sign", "/root/byoh-client.key.tmp"], input_bytes=challenge)


def challenge_matches_public_key(csr_request, challenge, signature):
    request_pem = V.b64(csr_request)
    public_pem = V.call(["openssl", "req", "-in", "/dev/stdin",
                         "-noout", "-pubkey"], input_bytes=request_pem)
    if b"BEGIN PUBLIC KEY" not in public_pem:
        stop("CSR public key not available")
    # OpenSSL verifier requires file paths for the public key and signature.
    # Local temporary material is public key + signature, NEVER private key.
    with tempfile.TemporaryDirectory(prefix="ok-byoh-csr-proof-") as folder:
        public_path = Path(folder) / "public.pem"
        signature_path = Path(folder) / "signature.bin"
        public_path.write_bytes(public_pem)
        signature_path.write_bytes(signature)
        V.call(["openssl", "dgst", "-sha256", "-verify", str(public_path),
                "-signature", str(signature_path)], input_bytes=challenge)


def inspect_one(kubeconfig, management_ca, port, host):
    name = host["name"]
    boot = V.kget(kubeconfig, "bootstrapkubeconfig", name, "default")
    if boot.get("spec", {}).get("apiserver") != "https://127.0.0.1:" + str(port):
        stop("unexpected API endpoint for " + name)
    if boot.get("spec", {}).get("certificate-authority-data") != management_ca:
        stop("unexpected management CA for " + name)
    uid = boot["metadata"]["uid"]
    token_id = hashlib.sha256(uid.encode()).hexdigest()[:6]
    secret = V.kget(kubeconfig, "secret", "bootstrap-token-" + token_id, "kube-system")
    csr = V.kget(kubeconfig, "csr", "byoh-csr-default." + name)
    identity, expected_sha = V.evaluate_identity(
        host=host, csr=csr, bootstrap=boot, secret=secret,
        now=datetime.now(timezone.utc))
    V.csr_subject_and_signature(csr["spec"]["request"], name)
    if V.read_remote_digest(host) != expected_sha:
        stop("remote bootstrap credential mismatch: " + name)

    challenge = secrets.token_bytes(48)
    signed = ssh_sign(host, challenge)
    challenge_matches_public_key(csr["spec"]["request"], challenge, signed)

    # Re-fetch CSR after remote challenge in case it changed during proof.
    now_csr = V.kget(kubeconfig, "csr", csr["metadata"]["name"])
    if now_csr.get("metadata", {}).get("uid") != csr["metadata"]["uid"] or (
        now_csr.get("metadata", {}).get("resourceVersion") != csr["metadata"].get("resourceVersion")
    ) or now_csr.get("spec") != csr["spec"] or now_csr.get("status", {}).get("conditions"):
        stop("CSR changed during host proof: " + name)
    return {
        "name": name,
        "peer": host["ssh"]["user"] + "@" + host["ssh"]["address"],
        "csr_name": csr["metadata"]["name"],
        "csr_uid": csr["metadata"]["uid"],
        "csr_rv": csr["metadata"]["resourceVersion"],
        "csr_request_sha256": hashlib.sha256(
            V.b64(csr["spec"]["request"])).hexdigest(),
        "bootstrap_uid": uid, "token_id": identity,
    }


def collect(path, data):
    kind, kubeconfig, port, hosts = I.validate(data)
    if kind != "ok-byoh-make-test" or os.environ.get("BYOH_KIND_NAME", kind) != kind:
        stop("explicit disposable management target required")
    if V.call(["kubectl", "--kubeconfig", str(kubeconfig),
               "config", "current-context"]).decode().strip() != "kind-" + kind:
        stop("management context mismatch")
    if V.call(["kubectl", "--kubeconfig", str(kubeconfig),
               "get", "--raw=/readyz"]).decode().strip() != "ok":
        stop("management API not ready")
    ca = V.call(["kubectl", "--kubeconfig", str(kubeconfig), "config", "view",
                 "--raw", "--minify", "-o",
                 "jsonpath={.clusters[0].cluster.certificate-authority-data}"]).decode().strip()
    if not ca:
        stop("management CA missing")
    checks = [inspect_one(kubeconfig, ca, port, host) for host in hosts]
    payload = {"management": kind, "api_port": port,
               "operation": "approve exact CSR UID/resourceVersion after host-key challenge",
               "csrs": checks}
    digest = hashlib.sha256(json.dumps(
        payload, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return kubeconfig, port, ca, hosts, checks, digest


def approval_payload(csr):
    """Use current UID and RV as API preconditions on /approval PUT."""
    obj = copy.deepcopy(csr)
    if obj.get("status", {}).get("conditions") or obj.get("status", {}).get("certificate"):
        stop("CSR no longer pending")
    obj.setdefault("status", {})["conditions"] = [{
        "type": "Approved", "status": "True",
        "reason": "OpenKubesVerifiedHostKeyAndBootstrapToken",
        "message": "Operator approved inventory-bound CSR and SSH host-key challenge",
    }]
    # Exact UID and resourceVersion stay on the PUT object.
    return json.dumps(obj, separators=(",", ":")).encode()


def approve_one(kubeconfig, check):
    current = V.kget(kubeconfig, "csr", check["csr_name"])
    if (current.get("metadata", {}).get("uid") != check["csr_uid"] or
            current.get("metadata", {}).get("resourceVersion") != check["csr_rv"] or
            hashlib.sha256(V.b64(current["spec"]["request"])).hexdigest()
            != check["csr_request_sha256"]):
        stop("CSR UID/resourceVersion/request drift: " + check["name"])
    body = approval_payload(current)
    endpoint = ("/apis/certificates.k8s.io/v1/certificatesigningrequests/" +
                check["csr_name"] + "/approval")
    result = V.call(["kubectl", "--kubeconfig", str(kubeconfig), "replace",
                     "--raw", endpoint, "-f", "-"], input_bytes=body)
    try:
        approved = json.loads(result)
    except (ValueError, TypeError):
        stop("approval API response invalid")
    if (approved.get("metadata", {}).get("uid") != check["csr_uid"] or
            not any(c.get("type") == "Approved" and c.get("status") == "True"
                    for c in approved.get("status", {}).get("conditions", []))):
        stop("approval not confirmed for " + check["name"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    parser.add_argument("--approve", action="store_true")
    args = parser.parse_args()
    path = Path(args.inventory)
    if path.is_symlink() or not path.is_file():
        stop("inventory missing/symlink")
    raw = path.read_bytes()
    try:
        data = yaml.safe_load(raw)
    except yaml.YAMLError:
        stop("invalid host inventory")
    kc, port, ca, hosts, plan, digest = collect(path, data)
    print("BYOH CSR approval " + ("ACTION" if args.approve else "READ-ONLY PLAN") +
          ": " + str(len(plan)) + " hosts", flush=True)
    for item in plan:
        print("  " + item["name"] + " " + item["peer"] +
              " CSR UID=" + item["csr_uid"] +
              " token ID=" + item["token_id"] +
              " host key challenge=PASS", flush=True)
    print("Plan SHA-256: " + digest, flush=True)
    if not args.approve:
        print("PASS: no CSR approved; run explicit approval target after review.", flush=True)
        return

    expected = "approve " + digest[:12]
    try:
        typed = input("Type '" + expected + "' to approve these exact CSRs: ")
    except EOFError:
        stop("interactive approval required")
    if typed != expected:
        stop("plan not authorized")
    if raw != path.read_bytes():
        stop("inventory changed after authorization")
    # Recheck all identities and CSR states after approval; new evidence/UID
    # changes invalidate the entire plan before the first write.
    kc2, port2, ca2, _, verified, digest2 = collect(path, data)
    if kc2 != kc or port2 != port or ca2 != ca or digest2 != digest:
        stop("approval evidence changed; no CSRs approved")
    for item in verified:
        approve_one(kc, item)
        print("PASS: approved exact CSR UID for " + item["name"], flush=True)
    print("CSR approvals complete; certificate issuance and host registration require verification.", flush=True)


if __name__ == "__main__":
    main()
