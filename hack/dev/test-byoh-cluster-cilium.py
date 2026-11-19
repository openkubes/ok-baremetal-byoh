#!/usr/bin/env python3
"""Offline Cilium plan safety regressions: never mutate a live cluster."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "cilium_module", Path(__file__).with_name("byoh-cluster-cilium.py"))
C = importlib.util.module_from_spec(spec)
spec.loader.exec_module(C)


def config():
    return {"apiVersion": "v1", "kind": "Config",
            "clusters": [{"name": "kubernetes", "cluster": {
                "server": "https://51.158.62.217:6443",
                "certificate-authority-data": "BASE64-CA"}}],
            "users": [{"name": "admin", "user": {
                "client-certificate-data": "BASE64-CERT",
                "client-key-data": "BASE64-KEY"}}],
            "contexts": [{"name": "admin@kubernetes", "context": {
                "cluster": "kubernetes", "user": "admin"}}],
            "current-context": "admin@kubernetes"}


class CiliumTests(unittest.TestCase):
    def test_kcp_v1beta2_initialization_field(self):
        kcp = {"apiVersion": "controlplane.cluster.x-k8s.io/v1beta2",
               "status": {"initialization": {"controlPlaneInitialized": True}}}
        self.assertTrue(C.kcp_initialized(kcp))

    def test_kcp_v1beta2_old_field_does_not_bypass_gate(self):
        kcp = {"apiVersion": "controlplane.cluster.x-k8s.io/v1beta2",
               "status": {"initialized": True}}
        self.assertFalse(C.kcp_initialized(kcp))

    def test_kcp_v1beta1_legacy_status(self):
        kcp = {"apiVersion": "controlplane.cluster.x-k8s.io/v1beta1",
               "status": {"initialized": True}}
        self.assertTrue(C.kcp_initialized(kcp))

    def test_kcp_v1beta2_uninitialized_rejected(self):
        kcp = {"apiVersion": "controlplane.cluster.x-k8s.io/v1beta2",
               "status": {"initialization": {"controlPlaneInitialized": False}}}
        self.assertFalse(C.kcp_initialized(kcp))
        self.assertFalse(C.kcp_initialized({"status": {}}))

    def test_values_pin_kube_proxy_mode(self):
        self.assertIs(C.VALUES["kubeProxyReplacement"], False)
        self.assertEqual(C.VALUES["operator"]["replicas"], 1)
        self.assertEqual(C.VALUES["k8sServiceHost"], C.CP)
        self.assertEqual(C.VERSION, "1.19.6")

    def test_existing_kubeconfig_identical_only(self):
        cfg = config()
        with patch.object(C, "TARGET", Path("/nonexistent/byoh-test-kubeconfig")):
            self.assertEqual(C.verify_existing(cfg), "new")

    def test_kubeconfig_server_requires_exact_cp(self):
        # Safe-mode tests patch the remote command, never reach SSH.
        import yaml
        broken = config()
        broken["clusters"][0]["cluster"]["server"] = "https://127.0.0.1:6443"
        with patch.object(C, "command") as cmd:
            cmd.return_value.stdout = yaml.safe_dump(broken).encode()
            with self.assertRaises(SystemExit):
                C.remote_config({"ssh": {"user": "ubuntu", "address": C.CP}})

    def test_remote_config_uses_verified_sni(self):
        import yaml
        with patch.object(C, "command") as cmd:
            cmd.return_value.stdout = yaml.safe_dump(config()).encode()
            updated = C.remote_config({"ssh": {"user": "ubuntu", "address": C.CP}})
        self.assertEqual(updated["clusters"][0]["cluster"]["server"],
                         "https://127.0.0.1:16444")
        self.assertEqual(updated["clusters"][0]["cluster"]["tls-server-name"], C.CP)

    def test_helm_never_uses_management_kubeconfig(self):
        script = Path(C.__file__).read_text()
        self.assertIn('"--kubeconfig", str(TARGET)', script)
        self.assertNotIn('"upgrade", "--install"', script)
        self.assertIn('if releases or daemon or operator:', script)
        self.assertIn('"--wait", "--timeout", "5m"', script)

    def test_plan_hash_binds_inventory_and_fresh_credentials(self):
        d1 = C.plan_digest(b"inventory1", config(), C.EXPECTED_CHART_SHA, "new")
        d2 = C.plan_digest(b"inventory2", config(), C.EXPECTED_CHART_SHA, "new")
        cfg = config()
        cfg["users"][0]["user"]["client-key-data"] = "DIFFERENT"
        d3 = C.plan_digest(b"inventory1", cfg, C.EXPECTED_CHART_SHA, "new")
        self.assertNotEqual(d1, d2)
        self.assertNotEqual(d1, d3)


if __name__ == "__main__":
    unittest.main(verbosity=2)
