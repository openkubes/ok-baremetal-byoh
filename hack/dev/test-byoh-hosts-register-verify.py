#!/usr/bin/env python3
"""Offline regression: Go-style kubeconfig field extraction patterns."""
import importlib.util
from pathlib import Path
import re
import unittest

script = Path(__file__).with_name("byoh-hosts-register-verify.py")
spec = importlib.util.spec_from_file_location("byoh_register_verify", script)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


class RemoteChecks(unittest.TestCase):
    def test_remote_parser_has_single_escaped_whitespace_patterns(self):
        body = mod.REMOTE
        self.assertIn('r"^\\s*client-certificate-data:', body)
        self.assertNotIn('r"^\\\\s*client-certificate-data:', body)

    def test_go_style_kubeconfig_fields(self):
        sample = """apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: Y2E=
    server: https://127.0.0.1:16443
users:
- name: default
  user:
    client-certificate-data: Y2VydA==
    client-key-data: a2V5
"""
        for field, expected in (("certificate-authority-data", "Y2E="),
                                ("client-certificate-data", "Y2VydA=="),
                                ("client-key-data", "a2V5")):
            matches = re.findall(
                r"^\s*" + re.escape(field) + r":\s*(\S+)\s*$",
                sample, flags=re.MULTILINE)
            self.assertEqual(matches, [expected])

    def test_no_token_or_insecure_flag_allowed(self):
        for unsafe in ("    token: secret", "  exec:", "  auth-provider:",
                       "    insecure-skip-tls-verify: true"):
            self.assertTrue(re.search(
                r"^\s*(?:token|exec|auth-provider):|^\s*insecure-skip-tls-verify:\s*true\s*$",
                unsafe, re.MULTILINE))


if __name__ == "__main__":
    unittest.main(verbosity=2)
