// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"testing"
	"time"
)

// ---------- MachineSet allow-by-default ----------

func mdGrantWithAck(t *testing.T) *Grant {
	t.Helper()
	d := validMDGrantData()
	d[grantKeyTransitions] = "MD-MOVE,MD-FIN-M,MD-TRIG,MD-C1,MD-C2,MD-FIN-IM,MD-ACK"
	d[GrantClaimKey] = "c"
	g, err := ParseGrant(d)
	if err != nil {
		t.Fatalf("setup md grant: %v", err)
	}
	return g
}

func msAckQuery() GrantQuery {
	return GrantQuery{
		ClusterUID: "cluster-uid-1",
		TargetKind: TargetMachineDeployment,
		TargetUID:  "md-uid-1",
		Transition: TransitionMDAcknowledge,
		Now:        time.Now(),
	}
}

func TestMoveAnnotationTouched(t *testing.T) {
	none := MoveAnnotationTouched(map[string]string{"x": "1"}, map[string]string{"x": "1"})
	if none {
		t.Fatalf("unrelated annotation must not count as move-touched")
	}
	added := MoveAnnotationTouched(map[string]string{}, map[string]string{msAcknowledgedMoveAnnotation: "m1"})
	if !added {
		t.Fatalf("adding acknowledged-move must count")
	}
}

func TestMachineSet_AllowByDefault(t *testing.T) {
	// No move annotation touched -> allow even under protected scope and no grant.
	if d := EvaluateMachineSetInPlace(protectedScope(), false, nil, &GrantQuery{}); !d.Allowed {
		t.Fatalf("replicas/template/status changes must be allowed: %s", d.Reason)
	}
	// Unprotected scope -> allow.
	if d := EvaluateMachineSetInPlace(Scope{Verified: true}, true, nil, &GrantQuery{}); !d.Allowed {
		t.Fatalf("unprotected namespace must allow")
	}
}

func TestMachineSet_MoveAnnotationGated(t *testing.T) {
	// Touched + valid MD grant authorizing MD-ACK -> allow.
	if d := EvaluateMachineSetInPlace(protectedScope(), true, mdGrantWithAck(t), msAckQueryPtr()); !d.Allowed {
		t.Fatalf("valid MD-ACK grant must allow the move annotation: %s", d.Reason)
	}
	// Touched + no grant -> deny.
	if d := EvaluateMachineSetInPlace(protectedScope(), true, nil, msAckQueryPtr()); d.Allowed {
		t.Fatalf("move annotation without a grant must deny")
	}
	// Touched + wrong transition -> deny.
	q := msAckQuery()
	q.Transition = TransitionMDMove
	if d := EvaluateMachineSetInPlace(protectedScope(), true, mdGrantWithAck(t), &q); d.Allowed {
		t.Fatalf("non-MD-ACK transition must deny the move annotation")
	}
}

// ---------- ByoMachine deny-by-default ----------

func TestByoMachine_InPlace(t *testing.T) {
	kcp := claimedKCPGrant(t)
	q := GrantQuery{ClusterUID: "cluster-uid-1", TargetKind: TargetKubeadmControlPlane, TargetUID: "kcp-uid-1", MachineUID: "machine-uid-1", Now: time.Now()}

	// add update-in-progress -> allow
	if d := EvaluateByoMachineInPlace(protectedScope(), ByoMachineChange{UpdateInProgress: AnnoAdded}, kcp, &q); !d.Allowed {
		t.Fatalf("add in-progress on InfraMachine must allow: %s", d.Reason)
	}
	// remove update-in-progress (cleanup) -> allow
	if d := EvaluateByoMachineInPlace(protectedScope(), ByoMachineChange{UpdateInProgress: AnnoRemoved}, kcp, &q); !d.Allowed {
		t.Fatalf("cleanup on InfraMachine must allow: %s", d.Reason)
	}
	// spec changed -> deny (rebind/reprovision)
	if d := EvaluateByoMachineInPlace(protectedScope(), ByoMachineChange{SpecChanged: true, UpdateInProgress: AnnoAdded}, kcp, &q); d.Allowed {
		t.Fatalf("ByoMachine spec change must deny")
	}
	// owner change -> deny
	if d := EvaluateByoMachineInPlace(protectedScope(), ByoMachineChange{OwnerRefsChanged: true, UpdateInProgress: AnnoAdded}, kcp, &q); d.Allowed {
		t.Fatalf("ByoMachine owner change must deny")
	}
	// no grant -> deny
	if d := EvaluateByoMachineInPlace(protectedScope(), ByoMachineChange{UpdateInProgress: AnnoAdded}, nil, &q); d.Allowed {
		t.Fatalf("ByoMachine in-place without a grant must deny")
	}
	// no recognized toggle -> deny
	if d := EvaluateByoMachineInPlace(protectedScope(), ByoMachineChange{}, kcp, &q); d.Allowed {
		t.Fatalf("no in-place toggle must deny")
	}
}

// ---------- Grant-ConfigMap name recognition + label-removal ----------

func TestGrantSelect_NameAndLabel(t *testing.T) {
	if !IsGrantObjectName("okx-grant-abc") {
		t.Fatalf("name prefix must be recognized")
	}
	if IsGrantObjectName("random-cm") {
		t.Fatalf("non-grant name must not be recognized")
	}
	// by name even without the label
	if !IsGrantObject("okx-grant-abc", nil) {
		t.Fatalf("grant by name must be selected")
	}
	// by label even without the name
	if !IsGrantObject("random-cm", map[string]string{GrantLabel: "true"}) {
		t.Fatalf("grant by label must be selected")
	}
	// neither
	if IsGrantObject("random-cm", map[string]string{"x": "y"}) {
		t.Fatalf("ordinary ConfigMap must not be selected")
	}
}

func TestGrantSelect_LabelRemovalDenied(t *testing.T) {
	oldL := map[string]string{GrantLabel: "true"}
	newL := map[string]string{} // label removed
	// even an otherwise-valid claim is denied if it also removes the grant label
	claimed := validKCPGrantData()
	claimed[GrantClaimKey] = "c"
	d := EvaluateGrantConfigMapGuarded(Update, ActorInitiator, oldL, newL, validKCPGrantData(), claimed)
	if d.Allowed {
		t.Fatalf("removing the grant label must deny")
	}
	// label kept -> delegates to EvaluateGrantConfigMap (valid claim allowed)
	d = EvaluateGrantConfigMapGuarded(Update, ActorInitiator, oldL, oldL, validKCPGrantData(), claimed)
	if !d.Allowed {
		t.Fatalf("keeping the label + valid claim must allow: %s", d.Reason)
	}
}

func msAckQueryPtr() *GrantQuery {
	q := msAckQuery()
	return &q
}
