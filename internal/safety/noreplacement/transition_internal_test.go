// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"testing"
	"time"
)

func sameOwner() (from, to OwnerControllerRef) {
	o := OwnerControllerRef{Kind: "MachineSet", Name: "ms-a", UID: "ms-uid-a"}
	return o, o
}

func versionBumpChange() MachineChange {
	from, to := sameOwner()
	return MachineChange{
		VersionFrom:         "v1.33.4",
		VersionTo:           "v1.34.1",
		OwnerControllerFrom: from,
		OwnerControllerTo:   to,
	}
}

func addInProgressChange() MachineChange {
	from, to := sameOwner()
	return MachineChange{
		VersionFrom: "v1.33.4", VersionTo: "v1.33.4",
		OwnerControllerFrom: from, OwnerControllerTo: to,
		UpdateInProgress: AnnoAdded,
	}
}

func moveChange() MachineChange {
	return MachineChange{
		VersionFrom: "v1.33.4", VersionTo: "v1.33.4",
		OwnerControllerFrom:  OwnerControllerRef{Kind: "MachineSet", Name: "ms-old", UID: "ms-uid-old"},
		OwnerControllerTo:    OwnerControllerRef{Kind: "MachineSet", Name: "ms-new", UID: "ms-uid-new"},
		MDUniqueLabelChanged: true,
		UpdateInProgress:     AnnoAdded,
		PendingAckMove:       AnnoAdded,
	}
}

