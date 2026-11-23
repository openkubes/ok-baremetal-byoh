#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Offline tests of uids.py: python3 -I hack/inplace/test/test_uids.py"""
import copy
import importlib.util
import os
import unittest

spec = importlib.util.spec_from_file_location("uids", os.path.join(os.path.dirname(__file__), "..", "uids.py"))
uids = importlib.util.module_from_spec(spec)
spec.loader.exec_module(uids)


def snap(version="v1.36.6"):
    return {
        "machines": [{"name": "cp-1", "uid": "m1", "version": version, "node": "n1", "kubelet": version}],
        "byoMachines": [{"name": "bm-1", "uid": "b1"}],
        "byoHosts": [{"name": "h1", "uid": "h1", "byoMachineUID": "b1"}],
        "nodes": [{"name": "n1", "uid": "n1", "kubelet": version}],
    }


class Compare(unittest.TestCase):
    def test_preserved_identity_with_the_target_version(self):
        ok, _ = uids.compare(snap(), snap("v1.37.2"), "v1.37.2")
        self.assertTrue(ok)

    def test_replaced_machine(self):
        after = snap("v1.37.2")
        after["machines"][0]["uid"] = "m2"
        self.assertFalse(uids.compare(snap(), after)[0])

    def test_added_machine(self):
        after = snap()
        after["machines"].append({"name": "cp-1-new", "uid": "m3"})
        self.assertFalse(uids.compare(snap(), after)[0])

    def test_rebound_host(self):
        after = snap()
        after["byoHosts"][0]["byoMachineUID"] = "b2"
        self.assertFalse(uids.compare(snap(), after)[0])

    def test_replaced_node(self):
        after = snap()
        after["nodes"][0]["uid"] = "n2"
        self.assertFalse(uids.compare(snap(), after)[0])

    def test_empty_snapshot_never_passes(self):
        for kind in uids.KINDS:
            after = copy.deepcopy(snap())
            after[kind] = []
            self.assertFalse(uids.compare(snap(), after)[0], kind)

    def test_version_not_reached(self):
        after = snap("v1.37.2")
        after["nodes"][0]["kubelet"] = "v1.36.6"
        self.assertFalse(uids.compare(snap(), after, "v1.37.2")[0])


if __name__ == "__main__":
    unittest.main()
