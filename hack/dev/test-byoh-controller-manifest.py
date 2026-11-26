#!/usr/bin/env python3
"""Pure, offline negative tests for the controller manifest substitution gate."""
import importlib.util
import os
import pathlib
import subprocess
import tempfile
import unittest

path = pathlib.Path(__file__).with_name("byoh-controller-manifest.py")
spec = importlib.util.spec_from_file_location("byoh_controller_manifest", path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

IMAGE = "localhost/openkubes/byoh-controller:git-012345abcdef"
BASE = """image: ghcr.io/openkubes/byoh-controller:dev
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


    def test_valid_ghcr_digest(self):
        image = "ghcr.io/openkubes/byoh-controller@sha256:" + "a" * 64
        result = module.render(BASE, image)
        self.assertIn("image: " + image, result)
        self.assertIn('value: "enable"', result)

    def test_ghcr_tag_rejected(self):
        with self.assertRaises(ValueError):
            module.render(
                BASE,
                "ghcr.io/openkubes/byoh-controller:latest",
            )

    def test_ghcr_short_digest_rejected(self):
        with self.assertRaises(ValueError):
            module.render(
                BASE,
                "ghcr.io/openkubes/byoh-controller@sha256:" + "a" * 12,
            )

    def test_ghcr_foreign_repository_rejected(self):
        with self.assertRaises(ValueError):
            module.render(
                BASE,
                "ghcr.io/other/byoh-controller@sha256:" + "a" * 64,
            )

    def test_ghcr_invalid_digest_rejected(self):
        with self.assertRaises(ValueError):
            module.render(
                BASE,
                "ghcr.io/openkubes/byoh-controller@sha256:" + "z" * 64,
            )


class ControllerSourceTest(unittest.TestCase):
    SCRIPT = pathlib.Path(__file__).with_name("byoh-controller.sh")

    def run_controller(self, source, digest, command="render"):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "manifest.yaml"
            env = os.environ.copy()
            env["BYOH_CONTROLLER_SOURCE"] = source
            env["BYOH_CONTROLLER_DIGEST"] = digest
            result = subprocess.run(
                ["bash", str(self.SCRIPT), command, str(output)],
                env=env,
                capture_output=True,
                text=True,
                timeout=10,
                check=False,
            )
            self.assertFalse(output.exists())
            return result

    def test_missing_digest_rejected(self):
        result = self.run_controller("ghcr", "")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("STOP:", result.stderr)

    def test_tag_rejected(self):
        result = self.run_controller("ghcr", "latest")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("STOP:", result.stderr)

    def test_unknown_source_rejected(self):
        result = self.run_controller("unknown", "")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("STOP:", result.stderr)

    def test_ghcr_local_build_rejected(self):
        result = self.run_controller(
            "ghcr", "sha256:" + "a" * 64, command="build"
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("local build is only available", result.stderr)

    def test_ghcr_kind_load_rejected(self):
        result = self.run_controller(
            "ghcr", "sha256:" + "a" * 64, command="load"
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("kind load is only available", result.stderr)

if __name__ == "__main__":
    unittest.main()
