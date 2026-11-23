#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Identity snapshots and comparison for an in-place upgrade (read-only).

  uids.py snapshot MACHINES.json BYOMACHINES.json BYOHOSTS.json NODES.json > snap.json
      Builds a snapshot from `kubectl get -o json` lists of the Machines, ByoMachines,
      ByoHosts (management) and Nodes (workload).
  uids.py compare BEFORE.json AFTER.json [--expect-version vX.Y.Z]
      PASS (exit 0) only when every Machine, ByoMachine, ByoHost and Node keeps its UID,
      every ByoHost stays bound to the same ByoMachine UID, nothing is added or removed,
      and (with --expect-version) every Machine and Node reports the version. Any missing
      or empty part of a snapshot is a FAIL: identity is never proved vacuously.
"""
import json
import sys

KINDS = ("machines", "byoMachines", "byoHosts", "nodes")


def items(path):
    with open(path) as f:
        return json.load(f).get("items", [])


def snapshot(machines, byomachines, byohosts, nodes):
    snap = {"machines": [], "byoMachines": [], "byoHosts": [], "nodes": []}
    for m in items(machines):
        status = m.get("status", {})
        snap["machines"].append({
            "name": m["metadata"]["name"], "uid": m["metadata"]["uid"],
            "version": m.get("spec", {}).get("version"),
            "node": status.get("nodeRef", {}).get("name"),
            "kubelet": (status.get("nodeInfo") or {}).get("kubeletVersion"),
        })
    for b in items(byomachines):
        snap["byoMachines"].append({"name": b["metadata"]["name"], "uid": b["metadata"]["uid"]})
    for h in items(byohosts):
        ref = h.get("spec", {}).get("byoMachineRef") or {}
        snap["byoHosts"].append({"name": h["metadata"]["name"], "uid": h["metadata"]["uid"], "byoMachineUID": ref.get("uid")})
    for n in items(nodes):
        snap["nodes"].append({"name": n["metadata"]["name"], "uid": n["metadata"]["uid"],
                              "kubelet": n.get("status", {}).get("nodeInfo", {}).get("kubeletVersion")})
    return snap


def compare(before, after, expect_version=None):
    findings, ok = [], True
    for kind in KINDS:
        if not before.get(kind) or not after.get(kind):
            ok = False
            findings.append(f"{kind}: snapshot missing or empty (cannot prove identity)")
            continue
        b = {i["name"]: i for i in before[kind]}
        a = {i["name"]: i for i in after[kind]}
        if set(b) != set(a):
            ok = False
            findings.append(f"{kind}: set changed before={sorted(b)} after={sorted(a)} (REPLACEMENT)")
            continue
        for name in sorted(b):
            if b[name]["uid"] != a[name]["uid"]:
                ok = False
                findings.append(f"{kind}/{name}: UID changed {b[name]['uid']} -> {a[name]['uid']} (REPLACEMENT)")
            elif kind == "byoHosts" and b[name].get("byoMachineUID") != a[name].get("byoMachineUID"):
                ok = False
                findings.append(f"byoHosts/{name}: bound ByoMachine changed {b[name].get('byoMachineUID')} -> {a[name].get('byoMachineUID')}")
            else:
                findings.append(f"{kind}/{name}: UID preserved {b[name]['uid']}")
    if expect_version:
        for kind, field in (("machines", "version"), ("machines", "kubelet"), ("nodes", "kubelet")):
            for i in after.get(kind, []):
                if i.get(field) != expect_version:
                    ok = False
                    findings.append(f"{kind}/{i['name']}: {field} {i.get(field)} is not {expect_version}")
    return ok, findings


def main(argv):
    if len(argv) == 6 and argv[1] == "snapshot":
        print(json.dumps(snapshot(*argv[2:6]), indent=2, sort_keys=True))
        return 0
    if len(argv) in (4, 6) and argv[1] == "compare":
        expect = None
        if len(argv) == 6:
            if argv[4] != "--expect-version":
                print(__doc__, file=sys.stderr)
                return 2
            expect = argv[5]
        with open(argv[2]) as f:
            before = json.load(f)
        with open(argv[3]) as f:
            after = json.load(f)
        ok, findings = compare(before, after, expect)
        print(json.dumps({"verdict": "PASS" if ok else "FAIL", "findings": findings}, indent=2))
        return 0 if ok else 1
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
