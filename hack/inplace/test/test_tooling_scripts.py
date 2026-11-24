#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Offline checks of shell tooling that only runs against a cluster: python3 -I hack/inplace/test/test_tooling_scripts.py

Each check pins a defect found in the ha1 run of 2026-11-24, where the script reported a failure that was its own."""
import os
import re
import unittest

HERE = os.path.dirname(__file__)


def code(name):
    with open(os.path.join(HERE, "..", name)) as f:
        return "\n".join(line for line in f.read().splitlines() if not line.lstrip().startswith("#"))


class DrainerVerify(unittest.TestCase):
    """`kubectl auth can-i create pods/eviction` asks for a pod named "eviction": the verify reported the eviction
    right of the drainer as missing although the ClusterRole grants it."""

    def setUp(self):
        self.script = code("drainer-credential.sh")

    def test_eviction_is_asked_as_a_subresource(self):
        self.assertIn('"yes create pods --subresource=eviction -A"', self.script)

    def test_no_resource_slash_name_query(self):
        self.assertNotRegex(self.script, r"can-i[^\n]*pods/eviction")
        self.assertNotIn("create pods/eviction", self.script)


class NegativeTests(unittest.TestCase):
    """Two cases could not reach the guard: maxSurge 2 is rejected by Cluster API's own validation, and a patch to
    the version the KCP already has is no change."""

    def setUp(self):
        self.script = code("negative-tests.sh")

    def test_maxsurge_is_raised_only_from_0_to_1(self):
        self.assertNotRegex(self.script, r'"maxSurge":\s*2')
        self.assertRegex(self.script, r'"maxSurge":\s*1')
        self.assertIn('if [ "$surge" = 0 ]; then', self.script)

    def test_version_case_never_patches_the_current_version(self):
        self.assertNotIn("${TARGET:-v1.37.2}", self.script)
        self.assertIn('[ "$ungranted" != "$current" ]', self.script)
        self.assertRegex(self.script, r"ungranted=\$\{TARGET:-")

    def test_every_request_is_a_dry_run(self):
        # every mutating verb goes through dry(), which adds --dry-run=server
        for line in self.script.splitlines():
            if re.search(r"\bk (-n \S+ )?(delete|patch|scale|create|label|annotate|apply)\b", line):
                self.fail("a request outside dry(): " + line.strip())
        self.assertIn('dry() { k --dry-run=server "$@" 2>&1; }', self.script)


if __name__ == "__main__":
    unittest.main()
