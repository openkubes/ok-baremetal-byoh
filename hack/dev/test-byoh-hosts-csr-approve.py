#!/usr/bin/env python3
"""Offline tests: CSR approval payload and proof of possession (no cluster writes)."""
import base64
import copy
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

file = Path(__file__).with_name("byoh-hosts-csr-approve.py")
spec = importlib.util.spec_from_file_location("byoh_csr_approval", file)
approval = importlib.util.module_from_spec(spec)
spec.loader.exec_module(approval)


class ApprovalTests(unittest.TestCase):
    def pending(self):
        return {
            "apiVersion": "certificates.k8s.io/v1",
            "kind": "CertificateSigningRequest",
            "metadata": {"name": "byoh-csr-default.ok-bm-lab-01",
                         "uid": "csr-uid-123", "resourceVersion": "908"},
            "spec": {"request": "dGVzdA=="},
            "status": {},
        }

    def test_approval_payload_retains_preconditions(self):
        obj = self.pending()
        original = copy.deepcopy(obj)
        encoded = approval.approval_payload(obj)
        import json
        written = json.loads(encoded)
        self.assertEqual(obj, original)
        self.assertEqual(written["metadata"]["uid"], "csr-uid-123")
        self.assertEqual(written["metadata"]["resourceVersion"], "908")
        self.assertEqual(written["status"]["conditions"][0]["type"], "Approved")
        self.assertEqual(written["status"]["conditions"][0]["status"], "True")

    def test_approved_or_issued_denied(self):
        for state in ({"conditions": [{"type": "Approved", "status": "True"}]},
                      {"certificate": "dGVzdA=="}):
            obj = self.pending()
            obj["status"] = state
            with self.subTest(state=state):
                with self.assertRaises(SystemExit):
                    approval.approval_payload(obj)

    def test_proof_matches_own_public_key(self):
        # Ephemeral local test keys never leave the test temporary directory.
        with tempfile.TemporaryDirectory(prefix="ok-byoh-proof-test-") as dirname:
            private = Path(dirname) / "key.pem"
            public = Path(dirname) / "public.pem"
            challenge = b"OpenKubes-test-nonce"
            subprocess.run(["openssl", "genpkey", "-algorithm", "RSA",
                            "-pkeyopt", "rsa_keygen_bits:2048",
                            "-out", str(private)], check=True, capture_output=True)
            subprocess.run(["openssl", "pkey", "-in", str(private),
                            "-pubout", "-out", str(public)], check=True, capture_output=True)
            csr = Path(dirname) / "request.pem"
            subprocess.run(["openssl", "req", "-new", "-key", str(private),
                            "-subj", "/CN=byoh:host:default:ok-bm-lab-01/O=byoh:hosts",
                            "-out", str(csr)], check=True, capture_output=True)
            signed = subprocess.run(["openssl", "dgst", "-sha256",
                                     "-sign", str(private)],
                                    input=challenge, capture_output=True, check=True).stdout
            approval.challenge_matches_public_key(base64.b64encode(csr.read_bytes()).decode(),
                                                   challenge, signed)
            with self.assertRaises(SystemExit):
                approval.challenge_matches_public_key(base64.b64encode(csr.read_bytes()).decode(),
                                                       b"different-challenge", signed)


if __name__ == "__main__":
    unittest.main(verbosity=2)
