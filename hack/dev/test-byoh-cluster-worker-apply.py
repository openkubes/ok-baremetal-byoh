#!/usr/bin/env python3
"""Offline worker apply guards; no cluster/SSH writes are performed."""
import importlib.util
from pathlib import Path
from unittest.mock import patch
import unittest

spec = importlib.util.spec_from_file_location(
    "worker_apply", Path(__file__).with_name("byoh-cluster-worker-apply.py"))
A = importlib.util.module_from_spec(spec)
spec.loader.exec_module(A)

PROOF = {"worker": "ok-bm-lab-02", "worker_uid": "host-uid",
         "worker_rv": "104"}

def host(role=None, uid="host-uid", rv="104", bound=None):
    labels = {} if role is None else {"role": role}
    return {"metadata": {"uid": uid, "resourceVersion": rv, "labels": labels},
            "spec": {"byoMachineRef": bound}}

class ApplyTests(unittest.TestCase):
    def test_unbound_unlabelled_host_allowed(self):
        with patch.object(A.W.P, "fetch", return_value=host()):
            self.assertIsNone(A.guard_host("test-kc", PROOF))

    def test_existing_correct_role_noop(self):
        with patch.object(A.W.P, "fetch", return_value=host("scaleway-worker")):
            with patch.object(A.W, "ssh", return_value="PASS") as ssh:
                with patch.object(A.W.P, "run") as run:
                    A.apply("test-kc", "---\nkind: MachineDeployment\n", PROOF)
        self.assertEqual(run.call_count, 1)
        self.assertIn("apply", run.call_args.args[0])
        self.assertNotIn("label", run.call_args.args[0])
        ssh.assert_called_once()

    def test_first_label_and_apply(self):
        with patch.object(A.W.P, "fetch", return_value=host()):
            with patch.object(A.W, "ssh", return_value="PASS"):
                with patch.object(A.W.P, "run") as run:
                    A.apply("test-kc", "---\nkind: MachineDeployment\n", PROOF)
        self.assertEqual(run.call_count, 2)
        self.assertIn("label", run.call_args_list[0].args[0])
        self.assertIn("apply", run.call_args_list[1].args[0])

    def test_bound_worker_rejected(self):
        with patch.object(A.W.P, "fetch", return_value=host(bound={"name": "foreign"})):
            with self.assertRaises(SystemExit):
                A.guard_host("test-kc", PROOF)

    def test_uid_or_resource_version_change_rejected(self):
        for modified in (host(uid="foreign"), host(rv="105")):
            with patch.object(A.W.P, "fetch", return_value=modified):
                with self.assertRaises(SystemExit):
                    A.guard_host("test-kc", PROOF)

    def test_conflicting_role_rejected(self):
        with patch.object(A.W.P, "fetch", return_value=host("control-plane")):
            with self.assertRaises(SystemExit):
                A.guard_host("test-kc", PROOF)

    def test_agent_never_started_by_apply(self):
        script = Path(A.__file__).read_text()
        self.assertNotIn("systemctl start", script)
        self.assertNotIn("kubeadm join", script)
        self.assertIn("if answer != phrase:", script)
        self.assertIn("W.compute(path)", script)

if __name__ == "__main__":
    unittest.main(verbosity=2)
