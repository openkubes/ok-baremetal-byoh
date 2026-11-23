#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Offline tests of kubeconfig_identity.py and of the identity checks of lib.sh (with a
stub kubectl): python3 -I hack/inplace/test/test_kubeconfig_identity.py"""
import base64
import hashlib
import importlib.util
import json
import os
import subprocess
import tempfile
import textwrap
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("ki", os.path.join(HERE, "..", "kubeconfig_identity.py"))
ki = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ki)

MGMT_CA = base64.b64encode(b"-----BEGIN CERTIFICATE-----\nmgmt\n-----END CERTIFICATE-----\n").decode()
WL_CA = base64.b64encode(b"-----BEGIN CERTIFICATE-----\nworkload\n-----END CERTIFICATE-----\n").decode()
MGMT_SERVER = "https://172.16.0.10:6444"
WL_SERVER = "https://172.16.0.10:6443"


def kubeconfig(contexts):
    """contexts: [(context, cluster-dict)]"""
    return {
        "clusters": [{"name": f"c-{n}", "cluster": c} for n, c in contexts],
        "contexts": [{"name": n, "context": {"cluster": f"c-{n}", "user": "u"}} for n, _ in contexts],
        "current-context": contexts[0][0],
    }


def mgmt_cfg(server=MGMT_SERVER, ca=MGMT_CA, **extra):
    return kubeconfig([("other", {"server": "https://127.0.0.1:1", "certificate-authority-data": WL_CA}),
                       ("kind-ok-lab-mgmt", {"server": server, "certificate-authority-data": ca, **extra})])


def wl_cfg(server=WL_SERVER, ca=WL_CA, n=1):
    return kubeconfig([(f"ha1-admin@ha1{'' if i == 0 else i}", {"server": server, "certificate-authority-data": ca})
                       for i in range(n)])


SHA = hashlib.sha256(MGMT_CA.encode()).hexdigest()


class Management(unittest.TestCase):
    def test_pinned_context_passes_although_another_is_current(self):
        ki.management(mgmt_cfg(), "kind-ok-lab-mgmt", MGMT_SERVER, SHA)

    def test_refusals(self):
        cases = {
            "missing context": (mgmt_cfg(), "kind-other", MGMT_SERVER, SHA),
            "other server": (mgmt_cfg(server="https://127.0.0.1:64597"), "kind-ok-lab-mgmt", MGMT_SERVER, SHA),
            "other CA": (mgmt_cfg(ca=WL_CA), "kind-ok-lab-mgmt", MGMT_SERVER, SHA),
            "TLS opt-out": (mgmt_cfg(**{"insecure-skip-tls-verify": True}), "kind-ok-lab-mgmt", MGMT_SERVER, SHA),
            "proxy": (mgmt_cfg(**{"proxy-url": "http://p"}), "kind-ok-lab-mgmt", MGMT_SERVER, SHA),
            "no CA": (mgmt_cfg(ca=""), "kind-ok-lab-mgmt", MGMT_SERVER, SHA),
        }
        for name, args in cases.items():
            with self.assertRaises(ki.Mismatch, msg=name):
                ki.management(*args)


class Workload(unittest.TestCase):
    def test_matching(self):
        self.assertEqual(ki.workload(wl_cfg(), WL_SERVER, WL_CA), "ha1-admin@ha1")

    def test_refusals(self):
        for name, cfg in {"other server": wl_cfg(server="https://10.0.0.1:6443"), "other CA": wl_cfg(ca=MGMT_CA),
                          "two contexts": wl_cfg(n=2), "invalid CA": wl_cfg(ca="%%%")}.items():
            with self.assertRaises(ki.Mismatch, msg=name):
                ki.workload(cfg, WL_SERVER, WL_CA)


STUB = textwrap.dedent("""\
    #!/usr/bin/env bash
    # kubectl stub: config view prints the management or workload kubeconfig, get prints fixtures
    d=$STUB_DIR
    case " $* " in
    *" config view "*) if [[ " $* " == *" $d/mgmt.kubeconfig "* ]]; then cat "$d/mgmt.json"; else cat "$d/wl.json"; fi ;;
    *"secret ha1-kubeconfig"*) printf %s "$(base64 < "$d/wl.json" | tr -d '\\n')" ;;
    *"secret ha1-ca"*) cat "$d/wl-ca" ;;
    *"controlPlaneEndpoint"*) cat "$d/endpoint" ;;
    *"get cluster ha1"*) cat "$d/uid" ;;
    *) echo "stub: unexpected kubectl $*" >&2; exit 3 ;;
    esac
    """)


class LibSh(unittest.TestCase):
    """Sourcing lib.sh proves both identities or stops the script."""

    def run_lib(self, mgmt=None, wl=None, uid="uid-1", endpoint=WL_SERVER, wl_ca=WL_CA):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        d = tmp.name
        files = {"mgmt.json": json.dumps(mgmt or mgmt_cfg()), "wl.json": json.dumps(wl or wl_cfg()),
                 "uid": uid, "endpoint": endpoint, "wl-ca": wl_ca, "mgmt.kubeconfig": ""}
        for name, text in files.items():
            with open(os.path.join(d, name), "w") as f:
                f.write(text)
        os.makedirs(os.path.join(d, "bin"))
        with open(os.path.join(d, "bin", "kubectl"), "w") as f:
            f.write(STUB)
        os.chmod(os.path.join(d, "bin", "kubectl"), 0o755)
        env = {"PATH": f"{d}/bin:/usr/bin:/bin:/opt/homebrew/bin:/usr/local/bin", "STUB_DIR": d,
               "MGMT_KUBECONFIG": f"{d}/mgmt.kubeconfig", "MGMT_CONTEXT": "kind-ok-lab-mgmt",
               "MGMT_SERVER": MGMT_SERVER, "MGMT_CA_SHA256": SHA, "NS": "ha1", "CLUSTER": "ha1", "CLUSTER_UID": "uid-1"}
        script = f'. "{HERE}/../lib.sh"; echo "WCTX=$WCTX"; w version >/dev/null 2>&1 || true; echo sourced'
        return subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True)

    def test_identities_proved(self):
        r = self.run_lib()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("WCTX=ha1-admin@ha1", r.stdout)

    def test_refused(self):
        cases = {
            "management server": {"mgmt": mgmt_cfg(server="https://127.0.0.1:64597")},
            "management CA": {"mgmt": mgmt_cfg(ca=WL_CA)},
            "cluster UID": {"uid": "uid-2"},
            "workload endpoint": {"endpoint": "https://10.9.9.9:6443"},
            "workload CA": {"wl_ca": MGMT_CA},
        }
        for name, kw in cases.items():
            r = self.run_lib(**kw)
            self.assertNotEqual(r.returncode, 0, name)
            self.assertNotIn("sourced", r.stdout, name)
            self.assertIn("refused", r.stderr, name)


if __name__ == "__main__":
    unittest.main()
