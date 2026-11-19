#!/usr/bin/env python3
"""Offline regression checks for lab CP authorization and isolated network plan."""
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]


def load(path, label):
    spec = importlib.util.spec_from_file_location(label, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


CP = load("hack/dev/byoh-cluster-control-plane-apply.py", "cp_apply_tests")
NET = load("hack/dev/byoh-cluster-network.py", "cp_net_tests")


class CPAndNetworkTests(unittest.TestCase):
    def test_network_hash_includes_inventory_and_rules(self):
        host = {"name": "ok-bm-lab-01", "ssh": {
            "user": "ubuntu", "address": "51.158.62.217"}}
        rules = {"PREROUTING": False, "OUTPUT": False}
        first = NET.plan_hash(b"inventory-1", host, rules)
        self.assertEqual(first, NET.plan_hash(b"inventory-1", host, rules))
        self.assertNotEqual(first, NET.plan_hash(b"inventory-2", host, rules))
        self.assertNotEqual(first, NET.plan_hash(
            b"inventory-1", host, {"PREROUTING": True, "OUTPUT": False}))
        self.assertEqual(len(first), 64)

    def test_apply_plan_does_not_implicitly_start_agent(self):
        # Guardrail: apply only invokes kubectl, never SSH/systemd/kubeadm.
        src = (ROOT / "hack/dev/byoh-cluster-control-plane-apply.py").read_text()
        self.assertNotIn("systemctl start", src)
        self.assertNotIn("kubeadm init", src)
        self.assertIn("dry-run=server", src)
        self.assertIn("role=control-plane", src)

    def test_nat_is_scoped_to_test_container_only(self):
        self.assertEqual(NET.CONTAINER, "ok-byoh-make-test-control-plane")
        self.assertEqual(NET.DOCKER_CONTEXT, "colima-ok-cli-kvm")
        self.assertEqual(NET.CP_ADDR, "51.158.62.217")
        self.assertEqual(NET.GUEST_IP, "192.168.5.2")


if __name__ == "__main__":
    unittest.main(verbosity=2)
