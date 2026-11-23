#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Does the running Cilium support the target Kubernetes minor? (read-only)

  cilium_compat.py COMPAT_JSON AGENT_IMAGE OPERATOR_IMAGE TARGET_VERSION [APPROVED_PRERELEASE]

COMPAT_JSON (cilium-compat.json):
  "stable":     {"1.20.5": ["1.33", ...]}             released versions, e2e-tested minors
  "prerelease": {"1.21.0-pre.4": {"kubernetes": [...], "agent": "<ref>@sha256:...",
                 "operator": "<ref>@sha256:...", "approval": "<why and where approved>"}}

A stable version passes when its table lists the target minor. A pre-release passes only when
  - it is listed under "prerelease" (reviewed and committed),
  - the running agent and operator images are exactly the pinned references, digests included,
  - the operator explicitly approved this run: APPROVED_PRERELEASE equals the version.
Agent and operator must run the same version. Anything unparseable fails. Exit 0 PASS, 1 FAIL.
"""
import json
import re
import sys

IMAGE = re.compile(r"(?P<repo>[a-z0-9][a-z0-9./_-]*):v(?P<version>\d+\.\d+\.\d+(?:-(?:pre|rc)\.\d+)?)"
                   r"(?:@sha256:(?P<digest>[0-9a-f]{64}))?")
TARGET = re.compile(r"v(\d+)\.(\d+)\.(\d+)")


def version_of(image):
    m = IMAGE.fullmatch(image)
    return m.group("version") if m else None


def check(table, agent, operator, target, approved=""):
    """Returns (ok, detail)."""
    version, op_version = version_of(agent), version_of(operator)
    t = TARGET.fullmatch(target)
    if not version or not op_version or not t:
        return False, f"cannot parse agent {agent!r}, operator {operator!r} or target {target!r}"
    if version != op_version:
        return False, f"agent {version} and operator {op_version} differ"
    minor = f"{t.group(1)}.{t.group(2)}"
    if "-" not in version:
        minors = table.get("stable", {}).get(version, [])
        return (minor in minors), f"Cilium {version} lists {', '.join(minors) or 'nothing'}"
    pin = table.get("prerelease", {}).get(version)
    if not pin:
        return False, f"pre-release {version} is not an approved entry of the compatibility table"
    if agent != pin.get("agent") or operator != pin.get("operator"):
        return False, f"pre-release {version}: running images are not the pinned digests"
    if approved != version:
        return False, f"pre-release {version}: not approved for this run (OKX_CILIUM_PRERELEASE={approved or 'unset'})"
    minors = pin.get("kubernetes", [])
    return (minor in minors), f"PRE-RELEASE Cilium {version} (pinned, approved) lists {', '.join(minors)}"


def main(argv):
    if len(argv) not in (4, 5):
        print(__doc__.strip().splitlines()[2].strip(), file=sys.stderr)
        return 2
    with open(argv[0]) as f:
        table = json.load(f)
    ok, detail = check(table, argv[1], argv[2], argv[3], argv[4] if len(argv) == 5 else "")
    print(detail)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
