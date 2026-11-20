#!/usr/bin/env python3
"""Offline fail-closed regression tests for BYOH Kubernetes upgrade plan."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("upgrade_plan", HERE / "byoh-cluster-upgrade-plan.py")
upgrade = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)

def healthy_cluster():
    return {"metadata": {"uid": "cluster-uid"}, "spec": {},
            "status": {"conditions": [{"type": "Available", "status": "True"}]}}

def healthy_kcp():
    return {"metadata": {"uid": "kcp-uid"}, "apiVersion": "controlplane.cluster.x-k8s.io/v1beta2",
            "spec": {"version": "v1.36.6", "replicas": 1},
            "status": {"initialization": {"controlPlaneInitialized": True},
                       "replicas": 1, "readyReplicas": 1,
                       "availableReplicas": 1, "upToDateReplicas": 1}}

class UpgradePlanOfflineTests(unittest.TestCase):
    def test_healthy_baseline_is_observable(self):
        upgrade.assess_cluster(healthy_cluster(), healthy_kcp())

    def test_rejects_explicit_pause_true_and_false(self):
        for paused in (True, False):
            with self.subTest(paused=paused):
                cluster = healthy_cluster()
                cluster["spec"]["paused"] = paused
                with self.assertRaises(SystemExit):
                    upgrade.assess_cluster(cluster, healthy_kcp())

    def test_rejects_pause_annotation(self):
        cluster = healthy_cluster()
        cluster["metadata"]["annotations"] = {"cluster.x-k8s.io/paused": ""}
        with self.assertRaises(SystemExit):
            upgrade.assess_cluster(cluster, healthy_kcp())

    def test_rejects_version_drift(self):
        kcp = healthy_kcp()
        kcp["spec"]["version"] = "v1.37.2"
        with self.assertRaises(SystemExit):
            upgrade.assess_cluster(healthy_cluster(), kcp)

    def test_rejects_unhealthy_kcp(self):
        kcp = healthy_kcp()
        kcp["status"]["availableReplicas"] = 0
        with self.assertRaises(SystemExit):
            upgrade.assess_cluster(healthy_cluster(), kcp)

    def test_rejects_uninitialized_kcp(self):
        kcp = healthy_kcp()
        kcp["status"]["initialization"]["controlPlaneInitialized"] = False
        with self.assertRaises(SystemExit):
            upgrade.assess_cluster(healthy_cluster(), kcp)

    def test_rejects_missing_cluster_availability(self):
        cluster = healthy_cluster()
        cluster["status"]["conditions"] = []
        with self.assertRaises(SystemExit):
            upgrade.assess_cluster(cluster, healthy_kcp())

    @patch.object(upgrade.subprocess, "run")
    def test_remote_package_check_fails_without_ssh(self, run):
        run.side_effect = OSError("unavailable")
        with self.assertRaises(SystemExit):
            upgrade.remote_packages("ok-bm-lab-01")

    @patch.object(upgrade.subprocess, "run")
    def test_remote_package_check_rejects_mixed_versions(self, run):
        import json
        run.return_value.returncode = 0
        run.return_value.stdout = json.dumps({
            "installed": {"kubeadm": "1.36.6-1.1", "kubelet": "1.37.2-1.1",
                          "kubectl": "1.36.6-1.1"},
            "target_available": {"kubeadm": True, "kubelet": True, "kubectl": True},
            "repositories": {name: ["v1.36"] for name in ("kubeadm", "kubelet", "kubectl")}
        }).encode()
        with self.assertRaises(SystemExit):
            upgrade.remote_packages("ok-bm-lab-02")

    @patch.object(upgrade.subprocess, "run")
    def test_repository_v136_requires_expected_change(self, run):
        import json
        run.return_value.returncode = 0
        run.return_value.stdout = json.dumps({
            "installed": {name: "1.36.6-1.1" for name in ("kubeadm", "kubelet", "kubectl")},
            "target_available": {name: False for name in ("kubeadm", "kubelet", "kubectl")},
            "repositories": {name: ["v1.36"] for name in ("kubeadm", "kubelet", "kubectl")}
        }).encode()
        result = upgrade.remote_packages("ok-bm-lab-01")
        self.assertEqual(upgrade.package_status(result)[0], "EXPECTED_CHANGE")

    @patch.object(upgrade.subprocess, "run")
    def test_repository_v137_ready_but_authorization_still_blocked(self, run):
        import json
        run.return_value.returncode = 0
        run.return_value.stdout = json.dumps({
            "installed": {name: "1.36.6-1.1" for name in ("kubeadm", "kubelet", "kubectl")},
            "target_available": {name: True for name in ("kubeadm", "kubelet", "kubectl")},
            "repositories": {name: ["v1.37"] for name in ("kubeadm", "kubelet", "kubectl")}
        }).encode()
        result = upgrade.remote_packages("ok-bm-lab-01")
        self.assertEqual(upgrade.package_status(result)[0], "PASS")

    @patch.object(upgrade.subprocess, "run")
    def test_rejects_unexpected_package_repository(self, run):
        import json
        run.return_value.returncode = 0
        run.return_value.stdout = json.dumps({
            "installed": {name: "1.36.6-1.1" for name in ("kubeadm", "kubelet", "kubectl")},
            "target_available": {name: False for name in ("kubeadm", "kubelet", "kubectl")},
            "repositories": {name: ["v1.35"] for name in ("kubeadm", "kubelet", "kubectl")}
        }).encode()
        with self.assertRaises(SystemExit):
            upgrade.remote_packages("ok-bm-lab-01")

    @patch.object(upgrade.subprocess, "run")
    def test_rejects_mixed_repositories(self, run):
        import json
        run.return_value.returncode = 0
        run.return_value.stdout = json.dumps({
            "installed": {name: "1.36.6-1.1" for name in ("kubeadm", "kubelet", "kubectl")},
            "target_available": {name: False for name in ("kubeadm", "kubelet", "kubectl")},
            "repositories": {"kubeadm": ["v1.36"], "kubelet": ["v1.37"], "kubectl": ["v1.36"]}
        }).encode()
        with self.assertRaises(SystemExit):
            upgrade.remote_packages("ok-bm-lab-01")


class RemoteParserRegressionTests(unittest.TestCase):
    """Exercise the ACTUAL remote apt-cache parser embedded in upgrade.REMOTE.

    The tests above mock subprocess.run and inject an already-parsed
    'repositories' list, so none of them execute the remote regex. This class
    runs the real REMOTE snippet through bash with stub apt-cache/dpkg-query on
    PATH, feeding representative `apt-cache policy`/`madison` output, and asserts
    the Kubernetes package repository is actually detected. Regression guard for
    the double-escaped regex that silently returned empty repositories and made
    a healthy v1.36 host fail-close with STOP instead of EXPECTED_CHANGE.
    """

    def _run_remote(self, repo_minor, target_available):
        import json
        import os
        import shutil
        import subprocess
        import tempfile
        import textwrap
        if shutil.which("bash") is None:
            self.skipTest("bash required for remote-parser regression")
        work = tempfile.mkdtemp(prefix="ok-byoh-remote-parser-")
        self.addCleanup(shutil.rmtree, work, ignore_errors=True)
        bindir = os.path.join(work, "bin")
        os.mkdir(bindir)
        apt_cache = textwrap.dedent("""\
            #!/usr/bin/env bash
            set -eu
            sub="$1"; pkg="$2"
            minor="${OK_TEST_REPO_MINOR}"
            if [ "$sub" = policy ]; then
              printf '%s:\\n' "$pkg"
              printf '  Installed: 1.36.6-1.1\\n'
              printf '  Candidate: 1.36.6-1.1\\n'
              printf '  Version table:\\n'
              printf ' *** 1.36.6-1.1 500\\n'
              printf '        500 https://pkgs.k8s.io/core:/stable:/v%s/deb  Packages\\n' "$minor"
              printf '        100 /var/lib/dpkg/status\\n'
            elif [ "$sub" = madison ]; then
              printf '   %s |  1.36.6-1.1 | https://pkgs.k8s.io/core:/stable:/v%s/deb  Packages\\n' "$pkg" "$minor"
              if [ "${OK_TEST_TARGET_AVAILABLE}" = 1 ]; then
                printf '   %s |  1.37.2-1.1 | https://pkgs.k8s.io/core:/stable:/v1.37/deb  Packages\\n' "$pkg"
              fi
            fi
            """)
        dpkg_query = "#!/usr/bin/env bash\nprintf '1.36.6-1.1'\n"
        for name, body in (("apt-cache", apt_cache), ("dpkg-query", dpkg_query)):
            target = os.path.join(bindir, name)
            with open(target, "w", encoding="utf-8") as handle:
                handle.write(body)
            os.chmod(target, 0o755)
        env = dict(os.environ)
        env["PATH"] = bindir + os.pathsep + env["PATH"]
        env["OK_TEST_REPO_MINOR"] = repo_minor
        env["OK_TEST_TARGET_AVAILABLE"] = "1" if target_available else "0"
        proc = subprocess.run(["bash", "-c", upgrade.REMOTE],
                              capture_output=True, env=env, timeout=60)
        self.assertEqual(proc.returncode, 0,
                         "remote snippet failed: " + proc.stderr.decode(errors="replace"))
        return json.loads(proc.stdout)

    def test_real_parser_detects_v136_repository(self):
        data = self._run_remote("1.36", target_available=False)
        # Pre-fix this returned {name: []} for every package and triggered STOP.
        self.assertEqual(data["repositories"],
                         {n: ["v1.36"] for n in ("kubeadm", "kubelet", "kubectl")})
        self.assertEqual(data["target_available"],
                         {n: False for n in ("kubeadm", "kubelet", "kubectl")})
        result = upgrade.package_status(
            {"repository": data["repositories"]["kubeadm"][0],
             "available": data["target_available"]})
        self.assertEqual(result[0], "EXPECTED_CHANGE")

    def test_real_parser_detects_v137_repository(self):
        data = self._run_remote("1.37", target_available=True)
        self.assertEqual(data["repositories"],
                         {n: ["v1.37"] for n in ("kubeadm", "kubelet", "kubectl")})
        self.assertTrue(all(data["target_available"].values()))
        result = upgrade.package_status(
            {"repository": data["repositories"]["kubeadm"][0],
             "available": data["target_available"]})
        self.assertEqual(result[0], "PASS")


if __name__ == "__main__":
    unittest.main()
