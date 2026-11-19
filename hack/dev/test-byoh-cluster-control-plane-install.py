#!/usr/bin/env python3
"""Offline fail-closed CP installation readiness tests."""
from pathlib import Path
import importlib.util
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "cp_install", Path(__file__).with_name("byoh-cluster-control-plane-install.py"))
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

MACHINE = "ok-byoh-scw1-control-plane-2ztzj"
UID = "00000000-0000-4000-8000-000000000001"
SECRET_UID = "00000000-0000-4000-8000-000000000002"


def fixtures():
    host = {"metadata": {"uid": "host-uid", "resourceVersion": "23"},
            "spec": {"byoMachineRef": {"name": MACHINE, "uid": UID},
                     "installationSecret": {"name": "installer-secret", "kind": "Secret", "namespace": "default"},
                     "bootstrapSecret": {"name": MACHINE, "kind": "Secret"}}}
    byom = {"metadata": {"uid": UID}}
    installer = {"status": {"ready": True, "installationSecret": {
        "name": "installer-secret", "namespace": "default", "kind": "Secret"}}}
    secret = {"metadata": {"uid": SECRET_UID}, "data": {"payload": "masked"}}
    return {("byohost", "ok-bm-lab-01"): host, ("byomachine", MACHINE): byom,
            ("k8sinstallerconfig", MACHINE): installer, ("secret", "installer-secret"): secret}


class CPInstallTests(unittest.TestCase):
    def check(self, data):
        with patch.object(mod.P, "fetch", side_effect=lambda _kc, kind, name, _ns: data[(kind, name)]):
            return mod.ready_installation("kubeconfig", MACHINE)

    def test_matching_ready_installation(self):
        self.assertEqual(self.check(fixtures())["machine_uid"], UID)

    def test_omitted_optional_secret_reference_fields_accepted(self):
        data = fixtures()
        data[("byohost", "ok-bm-lab-01")]["spec"]["installationSecret"] = {
            "name": "installer-secret", "namespace": "default"}
        data[("k8sinstallerconfig", MACHINE)]["status"]["installationSecret"] = {
            "name": "installer-secret", "namespace": "default"}
        data[("byohost", "ok-bm-lab-01")]["spec"]["bootstrapSecret"] = {
            "name": MACHINE}
        self.assertEqual(self.check(data)["installation_secret_uid"], SECRET_UID)

    def test_explicit_wrong_secret_kind_rejected(self):
        data = fixtures()
        data[("byohost", "ok-bm-lab-01")]["spec"]["installationSecret"]["kind"] = "ConfigMap"
        with self.assertRaises(SystemExit):
            self.check(data)

    def test_explicit_wrong_secret_api_version_rejected(self):
        data = fixtures()
        data[("byohost", "ok-bm-lab-01")]["spec"]["installationSecret"]["apiVersion"] = "other/v1"
        with self.assertRaises(SystemExit):
            self.check(data)

    def test_wrong_installation_secret_uid_rejected(self):
        data = fixtures()
        data[("byohost", "ok-bm-lab-01")]["spec"]["installationSecret"]["uid"] = "foreign-uid"
        with self.assertRaises(SystemExit):
            self.check(data)

    def test_wrong_bootstrap_secret_kind_rejected(self):
        data = fixtures()
        data[("byohost", "ok-bm-lab-01")]["spec"]["bootstrapSecret"]["kind"] = "ConfigMap"
        with self.assertRaises(SystemExit):
            self.check(data)

    def test_mismatched_machine_uid_rejected(self):
        data = fixtures()
        data[("byohost", "ok-bm-lab-01")]["spec"]["byoMachineRef"]["uid"] = "other"
        with self.assertRaises(SystemExit):
            self.check(data)

    def test_installer_not_ready_rejected(self):
        data = fixtures()
        data[("k8sinstallerconfig", MACHINE)]["status"]["ready"] = False
        with self.assertRaises(SystemExit):
            self.check(data)

    def test_mismatched_installation_secret_rejected(self):
        data = fixtures()
        data[("byohost", "ok-bm-lab-01")]["spec"]["installationSecret"]["name"] = "foreign"
        with self.assertRaises(SystemExit):
            self.check(data)

    def test_missing_installer_secret_rejected(self):
        data = fixtures()
        data[("secret", "installer-secret")]["data"] = {}
        with self.assertRaises(SystemExit):
            self.check(data)

    def test_enrollment_dropin_comparison_uses_real_newlines(self):
        expected_segment = mod.REMOTE_PREFLIGHT.split(
            'expected="$(printf', 1)[1].split('actual="$(cat', 1)[0]
        self.assertIn("[Service]\nWorkingDirectory=/root\nExecStart=", expected_segment)
        self.assertNotIn("[Service]\\\\nWorkingDirectory", expected_segment)
        self.assertIn('test "$actual" = "$expected"', mod.REMOTE_PREFLIGHT)

    def test_remote_action_never_enables_agent(self):
        self.assertNotIn("systemctl enable ", mod.REMOTE_INSTALL)
        self.assertIn("systemctl start", mod.REMOTE_INSTALL)
        self.assertIn("test ! -e /root/.byoh/bootstrap.kubeconfig", mod.REMOTE_INSTALL)
        self.assertIn("95-ok-byoh-cp-install.conf", mod.DROPIN)
        self.assertIn("skip-installation", mod.REMOTE_PREFLIGHT)
        self.assertNotIn("kubeadm init", mod.REMOTE_INSTALL)


if __name__ == "__main__":
    unittest.main(verbosity=2)
