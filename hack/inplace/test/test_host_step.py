#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Offline tests of host_step.py: python3 -I hack/inplace/test/test_host_step.py"""
import importlib.util
import os
import unittest

spec = importlib.util.spec_from_file_location("hs", os.path.join(os.path.dirname(__file__), "..", "host_step.py"))
hs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hs)

FROM, TO = "v1.36.6", "v1.37.2"
HOSTS = {"ha1-cp-1": "m-cp1", "ha1-cp-3": "m-cp3", "ha1-cp-2": "m-cp2", "ha1-w-1": "m-w1", "ha1-w-2": "m-w2"}
SEQ = ["ha1-cp-1", "ha1-cp-3", "ha1-cp-2", "ha1-w-1", "ha1-w-2"]
WAIT = "In-place update in progress: waiting for the approval of the upgrade of ByoHost {} to {} (annotation {})"


class Cluster:
    def __init__(self):
        self.machines, self.byomachines, self.hosts, self.nodes = {}, {}, {}, {}
        for host, machine in HOSTS.items():
            self.machines[host] = {"metadata": {"name": machine, "uid": "u-" + machine, "annotations": {}},
                                   "status": {"nodeRef": {"name": host}, "conditions": []}}
            self.byomachines[host] = {"metadata": {"name": machine, "uid": "b-" + machine, "ownerReferences": [
                {"kind": "Machine", "name": machine, "uid": "u-" + machine, "controller": True}]}}
            self.hosts[host] = {"metadata": {"name": host, "annotations": {}},
                                "spec": {"byoMachineRef": {"name": machine, "uid": "b-" + machine}}, "status": {}}
            self.nodes[host] = {"metadata": {"name": host},
                                "status": {"nodeInfo": {"kubeletVersion": FROM}, "conditions": [{"type": "Ready", "status": "True"}]}}

    def updating(self, host, waiting=True):
        m = self.machines[host]
        m["metadata"]["annotations"][hs.IN_PROGRESS] = ""
        if waiting:
            m["status"]["conditions"] = [{"type": "Updating", "message": WAIT.format(host, TO, hs.APPROVAL)}]

    def approve(self, host):
        self.hosts[host]["metadata"]["annotations"][hs.APPROVAL] = TO

    def finish(self, host):
        self.approve(host)
        self.machines[host]["metadata"]["annotations"].pop(hs.IN_PROGRESS, None)
        self.machines[host]["status"]["conditions"] = []
        self.hosts[host]["spec"]["inPlaceUpgrade"] = {"toVersion": TO, "id": "x"}
        self.hosts[host]["status"]["inPlaceUpgrade"] = {"phase": "Succeeded", "observedNodeVersion": TO, "observedKubeletVersion": TO}
        self.nodes[host]["status"]["nodeInfo"]["kubeletVersion"] = TO

    def view(self):
        return hs.hosts_view(list(self.machines.values()), list(self.byomachines.values()), list(self.hosts.values()),
                             list(self.nodes.values()))

    def next(self, seq=SEQ):
        return hs.decide_next(self.view(), TO, seq)


