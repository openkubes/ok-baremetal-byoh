#!/usr/bin/env python3
"""Offline tests for the deterministic BYOH CAPI CP manifest renderer."""
import importlib.util
from pathlib import Path
import unittest
import yaml

path = Path(__file__).with_name("byoh-cluster-control-plane-plan.py")
spec = importlib.util.spec_from_file_location("byoh_cp_plan_test", path)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


class ControlPlanePlanTests(unittest.TestCase):
    def test_expected_original_capi_kcp_components(self):
        rendered = list(yaml.safe_load_all(mod.render("51.158.62.217")))
        self.assertEqual([d["kind"] for d in rendered],
                         ["Cluster", "ByoCluster", "ByoMachineTemplate",
                          "KubeadmControlPlane", "K8sInstallerConfigTemplate"])
        self.assertEqual(rendered[1]["spec"]["controlPlaneEndpoint"],
                         {"host": "51.158.62.217", "port": 6443})
        self.assertEqual(rendered[3]["spec"]["version"], "v1.36.6")
        self.assertEqual(rendered[3]["spec"]["replicas"], 1)
        self.assertEqual(rendered[0]["metadata"]["name"], "ok-byoh-scw1")

    def test_no_unresolved_template_parameters(self):
        rendered = mod.render("51.158.62.217")
        self.assertNotIn("${", rendered)


if __name__ == "__main__":
    unittest.main(verbosity=2)
