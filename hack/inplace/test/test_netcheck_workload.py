#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Offline checks of netcheck/workload.yaml: python3 -I hack/inplace/test/test_netcheck_workload.py"""
import os
import re
import unittest

HERE = os.path.dirname(__file__)
with open(os.path.join(HERE, "..", "netcheck", "workload.yaml")) as f:
    DOCS = f.read().split("\n---\n")


def doc(kind, name):
    found = [d for d in DOCS if re.search(r"^kind: %s$" % kind, d, re.M) and re.search(r"^  name: %s$" % name, d, re.M)]
    assert len(found) == 1, (kind, name, len(found))
    return found[0]


def code(text):
    return "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#"))


class EchoReadiness(unittest.TestCase):
    """The echo pods back the Service: a probe that never succeeds leaves it without ready endpoints."""

    def setUp(self):
        self.echo = code(doc("DaemonSet", "echo"))

    def test_udp_listener_is_disabled(self):
        self.assertIn('"--udp-port=-1"', self.echo)

    def test_probe_is_tcp_on_the_http_port(self):
        probes = re.findall(r"^\s*readinessProbe: (.*)$", self.echo, re.M)
        self.assertEqual(len(probes), 1)
        self.assertRegex(probes[0], r"tcpSocket: \{port: 8080\}")

    def test_no_healthz_probe_without_udp(self):
        # observed on ha1: /healthz stays 412 with --udp-port=-1 (agnhost 2.56)
        self.assertNotIn("/healthz", self.echo)

    def test_service_targets_the_probed_port(self):
        self.assertRegex(code(doc("Service", "echo")), r"targetPort: 8080")


if __name__ == "__main__":
    unittest.main()
