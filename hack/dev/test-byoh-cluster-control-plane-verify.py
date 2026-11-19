#!/usr/bin/env python3
"""Offline CAPI control-plane binding preflight regressions (no Kubernetes access)."""
import importlib.util
from pathlib import Path
from unittest import TestCase, main
from unittest.mock import patch

script = Path(__file__).with_name("byoh-cluster-control-plane-verify.py")
spec = importlib.util.spec_from_file_location("cp_binding", script)
V = importlib.util.module_from_spec(spec)
spec.loader.exec_module(V)
NAME = "ok-byoh-scw1-control-plane-2ztzj"


def fixtures():
    values = {
        ("cluster", "ok-byoh-scw1"): {
            "metadata": {}, "spec": {"controlPlaneRef": {"name": "ok-byoh-scw1-control-plane"},
            "infrastructureRef": {"name": "ok-byoh-scw1"}}},
        ("kubeadmcontrolplane", "ok-byoh-scw1-control-plane"): {
            "apiVersion": "controlplane.cluster.x-k8s.io/v1beta2",
            "metadata": {}, "spec": {"version": "v1.36.6", "replicas": 1,
            "machineTemplate": {"spec": {"infrastructureRef": {"name": "ok-byoh-scw1-control-plane", "kind": "ByoMachineTemplate"}}}}},
        ("byomachine", NAME): {"metadata": {}, "spec": {}},
        ("byohost", "ok-bm-lab-01"): {"metadata": {"labels": {"role": "control-plane"}},
            "spec": {"byoMachineRef": {"name": NAME}}},
        ("kubeadmconfig", NAME): {"metadata": {}, "status": {"dataSecretName": NAME}},
        ("secret", NAME): {"metadata": {}, "data": {"value": "omitted"}},
        ("k8sinstallerconfig", NAME): {"metadata": {}, "spec": {}},
    }
    machine = {"metadata": {"name": NAME}, "spec": {
        "clusterName": "ok-byoh-scw1", "version": "v1.36.6",
        "infrastructureRef": {"name": NAME, "kind": "ByoMachine"},
        "bootstrap": {"configRef": {"name": NAME, "kind": "KubeadmConfig"}}}}
    return values, {"items": [machine]}


class BindingTests(TestCase):
    def call_verify(self, data, machines):
        import json
        def fetch(_kc, kind, name, _namespace):
            return data[(kind, name)]
        with patch.object(V.P, "fetch", side_effect=fetch), patch.object(
                V.P, "run", return_value=json.dumps(machines)):
            return V.verify_objects("dummy-kubeconfig")

    def test_valid_binding(self):
        data, machines = fixtures()
        self.assertEqual(self.call_verify(data, machines), (NAME, NAME))

    def test_v1beta1_legacy_reference_path(self):
        data, machines = fixtures()
        cp = data[("kubeadmcontrolplane", "ok-byoh-scw1-control-plane")]
        cp["apiVersion"] = "controlplane.cluster.x-k8s.io/v1beta1"
        cp["spec"]["machineTemplate"] = cp["spec"]["machineTemplate"]["spec"]
        self.assertEqual(self.call_verify(data, machines), (NAME, NAME))

    def test_v1beta2_legacy_path_rejected(self):
        data, machines = fixtures()
        cp = data[("kubeadmcontrolplane", "ok-byoh-scw1-control-plane")]
        cp["spec"]["machineTemplate"] = cp["spec"]["machineTemplate"]["spec"]
        with self.assertRaises(SystemExit):
            self.call_verify(data, machines)

    def test_wrong_kcp_template_reference_rejected(self):
        data, machines = fixtures()
        data[("kubeadmcontrolplane", "ok-byoh-scw1-control-plane")]["spec"]["machineTemplate"]["spec"]["infrastructureRef"]["name"] = "unexpected-template"
        with self.assertRaises(SystemExit):
            self.call_verify(data, machines)

    def test_missing_kcp_template_reference_rejected(self):
        data, machines = fixtures()
        del data[("kubeadmcontrolplane", "ok-byoh-scw1-control-plane")]["spec"]["machineTemplate"]["spec"]["infrastructureRef"]
        with self.assertRaises(SystemExit):
            self.call_verify(data, machines)

    def test_unbound_host_rejected(self):
        data, machines = fixtures()
        data[("byohost", "ok-bm-lab-01")]["spec"]["byoMachineRef"] = None
        with self.assertRaises(SystemExit):
            self.call_verify(data, machines)

    def test_foreign_machine_rejected(self):
        data, machines = fixtures()
        machines["items"][0]["spec"]["clusterName"] = "foreign"
        with self.assertRaises(SystemExit):
            self.call_verify(data, machines)

    def test_missing_bootstrap_secret_rejected(self):
        data, machines = fixtures()
        data[("kubeadmconfig", NAME)]["status"]["dataSecretName"] = None
        with self.assertRaises(SystemExit):
            self.call_verify(data, machines)


if __name__ == "__main__":
    main(verbosity=2)
