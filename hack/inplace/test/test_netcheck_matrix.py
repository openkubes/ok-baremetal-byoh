#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Offline execution tests for the real shell matrix runner."""

from collections import Counter
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest


HERE = Path(__file__).resolve().parents[1]
KUBECTL = """\
import fcntl
import json
import os
from pathlib import Path
import sys
import time

args = sys.argv[1:]
if args[:2] == ["get", "nodes"]:
    print("\\n".join(f"node-{i} Ready" for i in range(5, 0, -1)))
    sys.exit()
assert args[:2] == ["-n", "okx-netcheck"], args
args = args[2:]
if args[:2] == ["get", "pods"]:
    if "-l" not in args:
        print("echo Running")
        sys.exit()
    selector = args[args.index("-l") + 1]
    if os.environ.get("FAIL_LIST") == selector:
        sys.exit(1)
    if selector == "app=echo":
        print("\\n".join(f"192.0.2.{i} node-{i}" for i in range(5, 0, -1)))
    else:
        role = selector.split("role=")[1]
        end = 1 if os.environ.get("MISS_CLIENTS") == role else 0
        print("\\n".join(f"{role}-{i} node-{i}" for i in range(5, end, -1)))
    sys.exit()

action, pod, separator, binary, flag, script, argv0, target = args
assert (action, separator, binary, flag, argv0) == ("exec", "--", "/bin/sh", "-c", "sh")
assert script == r'/agnhost connect --timeout=3s "$1" && printf "%s\\n" OKX_CONNECT_OK'
assert not sys.stdin.read()

def event(phase):
    with Path(os.environ["EVENTS"]).open("a") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        stream.write(json.dumps([phase, pod, target]) + "\\n")
        stream.flush()

event("start")
time.sleep(0.06 if pod.endswith("1") else 0.02)
event("end")
if os.environ.get("FAIL_PROBE") == f"{pod} {target}":
    print("error: unable to create exec stream", file=sys.stderr)
    sys.exit(1)
override = json.loads(os.environ.get("PROBE_OVERRIDES", "{}")).get(f"{pod} {target}")
if override is not None:
    rc, stdout, stderr = override
    sys.stdout.write(stdout)
    sys.stderr.write(stderr)
    sys.exit(rc)
if os.environ["MODE"] in ("policy", "smoke") and pod.startswith("denied"):
    message = "REFUSED" if target.startswith("kubernetes.default.") else "TIMEOUT"
    print(message, "command terminated with exit code 1", sep="\\n", file=sys.stderr)
    sys.exit(1)
print("OKX_CONNECT_OK")
"""


