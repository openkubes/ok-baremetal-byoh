#!/usr/bin/env python3
"""Pure, offline-testable BYOH bootstrap replay decisions.

No remote changes and no token contents are exposed. A replay is permitted only
for exact same credential identity and a fresh, management-bound request.
"""
import hashlib
import json
from datetime import datetime, timezone


class ReplayStop(ValueError):
    pass


def require_fresh_bootstrap(obj, *, name, apiserver, ca_data, now=None, max_age_seconds=1200):
    """Validate an existing token request without printing its status credential."""
    try:
        if obj["metadata"]["name"] != name:
            raise ReplayStop("bootstrap host identity mismatch")
        if obj.get("metadata", {}).get("deletionTimestamp"):
            raise ReplayStop("bootstrap request is deleting")
        if obj["spec"]["apiserver"] != apiserver:
            raise ReplayStop("bootstrap management endpoint mismatch")
        if obj["spec"]["certificate-authority-data"] != ca_data:
            raise ReplayStop("bootstrap management CA mismatch")
        created = datetime.fromisoformat(
            obj["metadata"]["creationTimestamp"].replace("Z", "+00:00"))
        if created.tzinfo is None:
            raise ReplayStop("bootstrap creation time has no timezone")
        current = now or datetime.now(timezone.utc)
        age = (current - created).total_seconds()
        if age < -60 or age >= max_age_seconds:
            raise ReplayStop("bootstrap request stale or from future")
        if not obj.get("status", {}).get("bootstrapKubeconfigData"):
            raise ReplayStop("bootstrap request not ready")
    except (KeyError, ValueError, TypeError) as exc:
        raise ReplayStop("bootstrap request malformed or unsafe") from exc


def transfer_action(*, existing, existing_digest, expected_digest):
    """Determine transfer without overriding an existing remote credential."""
    if not isinstance(expected_digest, str) or len(expected_digest) != 64:
        raise ReplayStop("invalid expected checksum")
    try:
        int(expected_digest, 16)
    except ValueError as exc:
        raise ReplayStop("invalid expected checksum") from exc
    if not existing:
        return "install"
    if existing_digest == expected_digest:
        return "verify-existing"
    raise ReplayStop("existing bootstrap credential differs; explicit recovery required")


def plan_fingerprint(plan):
    return hashlib.sha256(json.dumps(
        plan, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def unit_action(*, expected_dropin_present, expected_dropin_matches,
                effective_flags_match, working_directory_matches):
    if expected_dropin_present and not expected_dropin_matches:
        raise ReplayStop("existing unit drop-in differs")
    if (expected_dropin_present and expected_dropin_matches and
            effective_flags_match and working_directory_matches):
        return "unchanged"
    return "install"
