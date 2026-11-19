#!/usr/bin/env python3
"""Offline worker post-install status regressions, no live API calls."""
import importlib.util
from pathlib import Path
from unittest.mock import patch
import unittest

spec = importlib.util.spec_from_file_location(
    "worker_status", Path(__file__).with_name("byoh-cluster-worker-status.py"))
S = importlib.util.module_from_spec(spec)
spec.loader.exec_module(S)

class WorkerStatusTests(unittest.TestCase):
    def test_ready_condition_true(self):
        self.assertTrue(S.is_ready({"status": {"conditions": [
            {"type": "Ready", "status": "True"}]}}))
        self.assertFalse(S.is_ready({"status": {"conditions": [
            {"type": "Ready", "status": "False"}]}}))

    def test_worker_md_replicas_require_all_ready(self):
        md = {"metadata": {}, "spec": {"clusterName": S.W.CLUSTER, "replicas": 1,
              "template": {"spec": {"version": S.W.VERSION}}},
              "status": {"replicas": 1, "readyReplicas": 0,
                         "availableReplicas": 1, "upToDateReplicas": 1}}
        with patch.object(S.P, "fetch", return_value=md):
            with self.assertRaises(SystemExit):
                S.check_capi("mock")

    def test_workload_uses_verified_current_credentials(self):
        cfg = {"clusters": [], "users": []}
        with patch.object(S.C, "remote_config", return_value=cfg) as rem:
            with patch.object(S.C, "verify_existing", return_value="missing"):
                with self.assertRaises(SystemExit):
                    S.check_workload("mock")
        rem.assert_called_once()

    def test_status_has_no_write_or_credential_dump(self):
        text = Path(S.__file__).read_text()
        self.assertNotIn("systemctl start", text)
        self.assertNotIn("kubeadm join", text)
        self.assertNotIn("kubectl apply", text)
        self.assertNotIn("secret", text.lower().split("def main():")[1])
        self.assertIn("cross-node", text)

if __name__ == "__main__":
    unittest.main(verbosity=2)
