// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The grants rendered by hack/inplace/render_grant.py (testdata, kept equal to
// the renderer by hack/inplace/test/test_render_grant.py) must be accepted by
// the guard exactly as the approver would create them, and must authorize the
// in-place transitions of their target kind only.
func TestRenderedGrantsAreAccepted(t *testing.T) {
	cases := []struct {
		file   string
		kind   TargetKind
		target string
		own    []string
		other  string
	}{
		{"rendered-grant-kcp.json", TargetKubeadmControlPlane, "11111111-2222-3333-4444-555555555555",
			[]string{TransitionKCPSetInProgress, TransitionKCPVersionBump, TransitionKCPPendingHook, TransitionKCPCleanupHook,
				TransitionKCPCleanupFlag, TransitionKCPInfraInProgress, TransitionKCPInfraCleanup}, TransitionMDMove},
		{"rendered-grant-md.json", TargetMachineDeployment, "66666666-7777-8888-9999-000000000000",
			[]string{TransitionMDMove, TransitionMDFinalizeMachine, TransitionMDTrigger, TransitionMDCleanupHook,
				TransitionMDCleanupFlag, TransitionMDFinalizeInfra, TransitionMDAcknowledge}, TransitionKCPVersionBump},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + c.file)
			if err != nil {
				t.Fatal(err)
			}
			var cm struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
				Data map[string]string `json:"data"`
			}
			if err = json.Unmarshal(raw, &cm); err != nil {
				t.Fatal(err)
			}
			if d := EvaluateGrantConfigMapGuarded(Create, ActorApprover, nil, cm.Metadata.Labels, nil, cm.Data); !d.Allowed {
				t.Fatalf("approver CREATE denied: %s", d.Reason)
			}
			if d := EvaluateGrantConfigMapGuarded(Create, ActorInitiator, nil, cm.Metadata.Labels, nil, cm.Data); d.Allowed {
				t.Fatal("initiator CREATE admitted")
			}
			g, err := ParseGrant(cm.Data)
			if err != nil {
				t.Fatalf("ParseGrant: %v", err)
			}
			scope := Scope{Verified: true, Protected: true}
			q := GrantQuery{DryRun: true, ClusterUID: "414317b7-77d4-4bd0-b86e-5cbe38b6c2c7", TargetKind: c.kind,
				TargetUID: c.target, VersionRelevant: true, FromVersion: "v1.36.6", ToVersion: "v1.37.2",
				Now: g.NotAfter.Add(-time.Hour)}
			for _, id := range c.own {
				q.Transition = id
				if d := VerifyGrant(scope, g, q); !d.Allowed {
					t.Errorf("transition %s denied: %s", id, d.Reason)
				}
			}
			q.Transition = c.other
			if d := VerifyGrant(scope, g, q); d.Allowed {
				t.Errorf("transition %s of the other target kind admitted", c.other)
			}
			q.Transition, q.DryRun = c.own[0], false
			if d := VerifyGrant(scope, g, q); d.Allowed {
				t.Error("an unclaimed grant admitted a non-dry-run change")
			}
			q.DryRun, q.Now = true, g.NotAfter
			if d := VerifyGrant(scope, g, q); d.Allowed {
				t.Error("an expired grant admitted a change")
			}
		})
	}
}
