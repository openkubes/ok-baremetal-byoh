#!/usr/bin/env python3
"""Offline regression tests for bounded BYOH replay decisions."""
import importlib.util
from datetime import datetime, timedelta, timezone
from pathlib import Path
import unittest

path = Path(__file__).with_name("byoh-hosts-state.py")
spec = importlib.util.spec_from_file_location("byoh_hosts_state", path)
state = importlib.util.module_from_spec(spec)
spec.loader.exec_module(state)

NOW = datetime(2026, 11, 19, 20, 0, tzinfo=timezone.utc)
CA = "test-ca-not-a-secret"
ENDPOINT = "https://127.0.0.1:16443"


def request(age=120):
    return {
        "metadata": {"name": "cp-1", "creationTimestamp":
                     (NOW - timedelta(seconds=age)).isoformat()},
        "spec": {"apiserver": ENDPOINT, "certificate-authority-data": CA},
        "status": {"bootstrapKubeconfigData": "test-only-credential"},
    }


class ReplayTests(unittest.TestCase):
    def check(self, obj):
        state.require_fresh_bootstrap(obj, name="cp-1", apiserver=ENDPOINT,
                                      ca_data=CA, now=NOW)

    def test_replay_fresh_identical_request(self):
        self.check(request())

    def test_stale_request_rejected(self):
        with self.assertRaises(state.ReplayStop):
            self.check(request(age=1200))

    def test_future_request_rejected(self):
        with self.assertRaises(state.ReplayStop):
            self.check(request(age=-90))

    def test_changed_ca_or_endpoint_rejected(self):
        for field, invalid in (("apiserver", "https://127.0.0.1:9"),
                               ("certificate-authority-data", "changed")):
            with self.subTest(field=field):
                obj = request()
                obj["spec"][field] = invalid
                with self.assertRaises(state.ReplayStop):
                    self.check(obj)

    def test_deleted_or_unready_request_rejected(self):
        obj = request()
        obj["metadata"]["deletionTimestamp"] = NOW.isoformat()
        with self.assertRaises(state.ReplayStop):
            self.check(obj)
        obj = request()
        obj["status"] = {}
        with self.assertRaises(state.ReplayStop):
            self.check(obj)

    def test_transfer_first_and_replay(self):
        digest = "a" * 64
        self.assertEqual(state.transfer_action(existing=False, existing_digest=None,
                                               expected_digest=digest), "install")
        self.assertEqual(state.transfer_action(existing=True, existing_digest=digest,
                                               expected_digest=digest), "verify-existing")

    def test_transfer_mismatch_fails_closed(self):
        with self.assertRaises(state.ReplayStop):
            state.transfer_action(existing=True, existing_digest="b" * 64,
                                  expected_digest="a" * 64)

    def test_unit_replay_is_noop(self):
        self.assertEqual(
            state.unit_action(expected_dropin_present=True,
                              expected_dropin_matches=True,
                              effective_flags_match=True,
                              working_directory_matches=True), "unchanged")

    def test_unit_first_install(self):
        self.assertEqual(
            state.unit_action(expected_dropin_present=False,
                              expected_dropin_matches=False,
                              effective_flags_match=False,
                              working_directory_matches=False), "install")

    def test_unit_drift_fails_closed(self):
        with self.assertRaises(state.ReplayStop):
            state.unit_action(expected_dropin_present=True,
                              expected_dropin_matches=False,
                              effective_flags_match=True,
                              working_directory_matches=True)

    def test_plan_changed_host_changes_digest(self):
        plan = {"hosts": [{"name": "cp-1"}]}
        old = state.plan_fingerprint(plan)
        plan["hosts"].append({"name": "worker-1"})
        self.assertNotEqual(old, state.plan_fingerprint(plan))


if __name__ == "__main__":
    unittest.main(verbosity=2)