class NetcheckMatrix(unittest.TestCase):
    def setUp(self):
        work = tempfile.TemporaryDirectory()
        self.addCleanup(work.cleanup)
        self.root = Path(work.name)
        self.runner = self.root / "netcheck.sh"
        shutil.copyfile(HERE / "netcheck.sh", self.runner)
        (self.root / "lib.sh").write_text(
            'set -euo pipefail\nTMPD=$(mktemp -d)\ntrap \'rm -rf -- "$TMPD"\' EXIT\n'
            'w() { "$TEST_PYTHON" "$MOCK_KUBECTL" "$@"; }\n'
        )
        self.mock = self.root / "kubectl.py"
        self.mock.write_text(KUBECTL)
        self.events = self.root / "events.jsonl"

    def env(self, mode, concurrency, **extras):
        self.events.write_text("")
        env = {k: v for k, v in os.environ.items() if k != "OKX_NETCHECK_CONCURRENCY"}
        if concurrency is not None:
            env["OKX_NETCHECK_CONCURRENCY"] = concurrency
        return {**env, "MODE": mode, "TEST_PYTHON": sys.executable, "MOCK_KUBECTL": str(self.mock),
                "EVENTS": str(self.events), **extras}

    def run_check(self, mode, concurrency=None, **extras):
        return subprocess.run(["bash", str(self.runner), mode], env=self.env(mode, concurrency, **extras),
                              capture_output=True, text=True, timeout=30)

    def recorded(self):
        return [json.loads(line) for line in self.events.read_text().splitlines()]

    @staticmethod
    def matrix(output):
        return [line for line in output.splitlines() if line.startswith(("PASS  ", "FAIL  ", "RESULT:"))]

    def starts_and_peak(self):
        starts = Counter()
        peak = active = 0
        for phase, pod, dst in self.recorded():
            active += 1 if phase == "start" else -1
            if phase == "start":
                starts[pod, dst] += 1
            peak = max(peak, active)
        self.assertEqual(active, 0)
        return starts, peak

    def test_complete_matrices_match_serial_and_have_bounded_parallelism(self):
        expected = Counter({
            (f"{role}-{i}", target): 1
            for role in ("allowed", "denied") for i in range(1, 6)
            for target in ([f"192.0.2.{j}:8080" for j in range(1, 6)]
                           + ["echo.okx-netcheck.svc.cluster.local:8080",
                              "kubernetes.default.svc.cluster.local:443"])
        })
        for mode in ("open", "policy"):
            with self.subTest(mode=mode):
                serial = self.run_check(mode)
                self.assertEqual(serial.returncode, 0, serial.stderr)
                self.assertEqual(self.starts_and_peak(), (expected, 1))
                parallel = self.run_check(mode, "4")
                self.assertEqual(parallel.returncode, 0, parallel.stderr)
                starts, peak = self.starts_and_peak()
                self.assertEqual(starts, expected)
                self.assertGreater(peak, 1)
                self.assertLessEqual(peak, 4)
                self.assertEqual(self.matrix(serial.stdout), self.matrix(parallel.stdout))
                self.assertEqual(self.matrix(parallel.stdout)[-1], "RESULT: 70/70 passed")

    def test_smoke_is_two_nodes_and_rejects_an_ambiguous_denial(self):
        for concurrency in ("1", "4"):
            with self.subTest(concurrency=concurrency):
                args = ["bash", str(self.runner), "smoke", "node-3"]
                good = subprocess.run(args, env=self.env("smoke", concurrency), capture_output=True, text=True, timeout=30)
                self.assertEqual(good.returncode, 0, good.stdout)
                self.assertIn("RESULT: 16/16 passed", good.stdout)
                starts, peak = self.starts_and_peak()
                self.assertEqual(sum(starts.values()), 16)
                self.assertEqual({pod for pod, _ in starts}, {"allowed-1", "allowed-3", "denied-1", "denied-3"})
                self.assertLessEqual(peak, int(concurrency))
                rejected = subprocess.run(args, env=self.env("smoke", concurrency,
                    PROBE_OVERRIDES=json.dumps({"denied-3 192.0.2.1:8080": [0, "", ""]})),
                    capture_output=True, text=True, timeout=30)
                self.assertEqual(rejected.returncode, 1)
                self.assertIn("expected timeout, got error", rejected.stdout)

    def test_failed_probe_preserves_fail_verdict_and_coverage(self):
        for mode in ("open", "policy"):
            with self.subTest(mode=mode):
                fail = {"FAIL_PROBE": "allowed-1 192.0.2.2:8080"}
                serial = self.run_check(mode, **fail)
                parallel = self.run_check(mode, "4", **fail)
                self.assertEqual((serial.returncode, parallel.returncode), (1, 1))
                self.assertEqual(self.matrix(serial.stdout), self.matrix(parallel.stdout))
                self.assertIn("RESULT: 69/70 passed", parallel.stdout)
                self.assertEqual(sum(self.starts_and_peak()[0].values()), 70)

    def test_empty_eof_fails_each_matrix_at_both_concurrency_settings(self):
        targets = ("allowed-1 192.0.2.1:8080",
                   "allowed-1 echo.okx-netcheck.svc.cluster.local:8080",
                   "allowed-1 kubernetes.default.svc.cluster.local:443",
                   "denied-1 192.0.2.1:8080")
        overrides = json.dumps(dict.fromkeys(targets, [0, "", ""]))
        for mode in ("open", "policy"):
            expected = [
                "FAIL  allowed node-1 -> echo node-1 (192.0.2.1) (expected ok, got error)",
                "FAIL  allowed node-1 -> Service (expected ok, got error)",
                "FAIL  allowed node-1 -> DNS (expected ok, got error)",
                f"FAIL  denied node-1 -> echo node-1 (192.0.2.1) "
                f"(expected {'ok' if mode == 'open' else 'timeout'}, got error)",
            ]
            for concurrency in ("1", "4"):
                with self.subTest(mode=mode, concurrency=concurrency):
                    result = self.run_check(mode, concurrency, PROBE_OVERRIDES=overrides)
                    self.assertEqual(result.returncode, 1, result.stderr)
                    self.assertEqual([line for line in self.matrix(result.stdout) if line.startswith("FAIL")], expected)
                    self.assertIn("RESULT: 66/70 passed", result.stdout)
                    self.assertEqual(sum(self.starts_and_peak()[0].values()), 70)

    def test_ambiguous_success_records_fail(self):
        ack = "OKX_CONNECT_OK\n"
        cases = [(0, "OKX_CONNECT_OK", ""), (0, ack + ack, ""), (0, ack + "\0", ""),
                 (0, ack, "error: exec stream lost\n"), (1, ack, ""), (2, ack, ""), (0, "unexpected\n", "")]
        overrides = {f"allowed-{i // 5 + 1} 192.0.2.{i % 5 + 1}:8080": outcome
                     for i, outcome in enumerate(cases)}
        result = self.run_check("open", "4", PROBE_OVERRIDES=json.dumps(overrides))
        self.assertEqual(result.returncode, 1, result.stderr)
        failures = [line for line in self.matrix(result.stdout) if line.startswith("FAIL")]
        self.assertEqual(len(failures), len(cases))
        self.assertTrue(all(line.endswith("(expected ok, got error)") for line in failures))
        self.assertIn("RESULT: 63/70 passed", result.stdout)
        self.assertEqual(sum(self.starts_and_peak()[0].values()), 70)

    def test_denied_echo_requires_an_unambiguous_remote_timeout(self):
        timeout = "TIMEOUT\ncommand terminated with exit code 1\n"
        cases = [
            (1, "", "error: unable to create exec stream\n", "error"),
            (1, "", "TIMEOUT\nerror: lost exec stream\n", "error"),
            (1, "", "TIMEOUT\n", "error"),
            (2, "", timeout, "error"),
            (1, timeout, "", "error"),
            (1, "", timeout + "warning: stream error\n", "error"),
            (1, "", "REFUSED\ncommand terminated with exit code 1\n", "refused"),
            (1, "\0", timeout, "error"),
        ]
        overrides = {}
        failures = []
        for i, (rc, stdout, stderr, outcome) in enumerate(cases):
            client, echo = divmod(i, 5)
            ip = f"192.0.2.{echo + 1}"
            overrides[f"denied-{client + 1} {ip}:8080"] = [rc, stdout, stderr]
            failures.append(f"FAIL  denied node-{client + 1} -> echo node-{echo + 1} ({ip}) "
                            f"(expected timeout, got {outcome})")
        for concurrency in ("1", "4"):
            with self.subTest(concurrency=concurrency):
                result = self.run_check("policy", concurrency, PROBE_OVERRIDES=json.dumps(overrides))
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual([line for line in self.matrix(result.stdout) if line.startswith("FAIL")], failures)
                self.assertIn("RESULT: 62/70 passed", result.stdout)
                self.assertEqual(sum(self.starts_and_peak()[0].values()), 70)

    def test_failed_or_short_inventory_and_invalid_concurrency_fail_closed(self):
        scenarios = (
            ("4", {"FAIL_LIST": "app=echo"}),
            ("4", {"FAIL_LIST": "app=client,role=denied"}),
            ("4", {"MISS_CLIENTS": "denied"}),
            *((value, {}) for value in ("0", "-1", "04", "17", "99999999999999999999999999")),
        )
        for concurrency, extras in scenarios:
            with self.subTest(concurrency=concurrency, extras=extras):
                result = self.run_check("policy", concurrency, **extras)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("RESULT:", result.stdout)
                self.starts_and_peak()

    def test_interrupted_matrix_joins_active_jobs_and_never_reports_pass(self):
        with subprocess.Popen(["bash", str(self.runner), "policy"], env=self.env("policy", "4"),
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) as child:
            until = time.monotonic() + 10
            try:
                while time.monotonic() < until and len(self.recorded()) < 2 and child.poll() is None:
                    time.sleep(0.01)
                self.assertIsNone(child.poll())
                self.assertGreaterEqual(len(self.recorded()), 2)
            finally:
                if child.poll() is None:
                    child.terminate()
                stdout, _ = child.communicate(timeout=10)
            self.assertEqual(child.returncode, 143)
            self.assertNotIn("RESULT:", stdout)
            self.starts_and_peak()


if __name__ == "__main__":
    unittest.main()
