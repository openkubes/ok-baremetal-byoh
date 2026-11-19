#!/usr/bin/env python3
"""Offline regression tests for the independent BYOH CSR authorization evidence."""
import base64
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest
import yaml

script = Path(__file__).with_name("byoh-hosts-csr-verify.py")
spec = importlib.util.spec_from_file_location("byoh_csr_verify", script)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

NOW = datetime(2026, 11, 19, 21, 10, tzinfo=timezone.utc)
HOST = {"name": "ok-bm-lab-01"}
UID = "00000000-1111-2222-3333-444444444444"
ID = hashlib.sha256(UID.encode()).hexdigest()[:6]
SECRET = "a" * 16
TOKEN = ID + "." + SECRET


def enc(value):
    return base64.b64encode(value.encode()).decode()


def fixture():
    return (
        {"metadata": {"name": "byoh-csr-default.ok-bm-lab-01",
                      "creationTimestamp": (NOW-timedelta(minutes=2)).isoformat()},
         "spec": {"username": "system:bootstrap:" + ID,
                  "signerName": "kubernetes.io/kube-apiserver-client",
                  "groups": ["system:bootstrappers", "system:bootstrappers:byoh",
                             "system:authenticated"],
                  "usages": ["client auth"]}, "status": {}},
        {"metadata": {"name": HOST["name"], "uid": UID},
         "status": {"bootstrapKubeconfigData": yaml.safe_dump({
             "users": [{"name": "bootstrap", "user": {"token": TOKEN}}]})}},
        {"metadata": {"name": "bootstrap-token-" + ID, "namespace": "kube-system",
                      "creationTimestamp": (NOW-timedelta(minutes=5)).isoformat(),
                      "annotations": {"byoh.openkubes.io/bootstrap-request-uid": UID}},
         "type": "bootstrap.kubernetes.io/token",
         "data": {"token-id": enc(ID), "token-secret": enc(SECRET),
                  "expiration": enc((NOW+timedelta(minutes=20)).isoformat())}}
    )


class CSRIdentityTests(unittest.TestCase):
    def verify(self, csr, bootstrap, secret):
        return module.evaluate_identity(host=HOST, csr=csr, bootstrap=bootstrap,
                                        secret=secret, now=NOW)

    def test_valid_owned_request(self):
        csr, bootstrap, secret = fixture()
        self.assertEqual(self.verify(csr, bootstrap, secret)[0], ID)

    def test_unowned_token_rejected(self):
        csr, bootstrap, secret = fixture()
        secret["metadata"]["annotations"]["byoh.openkubes.io/bootstrap-request-uid"] = "foreign"
        with self.assertRaises(SystemExit):
            self.verify(csr, bootstrap, secret)

    def test_wrong_requester_rejected(self):
        csr, bootstrap, secret = fixture()
        csr["spec"]["username"] = "system:bootstrap:badbad"
        with self.assertRaises(SystemExit):
            self.verify(csr, bootstrap, secret)

    def test_wrong_token_value_rejected(self):
        csr, bootstrap, secret = fixture()
        bootstrap["status"]["bootstrapKubeconfigData"] = yaml.safe_dump({
            "users": [{"user": {"token": ID + "." + "b" * 16}}]})
        with self.assertRaises(SystemExit):
            self.verify(csr, bootstrap, secret)

    def test_expired_token_rejected(self):
        csr, bootstrap, secret = fixture()
        secret["data"]["expiration"] = enc((NOW-timedelta(seconds=1)).isoformat())
        with self.assertRaises(SystemExit):
            self.verify(csr, bootstrap, secret)

    def test_wrong_signer_rejected(self):
        csr, bootstrap, secret = fixture()
        csr["spec"]["signerName"] = "kubernetes.io/kube-apiserver-client-kubelet"
        with self.assertRaises(SystemExit):
            self.verify(csr, bootstrap, secret)

    def test_extra_requester_group_rejected(self):
        csr, bootstrap, secret = fixture()
        csr["spec"]["groups"].append("system:masters")
        with self.assertRaises(SystemExit):
            self.verify(csr, bootstrap, secret)

    def test_already_approved_rejected(self):
        csr, bootstrap, secret = fixture()
        csr["status"]["conditions"] = [{"type": "Approved"}]
        with self.assertRaises(SystemExit):
            self.verify(csr, bootstrap, secret)


if __name__ == "__main__":
    unittest.main(verbosity=2)
