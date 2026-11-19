#!/usr/bin/env python3
"""Guarded Scaleway BYOH control-plane installation.

Default mode is read-only. --install performs one explicitly authorized
systemd drop-in write and starts the stopped bound BYOH agent exactly once.
No token creation, CSR approval, CAPI writes or infrastructure changes.
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


def load(relative, name):
    spec = importlib.util.spec_from_file_location(name, ROOT / relative)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


V = load("hack/dev/byoh-cluster-control-plane-verify.py", "cp_install_verify")
P = V.P
I = V.I
DROPIN = "/etc/systemd/system/byoh-hostagent.service.d/95-ok-byoh-cp-install.conf"
EXPECTED_90 = """[Service]
WorkingDirectory=/root
ExecStart=
ExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation --bootstrap-kubeconfig=/root/.byoh/bootstrap.kubeconfig"""
LEGACY_90 = """[Service]
WorkingDirectory=/root
ExecStart=
ExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation"""
NEW_DROPIN = """[Service]
WorkingDirectory=/root
ExecStart=
ExecStart=/usr/local/bin/byoh-hostagent --namespace default"""


def stop(reason):
    P.stop(reason)


def ssh(peer, script):
    return P.run(["ssh", "-T", "-o", "BatchMode=yes",
                  "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=8",
                  peer, "sudo", "-n", "bash", "-s"], input_bytes=script.encode(),
                 timeout=35)


REMOTE_PREFLIGHT = r'''set -eu
unit=byoh-hostagent.service
test "$(systemctl is-active "$unit" || true)" = inactive || { echo 'STOP: agent running' >&2; exit 21; }
test "$(systemctl is-enabled "$unit" || true)" = disabled || { echo 'STOP: agent enabled' >&2; exit 22; }
test -x /usr/local/bin/byoh-hostagent || exit 23
test -s /root/.byoh/config || exit 24
test ! -L /root/.byoh/config || exit 25
test "$(stat -c '%a %U:%G' /root/.byoh/config)" = '600 root:root' || exit 26
test ! -e /root/.byoh/bootstrap.kubeconfig || exit 27
test ! -e /etc/kubernetes/admin.conf || exit 28
test ! -e /etc/kubernetes/manifests/kube-apiserver.yaml || exit 29
test ! -e /etc/systemd/system/byoh-hostagent.service.d/95-ok-byoh-cp-install.conf || exit 30
test ! -L /etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf || exit 31
test -f /etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf || exit 32
expected="$(printf '%s' '[Service]\nWorkingDirectory=/root\nExecStart=\nExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation --bootstrap-kubeconfig=/root/.byoh/bootstrap.kubeconfig')"
legacy="$(printf '%s' '[Service]\nWorkingDirectory=/root\nExecStart=\nExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation')"
actual="$(cat /etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf)"
{ test "$actual" = "$expected" || test "$actual" = "$legacy"; } || exit 35
test "$(stat -c '%a %U:%G' /etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf)" = '644 root:root' || exit 36
case "$(systemctl show -p ExecStart --value "$unit")" in
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default --skip-installation --bootstrap-kubeconfig=/root/.byoh/bootstrap.kubeconfig ;'*) ;;
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default --skip-installation ;'*) ;;
  *) echo 'STOP: unexpected effective enrollment ExecStart' >&2; exit 33 ;;
esac
test "$(systemctl show -p WorkingDirectory --value "$unit")" = /root || exit 34
sha256sum /etc/systemd/system/byoh-hostagent.service.d/90-ok-byoh-enrollment.conf | cut -d ' ' -f 1
'''


REMOTE_INSTALL = r'''set -eu
unit=byoh-hostagent.service
dir=/etc/systemd/system/byoh-hostagent.service.d
path="$dir/95-ok-byoh-cp-install.conf"
test "$(systemctl is-active "$unit" || true)" = inactive || exit 21
test "$(systemctl is-enabled "$unit" || true)" = disabled || exit 22
test -s /root/.byoh/config || exit 24
test ! -e /root/.byoh/bootstrap.kubeconfig || exit 27
test ! -e /etc/kubernetes/admin.conf || exit 28
test ! -e /etc/kubernetes/manifests/kube-apiserver.yaml || exit 29
test ! -e "$path" && test ! -L "$path" || exit 30
test ! -L "$dir/90-ok-byoh-enrollment.conf" || exit 30
test "$(sha256sum "$dir/90-ok-byoh-enrollment.conf" | cut -d ' ' -f 1)" = "$1" || exit 31
test "$(stat -c '%a %U:%G' "$dir/90-ok-byoh-enrollment.conf")" = '644 root:root' || exit 36
case "$(systemctl show -p ExecStart --value "$unit")" in
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default --skip-installation --bootstrap-kubeconfig=/root/.byoh/bootstrap.kubeconfig ;'*) ;;
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default --skip-installation ;'*) ;;
  *) exit 33 ;;
esac
tmp="$(mktemp "$dir/.95-ok-byoh-cp-install.XXXXXXXX")"
trap 'rm -f "$tmp"' EXIT
printf '%s\n' '[Service]' 'WorkingDirectory=/root' 'ExecStart=' 'ExecStart=/usr/local/bin/byoh-hostagent --namespace default' >"$tmp"
chmod 644 "$tmp"
chown root:root "$tmp"
ln "$tmp" "$path"
rm -f "$tmp"
trap - EXIT
systemctl daemon-reload
case "$(systemctl show -p ExecStart --value "$unit")" in
  *'argv[]=/usr/local/bin/byoh-hostagent --namespace default ;'*) ;;
  *) echo 'STOP: effective CP ExecStart not verified' >&2; exit 40 ;;
esac
test "$(systemctl show -p WorkingDirectory --value "$unit")" = /root || exit 41
test "$(systemctl is-enabled "$unit" || true)" = disabled || exit 42
systemctl start "$unit"
test "$(systemctl is-active "$unit")" = active || exit 43
test "$(systemctl is-enabled "$unit" || true)" = disabled || exit 44
echo PASS
'''


def validate_inventory(path):
    if not path.is_file() or path.is_symlink():
        stop("inventory missing or symlink")
    raw = path.read_bytes()
    try:
        data = yaml.safe_load(raw)
    except yaml.YAMLError:
        stop("invalid inventory")
    kind, kc, _, hosts = I.validate(data)
    if kind != V.MGMT or os.getenv("BYOH_KIND_NAME") != V.MGMT:
        stop("management target not explicitly selected")
    if P.run(["kubectl", "--kubeconfig", str(kc), "config", "current-context"]) != "kind-" + kind:
        stop("management context mismatch")
    if P.run(["kubectl", "--kubeconfig", str(kc), "get", "--raw=/readyz"]) != "ok":
        stop("management API unavailable")
    cps = [x for x in hosts if x["role"] == "control-plane"]
    if len(cps) != 1 or cps[0]["name"] != V.HOST or cps[0]["ssh"]["address"] != "51.158.62.217":
        stop("lab control-plane inventory drift")
    return raw, kc, cps[0]


def ready_installation(kc, machine_name):
    host = P.fetch(kc, "byohost", V.HOST, V.NAMESPACE)
    byom = P.fetch(kc, "byomachine", machine_name, V.NAMESPACE)
    # ByoMachineRef UID is part of attachment integrity, not just the name.
    ref = host.get("spec", {}).get("byoMachineRef", {})
    if ref.get("uid") != byom["metadata"].get("uid"):
        stop("ByoHost attachment UID differs from ByoMachine")
    installer = P.fetch(kc, "k8sinstallerconfig", machine_name, V.NAMESPACE)
    if installer.get("status", {}).get("ready") is not True:
        stop("K8sInstallerConfig not Ready")
    desired_ref = installer.get("status", {}).get("installationSecret")
    host_ref = host.get("spec", {}).get("installationSecret")
    if not isinstance(desired_ref, dict) or not isinstance(host_ref, dict):
        stop("installation Secret not published to ByoHost")
    if (host_ref.get("name") != desired_ref.get("name") or
            host_ref.get("namespace", V.NAMESPACE) != V.NAMESPACE or
            desired_ref.get("namespace", V.NAMESPACE) != V.NAMESPACE or
            host_ref.get("kind") != "Secret"):
        # ObjectReference fields are non-secret. Print only the exact
        # attributes needed to distinguish API conversion, namespace drift,
        # and a genuinely foreign Secret before any host action.
        print("Installation Secret reference evidence (read-only):")
        for field in ("name", "kind", "namespace", "apiVersion", "uid"):
            print("  installer.status.installationSecret." + field + ": " +
                  str(desired_ref.get(field, "<absent>")))
            print("  byohost.spec.installationSecret." + field + ": " +
                  str(host_ref.get(field, "<absent>")))
        stop("installation Secret reference mismatch; no fallback accepted")
    name = host_ref.get("name")
    if not isinstance(name, str) or not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", name):
        stop("unexpected installation Secret name")
    secret = P.fetch(kc, "secret", name, V.NAMESPACE)
    if secret["metadata"].get("deletionTimestamp") or not secret.get("data"):
        stop("installation Secret deleting or empty")
    bootstrap = host.get("spec", {}).get("bootstrapSecret")
    if not isinstance(bootstrap, dict) or bootstrap.get("kind") != "Secret":
        stop("bound ByoHost bootstrap Secret ref missing")
    return {"host_uid": host["metadata"]["uid"], "host_rv": host["metadata"]["resourceVersion"],
            "machine_uid": byom["metadata"]["uid"],
            "installation_secret_name": name,
            "installation_secret_uid": secret["metadata"]["uid"],
            "bootstrap_secret_name": bootstrap.get("name")}


def compute(path):
    raw, kc, cp = validate_inventory(path)
    machine, bootstrap_secret = V.verify_objects(kc)
    V.verify_remote(cp)
    installation = ready_installation(kc, machine)
    if installation["bootstrap_secret_name"] != bootstrap_secret:
        stop("ByoHost bootstrap Secret differs from KubeadmConfig")
    peer = cp["ssh"]["user"] + "@" + cp["ssh"]["address"]
    unit_hash = ssh(peer, REMOTE_PREFLIGHT)
    if not re.fullmatch(r"[a-f0-9]{64}", unit_hash):
        stop("unexpected enrollment unit hash")
    # Hashing only identities/metadata, never Secret payloads or client keys.
    proof = {"management": V.MGMT, "cluster": V.CLUSTER,
             "host": V.HOST, "machine": machine,
             "inventory_sha256": hashlib.sha256(raw).hexdigest(),
             "unit_sha256": unit_hash, **installation}
    digest = hashlib.sha256(json.dumps(proof, sort_keys=True,
                            separators=(",", ":")).encode()).hexdigest()
    return raw, peer, unit_hash, proof, digest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True)
    parser.add_argument("--install", action="store_true")
    args = parser.parse_args()
    path = Path(args.inventory)
    raw, peer, unit_hash, proof, digest = compute(path)
    print("BYOH CONTROL PLANE INSTALL " + ("ACTION" if args.install else "READ-ONLY PLAN"))
    print("Target: " + V.CLUSTER + " on " + V.HOST + " (Kubernetes " + V.VERSION + ")")
    print("Bound Machine: " + proof["machine"])
    print("Ready installation Secret: " + proof["installation_secret_name"] + " (contents withheld)")
    print("Agent action: remove enrollment flags by verified systemd override; start service")
    print("Other hosts, OpenTofu, Scaleway firewall, CAPI specs: NO CHANGES")
    print("Plan SHA-256: " + digest, flush=True)
    if not args.install:
        print("PASS: installation plan ready; no changes made")
        return

    phrase = "install " + digest[:12]
    try:
        answer = input("Type '" + phrase + "' to start kubeadm installation: ")
    except EOFError:
        stop("interactive confirmation required")
    if answer != phrase:
        stop("installation not authorized")
    raw2, peer2, hash2, proof2, digest2 = compute(path)
    if (raw2 != raw or peer2 != peer or hash2 != unit_hash or
            digest2 != digest or proof2 != proof):
        stop("inventory/attachment/service changed since approval")
    script = "set -- " + unit_hash + "\n" + REMOTE_INSTALL
    if ssh(peer, script) != "PASS":
        stop("control-plane agent activation was not verified")
    print("PASS: control-plane BYOH agent started; kubeadm actions may now execute.")
    print("STOP: inspect API and CAPI state before setting up workload network.")


if __name__ == "__main__":
    main()
