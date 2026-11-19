#!/usr/bin/env python3
"""Offline checks for guarded worker MD plan rendering and remote safety."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "worker_plan", Path(__file__).with_name("byoh-cluster-worker-plan.py"))
W = importlib.util.module_from_spec(spec)
spec.loader.exec_module(W)


class WorkerPlanTests(unittest.TestCase):
    def test_real_template_renders_four_exact_objects(self):
        rendered = W.render(W.TEMPLATE.read_text())
        import yaml
        objects = list(yaml.safe_load_all(rendered))
        self.assertEqual([x["kind"] for x in objects],
                         ["ByoMachineTemplate", "KubeadmConfigTemplate",
                          "MachineDeployment", "K8sInstallerConfigTemplate"])
        self.assertEqual(objects[0]["spec"]["template"]["spec"]["selector"]["matchLabels"],
                         {"role": "scaleway-worker"})
        self.assertEqual(objects[2]["spec"]["replicas"], 1)
        self.assertEqual(objects[2]["spec"]["template"]["spec"]["version"], "v1.36.6")

    def test_unknown_template_parameter_fails_closed(self):
        with self.assertRaises(SystemExit):
            W.render(W.TEMPLATE.read_text() + "\nmetadata: $" + "{UNEXPECTED_VAR}\n")

    def test_template_selector_drift_fails_closed(self):
        changed = W.TEMPLATE.read_text().replace("role: worker", "role: unexpected")
        with self.assertRaises(SystemExit):
            W.render(changed)

    def test_remote_worker_requires_stopped_disabled_and_no_credentials_leak(self):
        self.assertIn('= inactive', W.REMOTE_WORKER)
        self.assertIn('= disabled', W.REMOTE_WORKER)
        self.assertIn('test ! -e /root/.byoh/bootstrap.kubeconfig', W.REMOTE_WORKER)
        self.assertIn("--skip-installation", W.REMOTE_WORKER)
        self.assertNotIn("systemctl start", W.REMOTE_WORKER)
        self.assertNotIn("journalctl", W.REMOTE_WORKER)

    def test_certificate_identity_uses_worker_and_control_plane(self):
        with patch.object(W, "ssh", side_effect=["a" * 64, "a" * 64]):
            self.assertEqual(W.certificate("ubuntu@worker", "51.158.62.217"), "a" * 64)
            self.assertEqual(W.certificate("ubuntu@cp", "127.0.0.1"), "a" * 64)

    def test_malformed_fingerprint_rejected(self):
        with patch.object(W, "ssh", return_value="not-a-digest"):
            with self.assertRaises(SystemExit):
                W.certificate("ubuntu@worker", "51.158.62.217")

if __name__ == "__main__":
    unittest.main(verbosity=2)
