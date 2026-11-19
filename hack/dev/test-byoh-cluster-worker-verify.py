#!/usr/bin/env python3
"""Offline worker install gate: no cluster or host access."""
import importlib.util
from pathlib import Path
from unittest.mock import patch
import unittest

spec = importlib.util.spec_from_file_location(
    "worker_verify", Path(__file__).with_name("byoh-cluster-worker-verify.py"))
V = importlib.util.module_from_spec(spec)
spec.loader.exec_module(V)


class VerifyTests(unittest.TestCase):
    def test_exact_reference(self):
        self.assertTrue(V.reference({"name": "machine1", "kind": "ByoMachine",
                                     "namespace": "default"}, "machine1", "ByoMachine"))
        self.assertFalse(V.reference({"name": "other", "kind": "ByoMachine"},
                                      "machine1", "ByoMachine"))
        self.assertFalse(V.reference({"name": "machine1", "kind": "ByoHost"},
                                      "machine1", "ByoMachine"))
        self.assertFalse(V.reference({"name": "machine1", "kind": "ByoMachine",
                                      "namespace": "foreign"}, "machine1", "ByoMachine"))

    def test_install_secret_references_allow_omitted_optional_fields(self):
        secret = {"metadata": {"uid": "secret-uid"}}
        ref = {"name": "secret-name", "namespace": "default"}
        self.assertTrue(V.secret_ref(ref, "secret-name", secret))
        self.assertFalse(V.secret_ref(dict(ref, uid="different"), "secret-name", secret))
        self.assertFalse(V.secret_ref(dict(ref, kind="ConfigMap"), "secret-name", secret))
        self.assertFalse(V.secret_ref(dict(ref, apiVersion="v2"), "secret-name", secret))

    def test_reject_missing_machine_deployment(self):
        with patch.object(V.P, "fetch", return_value={
            "metadata": {}, "spec": {"clusterName": V.W.CLUSTER, "replicas": 0}}):
            with self.assertRaises(SystemExit):
                V.verify_objects("fake")

    def test_remote_preinstall_guard_reuses_stopped_unit_check(self):
        with patch.object(V.W, "ssh", return_value="PASS") as ssh:
            V.remote_worker()
            self.assertEqual(ssh.call_args.args[1], V.W.REMOTE_WORKER)
        with patch.object(V.W, "ssh", return_value="FAIL"):
            with self.assertRaises(SystemExit):
                V.remote_worker()

    def test_no_unsafe_data_output_or_automatic_arm(self):
        text = Path(V.__file__).read_text()
        self.assertNotIn("systemctl start", text)
        self.assertNotIn("kubeadm join", text)
        self.assertNotIn("secret[\"data\"]", text)
        self.assertIn("UID verified", text)

if __name__ == "__main__":
    unittest.main(verbosity=2)