func TestClassify_Shapes(t *testing.T) {
	from, to := sameOwner()
	tests := []struct {
		name string
		in   MachineChange
		want Shape
	}{
		{"version bump", versionBumpChange(), ShapeVersionBump},
		{"add in-progress", addInProgressChange(), ShapeAddInProgress},
		{"remove in-progress", MachineChange{OwnerControllerFrom: from, OwnerControllerTo: to, UpdateInProgress: AnnoRemoved}, ShapeRemoveInProgress},
		{"add pending-hook (KCP-3)", MachineChange{OwnerControllerFrom: from, OwnerControllerTo: to, PendingHookUpdateMachine: AnnoAdded}, ShapeAddPendingHook},
		{"add pending-hook + drop ack (MD-TRIG)", MachineChange{OwnerControllerFrom: from, OwnerControllerTo: to, PendingHookUpdateMachine: AnnoAdded, PendingAckMove: AnnoRemoved}, ShapeAddPendingHook},
		{"remove pending-hook", MachineChange{OwnerControllerFrom: from, OwnerControllerTo: to, PendingHookUpdateMachine: AnnoRemoved}, ShapeRemovePendingHook},
		{"move", moveChange(), ShapeMove},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyMachineTransition(tc.in); got != tc.want {
				t.Fatalf("shape = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestClassify_FailClosed(t *testing.T) {
	mut := []struct {
		name string
		mut  func(*MachineChange)
	}{
		{"identity changed", func(c *MachineChange) { c.IdentityChanged = true }},
		{"finalizers changed", func(c *MachineChange) { c.FinalizersChanged = true }},
		{"cluster-name label changed", func(c *MachineChange) { c.ClusterNameLabelChanged = true }},
		{"protection annotation changed", func(c *MachineChange) { c.ProtectionAnnotationChanged = true }},
		{"other reserved metadata changed", func(c *MachineChange) { c.OtherReservedMetadataChanged = true }},
		{"ownerRefs beyond controller", func(c *MachineChange) { c.OwnerRefsChangedBeyondController = true }},
		{"version bump + extra spec", func(c *MachineChange) { c.SpecChangedBeyondVersion = true }},
		{"version bump + md label", func(c *MachineChange) { c.MDUniqueLabelChanged = true }},
	}
	for _, tc := range mut {
		t.Run(tc.name, func(t *testing.T) {
			c := versionBumpChange()
			tc.mut(&c)
			if got := ClassifyMachineTransition(c); got != ShapeNone {
				t.Fatalf("expected ShapeNone for %q, got %d", tc.name, got)
			}
		})
	}
	// Move missing the md label is not a move.
	t.Run("move without md label", func(t *testing.T) {
		c := moveChange()
		c.MDUniqueLabelChanged = false
		if ClassifyMachineTransition(c) != ShapeNone {
			t.Fatalf("move without md-label must be ShapeNone")
		}
	})
}

func TestTransitionIDForShape(t *testing.T) {
	cases := []struct {
		shape Shape
		kind  TargetKind
		want  string
	}{
		{ShapeVersionBump, TargetKubeadmControlPlane, TransitionKCPVersionBump},
		{ShapeVersionBump, TargetMachineDeployment, TransitionMDFinalizeMachine},
		{ShapeMove, TargetMachineDeployment, TransitionMDMove},
		{ShapeMove, TargetKubeadmControlPlane, ""},        // KCP has no move
		{ShapeAddInProgress, TargetMachineDeployment, ""}, // MS adds in-progress only via move
		{ShapeAddInProgress, TargetKubeadmControlPlane, TransitionKCPSetInProgress},
	}
	for _, c := range cases {
		if got := TransitionIDForShape(c.shape, c.kind); got != c.want {
			t.Fatalf("TransitionIDForShape(%d,%s)=%q want %q", c.shape, c.kind, got, c.want)
		}
	}
}

func TestEvaluateMachineUpdateInPlace(t *testing.T) {
	now := time.Now()
	// claimed KCP grant
	kcp := claimedKCPGrant(t)

	t.Run("valid claimed version bump -> allow", func(t *testing.T) {
		d := EvaluateMachineUpdateInPlace(protectedScope(), versionBumpChangePtr(), kcp, "cluster-uid-1", false, now)
		if !d.Allowed {
			t.Fatalf("expected allow, got deny: %s", d.Reason)
		}
	})
	t.Run("unclaimed real write -> deny", func(t *testing.T) {
		unc, _ := ParseGrant(validKCPGrantData())
		d := EvaluateMachineUpdateInPlace(protectedScope(), versionBumpChangePtr(), unc, "cluster-uid-1", false, now)
		if d.Allowed {
			t.Fatalf("unclaimed real write must deny")
		}
	})
	t.Run("unclaimed dry-run -> allow", func(t *testing.T) {
		unc, _ := ParseGrant(validKCPGrantData())
		d := EvaluateMachineUpdateInPlace(protectedScope(), versionBumpChangePtr(), unc, "cluster-uid-1", true, now)
		if !d.Allowed {
			t.Fatalf("dry-run probe must allow without claim: %s", d.Reason)
		}
	})
	t.Run("shape none -> deny", func(t *testing.T) {
		c := versionBumpChange()
		c.IdentityChanged = true
		d := EvaluateMachineUpdateInPlace(protectedScope(), &c, kcp, "cluster-uid-1", false, now)
		if d.Allowed {
			t.Fatalf("unrecognized shape must deny")
		}
	})
	t.Run("version mismatch vs grant -> deny", func(t *testing.T) {
		c := versionBumpChange()
		c.VersionTo = "v1.99.9"
		d := EvaluateMachineUpdateInPlace(protectedScope(), &c, kcp, "cluster-uid-1", false, now)
		if d.Allowed {
			t.Fatalf("version mismatch must deny")
		}
	})
	t.Run("MD move with claimed MD grant -> allow", func(t *testing.T) {
		d := validMDGrantData()
		d[GrantClaimKey] = "c"
		d[grantKeyTargetUID] = "md-uid-1"
		mdGrant, err := ParseGrant(d)
		if err != nil {
			t.Fatalf("setup md grant: %v", err)
		}
		dec := EvaluateMachineUpdateInPlace(protectedScope(), moveChangePtr(), mdGrant, "cluster-uid-1", false, now)
		if !dec.Allowed {
			t.Fatalf("MD move under MD grant must allow: %s", dec.Reason)
		}
	})
	t.Run("KCP grant cannot authorize a move -> deny", func(t *testing.T) {
		d := EvaluateMachineUpdateInPlace(protectedScope(), moveChangePtr(), kcp, "cluster-uid-1", false, now)
		if d.Allowed {
			t.Fatalf("a KCP grant must not authorize an MD move")
		}
	})
}

func versionBumpChangePtr() *MachineChange {
	c := versionBumpChange()
	return &c
}

func moveChangePtr() *MachineChange {
	c := moveChange()
	return &c
}
