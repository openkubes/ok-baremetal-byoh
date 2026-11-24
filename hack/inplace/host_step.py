#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Per-host gate of an in-place upgrade (read-only decision; used by host-step.sh).

  host_step.py next   MACHINES.json BYOMACHINES.json BYOHOSTS.json NODES.json TARGET SEQUENCE
  host_step.py verify MACHINES.json BYOMACHINES.json BYOHOSTS.json NODES.json TARGET HOST

SEQUENCE is the approved host order, comma-separated ByoHost names (the order in which Cluster API offers the
Machines: control planes oldest Machine first, then workers by the MachineSet's deletion order). The JSON files are
`kubectl get -o json` lists of the cluster's Machines, ByoMachines and ByoHosts (management) and Nodes (workload).
A ByoHost belongs to a Machine only through UIDs: spec.byoMachineRef -> ByoMachine -> its controller Machine.

next prints one decision and exits 0 only for APPROVE (the expected host waits for its approval):
  - every ByoHost attached to the cluster is in SEQUENCE exactly once, and nothing else is;
  - the expected host is the first one of SEQUENCE that is not done; every host before it is done (agent Succeeded
    at TARGET, Node at TARGET, Machine no longer updating) and no host after it has started;
  - exactly one Machine is updating in place, and it is the expected host's Machine;
  - its UpdateMachine answer says it waits for the approval of exactly this host and TARGET;
  - no other host carries an approval for TARGET that has not completed (it could start on its own).
Anything else is a hold (exit 2) with the reason; a deviation from SEQUENCE is a STOP (exit 3).
verify exits 0 when HOST is done and its Node is Ready.
"""
import json
import sys

APPROVAL = "inplace.openkubes.org/approved-upgrade"
IN_PROGRESS = "in-place-updates.internal.cluster.x-k8s.io/update-in-progress"


def load(path):
    with open(path) as f:
        return json.load(f).get("items", [])


def condition(obj, ctype):
    for c in (obj.get("status") or {}).get("conditions") or []:
        if c.get("type") == ctype:
            return c
    return {}


def owner_machine_uid(byomachine):
    for o in byomachine["metadata"].get("ownerReferences") or []:
        if o.get("kind") == "Machine" and o.get("controller"):
            return o.get("uid")
    return None


def hosts_view(machines, byomachines, byohosts, nodes):
    """{host: {...}} for every ByoHost attached (by UID) to a ByoMachine controlled by a Machine of the lists"""
    m_by_uid = {m["metadata"]["uid"]: m for m in machines}
    bm_by_uid = {b["metadata"]["uid"]: b for b in byomachines}
    n_by_name = {n["metadata"]["name"]: n for n in nodes}
    view = {}
    for h in byohosts:
        ref = (h.get("spec") or {}).get("byoMachineRef") or {}
        bm = bm_by_uid.get(ref.get("uid"))
        m = m_by_uid.get(owner_machine_uid(bm)) if bm and bm["metadata"]["name"] == ref.get("name") else None
        if m is None:
            continue
        node_name = ((m.get("status") or {}).get("nodeRef") or {}).get("name")
        n = n_by_name.get(node_name, {})
        req = (h.get("spec") or {}).get("inPlaceUpgrade") or {}
        st = (h.get("status") or {}).get("inPlaceUpgrade") or {}
        view[h["metadata"]["name"]] = {
            "machine": m["metadata"]["name"],
            "updating": IN_PROGRESS in (m["metadata"].get("annotations") or {}),
            "updating_message": condition(m, "Updating").get("message", ""),
            "approval": (h["metadata"].get("annotations") or {}).get(APPROVAL),
            "request_to": req.get("toVersion"), "request_id": req.get("id"),
            "phase": st.get("phase"), "step": st.get("step"),
            "observed_node": st.get("observedNodeVersion"), "observed_kubelet": st.get("observedKubeletVersion"),
            "node": node_name,
            "node_kubelet": ((n.get("status") or {}).get("nodeInfo") or {}).get("kubeletVersion"),
            "node_ready": condition(n, "Ready").get("status") == "True",
        }
    return view


def done(v, target):
    return (v["phase"] == "Succeeded" and v["request_to"] == target and v["observed_node"] == target
            and v["observed_kubelet"] == target and v["node_kubelet"] == target and not v["updating"])


def decide_next(view, target, sequence):
    """returns (code, decision, detail): 0 APPROVE, 2 HOLD, 3 STOP"""
    if len(set(sequence)) != len(sequence) or set(sequence) != set(view):
        return 3, "STOP", f"sequence {sequence} does not name every attached ByoHost exactly once ({sorted(view)})"
    pending = [h for h in sequence if not done(view[h], target)]
    if not pending:
        return 2, "COMPLETE", f"every host of the sequence is done at {target}"
    expected = pending[0]
    pos = sequence.index(expected)
    for h in sequence[pos + 1:]:
        v = view[h]
        if done(v, target) or v["request_to"] == target or v["phase"] == "Running":
            return 3, "STOP", f"{h} has started before {expected}: the order deviates from the approved sequence"
    stale = [h for h in sequence if h != expected and view[h]["approval"] == target and not done(view[h], target)]
    if stale:
        return 3, "STOP", f"open approval for {target} on {', '.join(stale)} (it could start without a decision): remove it first"
    updating = [h for h in sequence if view[h]["updating"]]
    if not updating:
        return 2, "HOLD", f"no Machine is updating in place yet (expected next: {expected}); version patch not applied or CAPI still reconciling"
    if updating != [expected]:
        return 3, "STOP", f"Cluster API updates {', '.join(updating)}, the approved sequence expects {expected}"
    v = view[expected]
    if v["approval"] == target:
        state = v["phase"] or ("requested" if v["request_to"] == target else "approved, not yet requested")
        return 2, "RUNNING", f"{expected} is approved ({state}, step {v['step'] or '-'}): monitor, then verify"
    want = f"waiting for the approval of the upgrade of ByoHost {expected} to {target}"
    if want not in v["updating_message"]:
        return 2, "HOLD", f"{expected} is updating but not waiting for an approval yet: {v['updating_message'] or 'no message'}"
    return 0, "APPROVE", f"{expected} (Machine {v['machine']}, Node {v['node']}) waits for its approval to {target}"


def decide_verify(view, target, host):
    if host not in view:
        return 3, "STOP", f"{host} is not an attached ByoHost of the cluster"
    v = view[host]
    if not done(v, target):
        return 2, "NOT-DONE", (f"{host}: phase={v['phase']} step={v['step']} request={v['request_to']} observed node/kubelet="
                               f"{v['observed_node']}/{v['observed_kubelet']} Node={v['node_kubelet']} updating={v['updating']}")
    if not v["node_ready"]:
        return 2, "NOT-READY", f"Node {v['node']} is not Ready"
    return 0, "VERIFIED", f"{host}: agent Succeeded, Node {v['node']} Ready at {target}, Machine {v['machine']} no longer updating"


def main(argv):
    if len(argv) != 7 or argv[0] not in ("next", "verify"):
        print(__doc__.strip().splitlines()[2].strip(), file=sys.stderr)
        return 1
    mode, machines, byomachines, byohosts, nodes, target, arg = argv
    view = hosts_view(load(machines), load(byomachines), load(byohosts), load(nodes))
    if mode == "next":
        code, decision, detail = decide_next(view, target, [x for x in arg.split(",") if x])
    else:
        code, decision, detail = decide_verify(view, target, arg)
    for name in sorted(view):
        v = view[name]
        print(f"  {name:10} machine={v['machine']} node={v['node_kubelet']} ready={v['node_ready']} updating={v['updating']} "
              f"approval={v['approval'] or '-'} request={v['request_to'] or '-'} phase={v['phase'] or '-'}")
    print(f"{decision}: {detail}")
    return code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
