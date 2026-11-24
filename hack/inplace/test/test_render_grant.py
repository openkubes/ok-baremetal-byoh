#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Offline tests of render_grant.py: python3 -I hack/inplace/test/test_render_grant.py"""
import argparse
import datetime
import importlib.util
import json
import os
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("rg", os.path.join(os.path.dirname(__file__), "..", "render_grant.py"))
rg = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rg)

NOW = datetime.datetime(2026, 11, 24, 12, 0, tzinfo=datetime.timezone.utc)
CLUSTER, KCP = "414317b7-77d4-4bd0-b86e-5cbe38b6c2c7", "11111111-2222-3333-4444-555555555555"


class Render(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.files = {}
        for name, text in (("lock", '{"v1.37.2": {}}'), ("req", "request\n"), ("auth", "approved\n")):
            self.files[name] = os.path.join(tmp.name, name)
            with open(self.files[name], "w") as f:
                f.write(text)

    def args(self, **kw):
        a = dict(namespace="ha1", cluster_uid=CLUSTER, kind="KubeadmControlPlane", name="ha1-control-plane", uid=KCP,
                 from_version="v1.36.6", to="v1.37.2", not_after="2026-11-24T18:00:00Z", package_lock=self.files["lock"],
                 request=self.files["req"], authorization=self.files["auth"])
        a.update(kw)
        return argparse.Namespace(**a)

    def test_kcp_grant(self):
        cm = rg.render(self.args(), NOW)
        d = cm["data"]
        self.assertEqual(cm["metadata"]["labels"], {rg.GRANT_LABEL: "true"})
        self.assertEqual(cm["metadata"]["namespace"], "ha1")
        self.assertNotIn("claim", d)
        self.assertNotIn("machineUID", d)
        self.assertEqual(d["transitions"], "KCP-1,KCP-2,KCP-3,KCP-C1,KCP-C2,KCP-IM,KCP-IM-C")
        self.assertEqual((d["targetKind"], d["targetUID"], d["clusterUID"]), ("KubeadmControlPlane", KCP, CLUSTER))
        self.assertEqual(d["permittedUseCount"], "1")
        for k in ("payloadDigest", "requestDigest", "authorizationDigest"):
            self.assertRegex(d[k], r"^sha256:[0-9a-f]{64}$")
        self.assertEqual(rg.render(self.args(), NOW), cm, "deterministic")

    def test_md_grant_includes_the_machineset_acknowledge(self):
        d = rg.render(self.args(kind="MachineDeployment", name="ha1-md-0"), NOW)["data"]
        self.assertIn("MD-ACK", d["transitions"].split(","))
        self.assertFalse(any(t.startswith("KCP") for t in d["transitions"].split(",")))

    def test_refusals(self):
        cases = {
            "unknown kind": dict(kind="MachineSet"),
            "not a UID": dict(uid="kcp"),
            "backwards": dict(from_version="v1.37.2", to="v1.36.6"),
            "two minors": dict(to="v1.38.0"),
            "expired": dict(not_after="2026-11-24T11:00:00Z"),
            "too long a window": dict(not_after="2026-11-26T12:00:00Z"),
            "no zone": dict(not_after="2026-11-24T18:00:00"),
        }
        for name, kw in cases.items():
            with self.assertRaises((ValueError, TypeError), msg=name):
                rg.render(self.args(**kw), NOW)

    def test_goldens_accepted_by_the_guard(self):
        """internal/safety/noreplacement/testdata holds this renderer's output; the guard's Go test
        (TestRenderedGrantsAreAccepted) admits exactly these grants, so renderer and guard cannot drift apart."""
        repo = os.path.join(os.path.dirname(__file__), "..", "..", "..")
        test_data = os.path.join(os.path.dirname(__file__), "testdata")
        common = dict(package_lock=os.path.join(repo, "config/inplace/base/packages-v1.37.2.json"),
                      request=os.path.join(test_data, "grant-request.txt"),
                      authorization=os.path.join(test_data, "grant-authorization.txt"))
        for short, kw in (("kcp", dict(uid=KCP)),
                          ("md", dict(kind="MachineDeployment", name="ha1-md-0", uid="66666666-7777-8888-9999-000000000000"))):
            with open(os.path.join(repo, f"internal/safety/noreplacement/testdata/rendered-grant-{short}.json")) as f:
                golden = json.load(f)
            self.assertEqual(rg.render(self.args(**common, **kw), NOW), golden, short)

    def test_empty_record_is_refused(self):
        with open(self.files["auth"], "w") as f:
            f.write("  \n")
        with self.assertRaises(ValueError):
            rg.render(self.args(), NOW)


if __name__ == "__main__":
    unittest.main()
