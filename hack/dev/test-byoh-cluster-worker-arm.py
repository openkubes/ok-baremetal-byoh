#!/usr/bin/env python3
"""Offline worker arm safety tests: no real SSH or cluster writes."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "worker_arm", Path(__file__).with_name("byoh-cluster-worker-arm.py"))
A = importlib.util.module_from_spec(spec)
spec.loader.exec_module(A)


class WorkerArmTests(unittest.TestCase):
    def test_distinct_worker_override_and_exclusive_create(self):
        self.assertIn("95-ok-byoh-worker-install.conf", A.REMOTE_ARM)
        self.assertNotIn("95-ok-byoh-cp-install.conf", A.REMOTE_ARM)
        self.assertIn('ln "$tmp" "$path"', A.REMOTE_ARM)
        self.assertIn('systemctl daemon-reload', A.REMOTE_ARM)
        self.assertIn('systemctl start "$unit"', A.REMOTE_ARM)
        self.assertIn("--skip-installation", A.REMOTE_PREFLIGHT)
        self.assertIn('sha256sum', A.REMOTE_PREFLIGHT)

    def test_only_expected_target_host(self):
        self.assertEqual(A.PEER, "ubuntu@51.158.62.216")
        self.assertEqual(A.HOST, "ok-bm-lab-02")

    def test_plan_only_has_no_automatic_arm(self):
        text = Path(A.__file__).read_text()
        self.assertIn('if not args.install:', text)
        self.assertIn('if answer != phrase:', text)
        self.assertIn('raw2, unit_hash2, proof2, digest2 = compute(path)', text)
        self.assertIn('unit_hash + "\\n" + REMOTE_ARM', text)
        self.assertNotIn('os.system(', text)

    def test_ssh_nonzero_fails_closed_and_never_prints_remote_stderr(self):
        from subprocess import CompletedProcess
        with patch.object(A.subprocess, "run",
                          return_value=CompletedProcess([], 21, b"secret", b"private-token")):
            with self.assertRaises(SystemExit) as raised:
                A.ssh(A.PEER, "exit 21")
        self.assertNotIn("private-token", str(raised.exception))
        self.assertNotIn("secret", str(raised.exception))

if __name__ == "__main__":
    unittest.main(verbosity=2)
