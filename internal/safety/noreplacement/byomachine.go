// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

// ByoMachine (the BYOH InfraMachine) is DENY-BY-DEFAULT like the Machine. During
// in-place CAPI writes labels=nil + {clonedFrom..., update-in-progress} on the
// InfraMachine (KCP-IM / MD-FIN-IM) and later removes update-in-progress
// (cleanup). The security-critical invariants are that the ByoMachine spec, owner
// references and finalizers never change (that would permit host rebind,
// re-provision or detach), and identity is preserved. Metadata churn that CAPI
// performs (labels, clonedFrom annotations) is benign and allowed, but only in
// combination with a valid grant and a recognized in-place annotation toggle.

// ByoMachineChange is the CAPI-free view of an old->new ByoMachine diff.
type ByoMachineChange struct {
	IdentityChanged   bool
	SpecChanged       bool
	OwnerRefsChanged  bool
	FinalizersChanged bool
	UpdateInProgress  AnnoDelta // update-in-progress added (set) or removed (cleanup)
}

// classifyByoMachine returns the in-place shape for a ByoMachine diff, or ShapeNone.
// Only the update-in-progress toggle is recognized; spec/owner/finalizer/identity
// must be unchanged.
func classifyByoMachine(c ByoMachineChange) Shape {
	if c.IdentityChanged || c.SpecChanged || c.OwnerRefsChanged || c.FinalizersChanged {
		return ShapeNone
	}
	switch c.UpdateInProgress {
	case AnnoAdded:
		return ShapeAddInProgress
	case AnnoRemoved:
		return ShapeRemoveInProgress
	default:
		return ShapeNone
	}
}

// byoMachineTransitionID maps a ByoMachine in-place shape to the grant transition
// ID for the target kind.
func byoMachineTransitionID(shape Shape, kind TargetKind) string {
	switch kind {
	case TargetKubeadmControlPlane:
		switch shape {
		case ShapeAddInProgress:
			return TransitionKCPInfraInProgress
		case ShapeRemoveInProgress:
			return TransitionKCPInfraCleanup
		}
	case TargetMachineDeployment:
		switch shape {
		case ShapeAddInProgress:
			return TransitionMDFinalizeInfra
		case ShapeRemoveInProgress:
			// cleanup on the InfraMachine shares the MD cleanup grant transition.
			return TransitionMDFinalizeInfra
		}
	}
	return ""
}

// EvaluateByoMachineInPlace authorizes a protected ByoMachine UPDATE under a grant.
func EvaluateByoMachineInPlace(
	scope Scope,
	change ByoMachineChange,
	grant *Grant,
	q *GrantQuery,
) Decision {

	if !scope.Verified {
		return deny(reasonScopeUnverified)
	}
	if !scope.Protected {
		return allow()
	}
	shape := classifyByoMachine(change)
	if shape == ShapeNone {
		return deny("ByoMachine change is not a recognized in-place transition")
	}
	if grant == nil {
		return deny("ByoMachine in-place update requires an authorized upgrade grant")
	}
	id := byoMachineTransitionID(shape, grant.TargetKind)
	if id == "" {
		return deny("ByoMachine transition is not valid for the grant target kind")
	}
	qq := *q
	qq.Transition = id
	qq.VersionRelevant = false
	return VerifyGrant(scope, grant, qq)
}
