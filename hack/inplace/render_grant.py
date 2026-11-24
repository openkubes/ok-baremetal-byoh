#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Render (never apply) the upgrade grant of a KubeadmControlPlane or a MachineDeployment.

  render_grant.py --namespace NS --cluster-uid UID --kind KubeadmControlPlane|MachineDeployment --name NAME --uid UID
                  --from vX.Y.Z --to vX.Y.Z --not-after RFC3339 --package-lock FILE --request FILE --authorization FILE

Prints the grant ConfigMap as JSON for the approver to review and create (the guard admits the CREATE only from the
approver identity, without a claim; the initiator later adds the single claim). One grant per KCP and one per MD,
without machineUID (it would make the lookup ambiguous). Data (M1 §1.1, internal/safety/noreplacement/grant.go):
  transitions          every in-place transition of the target kind (KCP-* or MD-* incl. MD-ACK)
  payloadDigest        sha256 of the package lock of the target version (the pinned debs the hosts install)
  requestDigest        sha256 of the change request record (who requested what, evidence reference)
  authorizationDigest  sha256 of the approval record (who approved, when, which gate)
  notAfter             the end of the change window (keep it short); permittedUseCount 1
The record files stay in the gate evidence, so every digest can be traced back to its document.
A grant is never deleted or changed. If the window ends before the rollout does, render a new grant with a later
--not-after once the first one has expired (two valid grants of one target are ambiguous and denied); it gets
another name, because the end of the window is part of the grant ID.
"""
import argparse
import datetime
import hashlib
import json
import re
import sys

GRANT_LABEL = "safety.openkubes.org/grant"
TRANSITIONS = {
    "KubeadmControlPlane": ["KCP-1", "KCP-2", "KCP-3", "KCP-C1", "KCP-C2", "KCP-IM", "KCP-IM-C"],
    "MachineDeployment": ["MD-MOVE", "MD-FIN-M", "MD-TRIG", "MD-C1", "MD-C2", "MD-FIN-IM", "MD-ACK"],
}
SHORT = {"KubeadmControlPlane": "kcp", "MachineDeployment": "md"}
VERSION = re.compile(r"v(\d+)\.(\d+)\.(\d+)")
UID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")


def digest(path):
    with open(path, "rb") as f:
        data = f.read()
    if not data.strip():
        raise ValueError(f"{path} is empty")
    return "sha256:" + hashlib.sha256(data).hexdigest()


def render(a, now=None):
    now = now or datetime.datetime.now(datetime.timezone.utc)
    if a.kind not in TRANSITIONS:
        raise ValueError(f"kind must be one of {sorted(TRANSITIONS)}")
    for name, value in (("cluster-uid", a.cluster_uid), ("uid", a.uid)):
        if not UID.fullmatch(value):
            raise ValueError(f"--{name} {value!r} is not a UID")
    f, t = VERSION.fullmatch(a.from_version), VERSION.fullmatch(a.to)
    if not f or not t:
        raise ValueError("--from and --to must be vX.Y.Z")
    fv, tv = tuple(map(int, f.groups())), tuple(map(int, t.groups()))
    if not (fv[0] == tv[0] and tv > fv and tv[1] <= fv[1] + 1):
        raise ValueError(f"{a.from_version} -> {a.to} is not a supported in-place transition (forward, at most one minor)")
    not_after = datetime.datetime.fromisoformat(a.not_after.replace("Z", "+00:00"))
    if not_after.tzinfo is None or not_after <= now:
        raise ValueError("--not-after must be a future RFC3339 time with a zone")
    if not_after - now > datetime.timedelta(hours=24):
        raise ValueError("--not-after is more than 24 h away: keep the change window short")
    pkg = digest(a.package_lock)
    # The end of the window is part of the ID: a grant cannot be deleted or changed, so the grant of a later window
    # for the same target (after the first one expired) needs another name.
    window = not_after.strftime("%Y-%m-%dT%H:%M:%SZ")
    grant_id = f"{SHORT[a.kind]}-{a.name}-{a.to}-{hashlib.sha256((a.uid + a.to + pkg + window).encode()).hexdigest()[:8]}"
    data = {
        "grantID": grant_id, "clusterUID": a.cluster_uid, "targetKind": a.kind, "targetName": a.name, "targetUID": a.uid,
        "fromVersion": a.from_version, "toVersion": a.to, "transitions": ",".join(TRANSITIONS[a.kind]),
        "payloadDigest": pkg, "notAfter": window, "permittedUseCount": "1",
        "requestDigest": digest(a.request), "authorizationDigest": digest(a.authorization),
    }
    return {"apiVersion": "v1", "kind": "ConfigMap",
            "metadata": {"name": f"okx-grant-{grant_id}"[:253].lower(), "namespace": a.namespace, "labels": {GRANT_LABEL: "true"}},
            "data": data}


def main(argv):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    for opt in ("--namespace", "--cluster-uid", "--kind", "--name", "--uid", "--to", "--not-after", "--package-lock",
                "--request", "--authorization"):
        p.add_argument(opt, required=True)
    p.add_argument("--from", dest="from_version", required=True)
    a = p.parse_args(argv)
    try:
        print(json.dumps(render(a), indent=2, sort_keys=True))
    except (ValueError, OSError) as e:
        print(f"refused: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
