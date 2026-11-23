#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Offline tests of cilium_compat.py: python3 -I hack/inplace/test/test_cilium_compat.py"""
import importlib.util
import json
import os
import unittest

HERE = os.path.dirname(__file__)
spec = importlib.util.spec_from_file_location("cc", os.path.join(HERE, "..", "cilium_compat.py"))
cc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cc)

with open(os.path.join(HERE, "..", "cilium-compat.json")) as f:
    TABLE = json.load(f)
PRE = "1.21.0-pre.4"
AGENT = TABLE["prerelease"][PRE]["agent"]
OPERATOR = TABLE["prerelease"][PRE]["operator"]
STABLE_AGENT = "quay.io/cilium/cilium:v1.20.5@sha256:" + "a" * 64
STABLE_OPERATOR = "quay.io/cilium/operator-generic:v1.20.5@sha256:" + "b" * 64


class Stable(unittest.TestCase):
    def test_listed_minor(self):
        self.assertTrue(cc.check(TABLE, STABLE_AGENT, STABLE_OPERATOR, "v1.36.6")[0])

    def test_unlisted_minor(self):
        self.assertFalse(cc.check(TABLE, STABLE_AGENT, STABLE_OPERATOR, "v1.37.2")[0])

    def test_unknown_stable_version(self):
        self.assertFalse(cc.check(TABLE, STABLE_AGENT.replace("1.20.5", "1.20.9"),
                                  STABLE_OPERATOR.replace("1.20.5", "1.20.9"), "v1.36.6")[0])

    def test_without_digest_is_parsed(self):
        self.assertTrue(cc.check(TABLE, "quay.io/cilium/cilium:v1.20.5", "quay.io/cilium/operator-generic:v1.20.5", "v1.36.6")[0])


class PreRelease(unittest.TestCase):
    def test_pinned_and_approved(self):
        ok, detail = cc.check(TABLE, AGENT, OPERATOR, "v1.37.2", PRE)
        self.assertTrue(ok, detail)
        self.assertIn("PRE-RELEASE", detail)

    def test_not_approved_for_the_run(self):
        self.assertFalse(cc.check(TABLE, AGENT, OPERATOR, "v1.37.2")[0])
        self.assertFalse(cc.check(TABLE, AGENT, OPERATOR, "v1.37.2", "1.21.0-pre.3")[0])

    def test_other_digest(self):
        self.assertFalse(cc.check(TABLE, AGENT[:-1] + "0", OPERATOR, "v1.37.2", PRE)[0])
        self.assertFalse(cc.check(TABLE, AGENT, OPERATOR[:-1] + "0", "v1.37.2", PRE)[0])

    def test_tag_without_digest(self):
        self.assertFalse(cc.check(TABLE, AGENT.split("@")[0], OPERATOR, "v1.37.2", PRE)[0])

    def test_unlisted_prerelease(self):
        a, o = AGENT.replace("pre.4", "pre.5"), OPERATOR.replace("pre.4", "pre.5")
        self.assertFalse(cc.check(TABLE, a, o, "v1.37.2", "1.21.0-pre.5")[0])

    def test_rc_is_a_prerelease(self):
        a = "quay.io/cilium/cilium:v1.21.0-rc.1@sha256:" + "c" * 64
        o = "quay.io/cilium/operator-generic:v1.21.0-rc.1@sha256:" + "d" * 64
        self.assertFalse(cc.check(TABLE, a, o, "v1.37.2", "1.21.0-rc.1")[0])

    def test_unlisted_target_minor(self):
        self.assertFalse(cc.check(TABLE, AGENT, OPERATOR, "v1.38.0", PRE)[0])


class Malformed(unittest.TestCase):
    def test_agent_and_operator_differ(self):
        self.assertFalse(cc.check(TABLE, AGENT, STABLE_OPERATOR, "v1.37.2", PRE)[0])

    def test_unparseable(self):
        for agent in ["quay.io/cilium/cilium:latest", "quay.io/cilium/cilium@sha256:" + "a" * 64,
                      "quay.io/cilium/cilium:v1.21.0-beta.1", "quay.io/cilium/cilium:v1.21", ""]:
            self.assertFalse(cc.check(TABLE, agent, OPERATOR, "v1.37.2", PRE)[0], agent)
        self.assertFalse(cc.check(TABLE, AGENT, OPERATOR, "1.37.2", PRE)[0])


if __name__ == "__main__":
    unittest.main()
