#!/usr/bin/env python3
"""Offline regression tests for bootstrap revocation result handling."""

import runpy
import pathlib
import unittest
import os
from unittest.mock import patch

SCRIPT = pathlib.Path(__file__).with_name(
    "test-byoh-bootstrap-revocation-live.py"
)

token_rejected = runpy.run_path(str(SCRIPT))["token_rejected"]


class TokenReviewResultTest(unittest.TestCase):

    def test_explicit_rejection(self):
        self.assertTrue(token_rejected({"authenticated": False}))

    def test_invalid_bearer_token(self):
        self.assertTrue(
            token_rejected({"error": "invalid bearer token"})
        )

    def test_authenticated_token(self):
        self.assertFalse(
            token_rejected({"authenticated": True})
        )

    def test_empty_response_rejected(self):
        with self.assertRaises(RuntimeError):
            token_rejected({})

    def test_internal_error_rejected(self):
        with self.assertRaises(RuntimeError):
            token_rejected({"error": "internal authentication error"})

    def test_backend_error_rejected(self):
        with self.assertRaises(RuntimeError):
            token_rejected({
                "authenticated": False,
                "error": "backend unavailable",
            })

    def test_conflicting_result_rejected(self):
        with self.assertRaises(RuntimeError):
            token_rejected({
                "authenticated": True,
                "error": "invalid bearer token",
            })

    def test_unexpected_identity_rejected(self):
        with self.assertRaises(RuntimeError):
            token_rejected({
                "authenticated": False,
                "user": {"username": "unexpected"},
            })



class CleanupFailureTest(unittest.TestCase):

    def test_cleanup_failure_preserves_original_error(self):
        main = runpy.run_path(str(SCRIPT))["main"]
        namespace = main.__globals__

        def fake_kube_json(*args, **kwargs):
            if args[:2] == ("config", "view"):
                return {
                    "clusters": [{
                        "cluster": {
                            "server": "https://127.0.0.1:6443",
                            "certificate-authority-data": "ZHVtbXk=",
                        }
                    }]
                }
            if args[0] == "create":
                return {}
            raise AssertionError("Unexpected API call")

        def fake_wait(*args):
            raise RuntimeError("synthetic issuance failure")

        def fake_kube(*args, **kwargs):
            raise RuntimeError("synthetic cleanup failure")

        replacements = {
            "check_target": lambda: None,
            "kube_json": fake_kube_json,
            "kube": fake_kube,
            "get_optional": lambda *args: {"metadata": {}},
            "wait_for": fake_wait,
        }

        with (
            patch.dict(os.environ, {
                "BYOH_LIVE_REVOCATION_ACK": "yes"
            }),
            patch.dict(namespace, replacements),
        ):
            with self.assertRaises(RuntimeError) as caught:
                main()

        self.assertIn(
            "Test cleanup incomplete", str(caught.exception)
        )
        self.assertIsNotNone(caught.exception.__cause__)
        self.assertIn(
            "synthetic issuance failure",
            str(caught.exception.__cause__),
        )


if __name__ == "__main__":
    unittest.main()
