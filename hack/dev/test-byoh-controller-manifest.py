#!/usr/bin/env python3
"""Pure, offline negative tests for the controller manifest substitution gate."""
import importlib.util
import pathlib
import unittest

path = pathlib.Path(__file__).with_name("byoh-controller-manifest.py")
spec = importlib.util.spec_from_file_location("byoh_controller_manifest", path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

IMAGE = "localhost/openkubes/byoh-controller:git-012345abcdef"
BASE = """image: ghcr.io/superbrothers/cluster-api-byoh-controller:dev
- name: MANUAL_CSR_APPROVAL
  value: "${MANUAL_CSR_APPROVAL:=disable}"
- --diagnostics-address=${CAPI_DIAGNOSTICS_ADDRESS:=:8443}
- --insecure-diagnostics=${CAPI_INSECURE_DIAGNOSTICS:=false}
- --requeue-period=${BYOH_REQUEUE_PERIOD:=10s}
- --byomachine-concurrency=${BYOH_BYOMACHINE_CONCURRENCY:=1}
"""

class ManifestGateTest(unittest.TestCase):
    def test_safe_render(self):
        result = module.render(BASE, IMAGE)
        self.assertIn("image: " + IMAGE, result)
        self.assertIn('value: "enable"', result)
        self.assertNotIn("${", result)

    def test_wrong_image(self):
        with self.assertRaises(ValueError):
            module.render(BASE, "ghcr.io/superbrothers/cluster-api-byoh-controller:latest")

    def test_auto_approval(self):
        with self.assertRaises(ValueError):
            module.render(BASE.replace("${MANUAL_CSR_APPROVAL:=disable}", "disable"), IMAGE)

    def test_extra_upstream_image(self):
        with self.assertRaises(ValueError):
            module.render(BASE + "\nimage: ghcr.io/superbrothers/cluster-api-byoh-controller:latest", IMAGE)

    def test_repeated_template_field(self):
        with self.assertRaises(ValueError):
            module.render(BASE + "\n" + BASE, IMAGE)

if __name__ == "__main__":
    unittest.main()