class Next(unittest.TestCase):
    def test_full_sequence_one_host_at_a_time(self):
        c = Cluster()
        for host in SEQ:
            self.assertEqual(c.next()[:2], (2, "HOLD"), f"before CAPI selects {host}")
            c.updating(host)
            code, decision, detail = c.next()
            self.assertEqual((code, decision), (0, "APPROVE"), detail)
            self.assertIn(host, detail)
            c.approve(host)
            self.assertEqual(c.next()[:2], (2, "RUNNING"))
            c.finish(host)
            self.assertEqual(hs.decide_verify(c.view(), TO, host)[:2], (0, "VERIFIED"))
        self.assertEqual(c.next()[:2], (2, "COMPLETE"))

    def test_capi_selects_another_host(self):
        c = Cluster()
        c.updating("ha1-cp-2")   # the requested cp-1, cp-2, cp-3 order is not what CAPI does
        self.assertEqual(c.next()[:2], (3, "STOP"))

    def test_host_started_out_of_order(self):
        c = Cluster()
        c.finish("ha1-cp-3")
        c.updating("ha1-cp-1")
        self.assertEqual(c.next()[:2], (3, "STOP"))

    def test_stale_approval_on_another_host(self):
        c = Cluster()
        c.updating("ha1-cp-1")
        c.approve("ha1-cp-3")    # pre-approved: would start without a decision
        code, decision, detail = c.next()
        self.assertEqual((code, decision), (3, "STOP"), detail)
        self.assertIn("ha1-cp-3", detail)

    def test_two_machines_updating(self):
        c = Cluster()
        c.updating("ha1-cp-1")
        c.updating("ha1-cp-3")
        self.assertEqual(c.next()[:2], (3, "STOP"))

    def test_updating_but_not_waiting_for_approval(self):
        c = Cluster()
        c.finish("ha1-cp-1"), c.finish("ha1-cp-3"), c.finish("ha1-cp-2")
        c.updating("ha1-w-1", waiting=False)
        c.machines["ha1-w-1"]["status"]["conditions"] = [{"type": "Updating", "message": "In-place update in progress: draining Node ha1-w-1"}]
        self.assertEqual(c.next()[:2], (2, "HOLD"))

    def test_approval_for_another_host_name_in_message(self):
        c = Cluster()
        c.updating("ha1-cp-1", waiting=False)
        c.machines["ha1-cp-1"]["status"]["conditions"] = [{"type": "Updating", "message": WAIT.format("ha1-cp-3", TO, hs.APPROVAL)}]
        self.assertEqual(c.next()[:2], (2, "HOLD"))

    def test_sequence_must_cover_every_host(self):
        c = Cluster()
        self.assertEqual(c.next(SEQ[:4])[:2], (3, "STOP"))
        self.assertEqual(c.next(SEQ + ["ha1-cp-1"])[:2], (3, "STOP"))
        self.assertEqual(c.next(SEQ[:4] + ["ha1-w-9"])[:2], (3, "STOP"))


class Verify(unittest.TestCase):
    def test_not_done_until_agent_node_and_machine_agree(self):
        mutations = {
            "Node still at the old version": lambda c: c.nodes["ha1-cp-1"]["status"]["nodeInfo"].update(kubeletVersion=FROM),
            "agent still running": lambda c: c.hosts["ha1-cp-1"]["status"]["inPlaceUpgrade"].update(phase="Running"),
            "Machine still updating": lambda c: c.machines["ha1-cp-1"]["metadata"]["annotations"].update({hs.IN_PROGRESS: ""}),
        }
        for name, mutate in mutations.items():
            c = Cluster()
            c.finish("ha1-cp-1")
            mutate(c)
            self.assertNotEqual(hs.decide_verify(c.view(), TO, "ha1-cp-1")[0], 0, name)

    def test_node_not_ready(self):
        c = Cluster()
        c.finish("ha1-w-1")
        c.nodes["ha1-w-1"]["status"]["conditions"] = [{"type": "Ready", "status": "False"}]
        self.assertEqual(hs.decide_verify(c.view(), TO, "ha1-w-1")[:2], (2, "NOT-READY"))

    def test_attachment_only_by_uid(self):
        c = Cluster()
        c.hosts["ha1-w-2"]["spec"]["byoMachineRef"]["uid"] = "b-other"      # same name, other ByoMachine
        c.byomachines["ha1-w-1"]["metadata"]["ownerReferences"][0]["controller"] = False
        self.assertEqual(set(c.view()), set(HOSTS) - {"ha1-w-1", "ha1-w-2"})
        self.assertEqual(c.next()[:2], (3, "STOP"))

    def test_unknown_host(self):
        self.assertEqual(hs.decide_verify(Cluster().view(), TO, "ha1-w-9")[0], 3)


if __name__ == "__main__":
    unittest.main()
