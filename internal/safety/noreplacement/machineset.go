// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

// MachineSet admission is ALLOW-BY-DEFAULT (M3 security finding, proven against
// cluster-api v1.14.3): the in-place MD move legitimately scales spec.replicas on
// the source/target MachineSets and creates the target MS, and every path to
// actual machine replacement or an unauthorized version change terminates at a
// Machine-level gate (Machine DELETE/CREATE deny + grant-gated Machine version
// write/move). A deny-all MachineSet webhook with failurePolicy: Fail would break
// in-place upgrades. So the ONLY MachineSet-specific capability we gate is the
// pair of move-authorization annotations; everything else is allowed.

// Move-authorization annotations on a MachineSet (CAPI v1.14.3).
const (
	// The keys of cluster-api v1.14.3 (api/core/v1beta2/machineset_types.go):
	// AcknowledgedMoveAnnotation on the target, and the pair that routes a
	// move, MachineSetReceiveMachinesFromMachineSetsAnnotation on the target
	// and MachineSetMoveMachinesToMachineSetAnnotation on the source.
	msAcknowledgedMoveAnnotation    = "in-place-updates.internal.cluster.x-k8s.io/acknowledged-move"
	msReceiveMachinesFromAnnotation = "in-place-updates.internal.cluster.x-k8s.io/receive-machines-from-machinesets"
	msMoveMachinesToAnnotation      = "in-place-updates.internal.cluster.x-k8s.io/move-machines-to-machineset"
	machineDeploymentNameLabel      = "cluster.x-k8s.io/deployment-name"
)

// MoveAnnotationTouched reports whether either move-authorization annotation was
// added, removed or changed between old and new MachineSet annotation maps.
func MoveAnnotationTouched(oldAnns, newAnns map[string]string) bool {
	for _, k := range []string{msAcknowledgedMoveAnnotation, msReceiveMachinesFromAnnotation, msMoveMachinesToAnnotation} {
		ov, oOK := oldAnns[k]
		nv, nOK := newAnns[k]
		if oOK != nOK || ov != nv {
			return true
		}
	}
	return false
}

// EvaluateMachineSetInPlace authorizes a protected MachineSet UPDATE.
//
// Allow-by-default: if no move-authorization annotation is touched, the change
// is allowed (replicas, template, status, ownerRefs, other metadata). If a
// move-authorization annotation IS touched, it requires a valid MD grant
// authorizing the MD-ACK transition and binding this MachineSet to the grant's
// MachineDeployment (the handler sets q.Transition=MD-ACK, q.TargetKind=
// MachineDeployment, q.TargetUID=<owning MD UID>, and has verified this MS is the
// grant's approved target). CREATE/DELETE of a MachineSet is handled separately
// (allowed; the resulting Machine CREATE/DELETE is the real gate).
func EvaluateMachineSetInPlace(
	scope Scope,
	moveAnnotationTouched bool,
	grant *Grant,
	q *GrantQuery,
) Decision {

	if !scope.Verified {
		return deny(reasonScopeUnverified)
	}
	if !scope.Protected {
		return allow()
	}
	if !moveAnnotationTouched {
		return allow()
	}
	if q.Transition != TransitionMDAcknowledge {
		return deny("move-authorization annotation requires the MD-ACK grant transition")
	}
	return VerifyGrant(scope, grant, *q)
}
