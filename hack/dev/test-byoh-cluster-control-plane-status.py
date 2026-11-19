#!/usr/bin/env python3
"""Offline tests for read-only control-plane status and pre-CNI classification."""
import importlib.util
from pathlib import Path
import unittest
spec = importlib.util.spec_from_file_location("cp_status", Path(__file__).with_name("byoh-cluster-control-plane-status.py"))
S = importlib.util.module_from_spec(spec)
spec.loader.exec_module(S)

def fixture():
    return {
        "readyz": "ok",
        "packages": {k: "1.36.6-1.1" for k in ("kubeadm", "kubectl", "kubelet")},
        "nodes": [{"name": "ok-bm-lab-01", "version": "v1.36.6", "ready": "False"}],
        "pods": [{"name": n + "-ok-bm-lab-01", "phase": "Running", "ready": True}
                 for n in ("etcd", "kube-apiserver", "kube-controller-manager", "kube-scheduler")]
                + [{"name": "coredns-abc", "phase": "Pending", "ready": False}]}

class StatusTests(unittest.TestCase):
    def test_pre_cni_expected(self):
        self.assertEqual(S.assess(fixture()), (False, False))

    def test_ready_cluster(self):
        d = fixture()
        d["nodes"][0]["ready"] = "True"
        d["pods"][-1] = {"name": "coredns-abc", "phase": "Running", "ready": True}
        self.assertEqual(S.assess(d), (True, True))

    def test_wrong_node_rejected(self):
        d = fixture()
        d["nodes"][0]["name"] = "foreign-host"
        with self.assertRaises(SystemExit):
            S.assess(d)

    def test_failed_apiserver_pod_rejected(self):
        d = fixture()
        d["pods"][1]["ready"] = False
        with self.assertRaises(SystemExit):
            S.assess(d)

    def test_never_reads_journal_or_emits_tokens(self):
        self.assertNotIn("journalctl", S.REMOTE)
        self.assertNotIn("join --token", S.REMOTE)
        self.assertIn("--raw=/readyz", S.REMOTE)

if __name__ == "__main__":
    unittest.main(verbosity=2)
