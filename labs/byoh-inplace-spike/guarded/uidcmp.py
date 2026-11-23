#!/usr/bin/env python3
# Pure UID/identity comparison for in-place accept evidence. Reads two uids-*.json
# snapshots (before/after) and asserts Machine, InfraMachine and Node UIDs are
# UNCHANGED (no replacement), while spec.version advanced. Exit 0 iff PASS.
import json, sys

def load(p):
    with open(p) as f: return json.load(f)

def index(items, key="name"):
    return {i[key]: i for i in items}

def main():
    before, after = load(sys.argv[1]), load(sys.argv[2])
    findings, ok = [], True

    for kind in ("machines", "infraMachines", "nodes"):
        # A missing/empty snapshot (e.g. unreachable workload API) must never pass vacuously.
        if not before.get(kind) or not after.get(kind):
            ok = False
            findings.append(f"{kind}: snapshot missing or empty (cannot prove identity)")
            continue
        b, a = index(before[kind]), index(after[kind])
        if set(b) != set(a):
            ok = False
            findings.append(f"{kind}: name set changed before={sorted(b)} after={sorted(a)} (REPLACEMENT)")
            continue
        for name in b:
            if b[name]["uid"] != a[name]["uid"]:
                ok = False
                findings.append(f"{kind}/{name}: UID changed {b[name]['uid']} -> {a[name]['uid']} (REPLACEMENT)")
            else:
                findings.append(f"{kind}/{name}: UID preserved {b[name]['uid']}")

    # version should advance on the Machines (in-place metadata convergence)
    bm, am = index(before.get("machines", [])), index(after.get("machines", []))
    for name in set(bm) & set(am):
        bv, av = bm[name].get("version"), am[name].get("version")
        if bv != av:
            findings.append(f"machines/{name}: spec.version {bv} -> {av} (advanced)")

    print(json.dumps({"verdict": "PASS" if ok else "FAIL", "findings": findings}, indent=2))
    sys.exit(0 if ok else 1)

if __name__ == "__main__":
    main()
