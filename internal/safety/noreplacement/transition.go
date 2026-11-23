// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import "time"

// This file is the PURE, dependency-free transition matcher. It classifies an
// old->new Machine diff into exactly one enumerated in-place transition SHAPE
// (or none), strictly, so the guard can authorize only the writes CAPI v1.14.3
// actually performs during in-place (see the M1 matrix). The CAPI-typed
// extractor that fills MachineChange from clusterv1.Machine objects lives in the
// admission adapter and is built/integration-tested on the Mac / in CI (PENDING).

// AnnoDelta describes how a single annotation (or an entry in a comma-separated
// annotation such as pending-hooks) changed between old and new.
type AnnoDelta int

const (
	AnnoUnchanged AnnoDelta = iota
	AnnoAdded
	AnnoRemoved
	AnnoChanged
)

// OwnerControllerRef is the controller ownerReference identity (kind/name/uid).
type OwnerControllerRef struct {
	Kind string
	Name string
	UID  string
}

// MachineChange is the CAPI-free view of an old->new Machine diff required to
// classify an in-place transition. Every bool defaults to the safe (no-change)
// value, so an incompletely-populated change can never widen what is allowed.
type MachineChange struct {
	IdentityChanged bool // uid/name/namespace differ -> never an in-place transition

	VersionFrom              string
	VersionTo                string
	SpecChangedBeyondVersion bool // any spec field other than version differs

	OwnerControllerFrom              OwnerControllerRef
	OwnerControllerTo                OwnerControllerRef
	OwnerRefsChangedBeyondController bool
	FinalizersChanged                bool

	UpdateInProgress         AnnoDelta // in-place-updates.internal.cluster.x-k8s.io/update-in-progress
	PendingAckMove           AnnoDelta // .../pending-acknowledge-move
	PendingHookUpdateMachine AnnoDelta // runtime.cluster.x-k8s.io/pending-hooks contains UpdateMachine

	MDUniqueLabelChanged bool // machine-template-hash
	// SetNameLabelChanged: cluster.x-k8s.io/set-name differs. After an MD-MOVE the
	// target MachineSet re-aligns this label to itself (syncMachines and
	// completeMoveMachine, CAPI v1.14.3). Permitted ONLY when the new value equals
	// the (unchanged) controller MachineSet name (SetNameMatchesController).
	SetNameLabelChanged          bool
	SetNameMatchesController     bool
	ClusterNameLabelChanged      bool
	ProtectionAnnotationChanged  bool
	OtherReservedMetadataChanged bool // any OTHER reserved key changed
}

// Shape is a canonical in-place transition shape, independent of KCP vs MD.
type Shape int

const (
	ShapeNone Shape = iota
	ShapeAddInProgress
	ShapeRemoveInProgress
	ShapeAddPendingHook
	ShapeRemovePendingHook
	ShapeVersionBump
	ShapeMove
	// ShapeMoveAlign: metadata-only tail of an MD-MOVE — the target MachineSet
	// re-aligns cluster.x-k8s.io/set-name to itself; nothing else in the
	// classified change set moves. Authorized by the MD-MOVE transition.
	ShapeMoveAlign
)

func (c *MachineChange) versionChanged() bool { return c.VersionFrom != c.VersionTo }

func (c *MachineChange) ownerControllerMoved() bool {
	return c.OwnerControllerFrom.Kind == string(TargetMachineSet) &&
		c.OwnerControllerTo.Kind == string(TargetMachineSet) &&
		c.OwnerControllerFrom.UID != "" &&
		c.OwnerControllerTo.UID != "" &&
		c.OwnerControllerFrom.UID != c.OwnerControllerTo.UID
}

func (c *MachineChange) ownerControllerUnchanged() bool {
	return c.OwnerControllerFrom == c.OwnerControllerTo
}

// ClassifyMachineTransition returns the single in-place shape the diff matches,
// or ShapeNone. It is intentionally strict and fail-closed: any identity,
// finalizer, cluster-name-label, protection-annotation or other-reserved-metadata
// change, or any unexpected combination, yields ShapeNone (-> deny).
//
//nolint:gocyclo,gocritic // the source-verified transition matrix (M1 §3) as one fail-closed decision table, on a copy of the change.
func ClassifyMachineTransition(c MachineChange) Shape {
	// Hard invariants: these must NEVER change during any in-place transition.
	if c.IdentityChanged || c.FinalizersChanged ||
		c.ClusterNameLabelChanged || c.ProtectionAnnotationChanged ||
		c.OtherReservedMetadataChanged || c.OwnerRefsChangedBeyondController {
		return ShapeNone
	}
	// set-name may only ever follow the controller MachineSet, never point elsewhere.
	if c.SetNameLabelChanged && !c.SetNameMatchesController {
		return ShapeNone
	}

	// MD-MOVE: single patch — controller ownerRef source-MS->target-MS, md unique
	// label change, add update-in-progress + pending-acknowledge-move. No version,
	// no other spec change, no pending-hooks.
	if c.ownerControllerMoved() {
		if !c.versionChanged() && !c.SpecChangedBeyondVersion &&
			c.MDUniqueLabelChanged &&
			c.UpdateInProgress == AnnoAdded && c.PendingAckMove == AnnoAdded &&
			c.PendingHookUpdateMachine == AnnoUnchanged {
			return ShapeMove
		}
		return ShapeNone
	}

	// Beyond MD-MOVE every transition keeps the controller ownerRef and md label.
	if !c.ownerControllerUnchanged() || c.MDUniqueLabelChanged {
		return ShapeNone
	}

	// Version bump: only spec.version changes; no in-place annotations toggle here.
	if c.versionChanged() {
		if !c.SpecChangedBeyondVersion &&
			c.UpdateInProgress == AnnoUnchanged &&
			c.PendingAckMove == AnnoUnchanged &&
			c.PendingHookUpdateMachine == AnnoUnchanged {
			return ShapeVersionBump
		}
		return ShapeNone
	}

	// From here on spec (incl. version) is unchanged: pure metadata transitions.
	if c.SpecChangedBeyondVersion {
		return ShapeNone
	}

	// set-name re-alignment is only valid on its own (no in-place annotation toggles).
	if c.SetNameLabelChanged {
		if c.UpdateInProgress == AnnoUnchanged && c.PendingAckMove == AnnoUnchanged &&
			c.PendingHookUpdateMachine == AnnoUnchanged {
			return ShapeMoveAlign
		}
		return ShapeNone
	}

	switch {
	case c.UpdateInProgress == AnnoAdded &&
		c.PendingAckMove == AnnoUnchanged && c.PendingHookUpdateMachine == AnnoUnchanged:
		return ShapeAddInProgress
	case c.UpdateInProgress == AnnoRemoved &&
		c.PendingAckMove == AnnoUnchanged && c.PendingHookUpdateMachine == AnnoUnchanged:
		return ShapeRemoveInProgress
	case c.PendingHookUpdateMachine == AnnoAdded &&
		c.UpdateInProgress == AnnoUnchanged &&
		(c.PendingAckMove == AnnoUnchanged || c.PendingAckMove == AnnoRemoved):
		// KCP-3 (pending-ack unchanged) and MD-TRIG (also drops pending-ack-move).
		return ShapeAddPendingHook
	case c.PendingHookUpdateMachine == AnnoRemoved &&
		c.UpdateInProgress == AnnoUnchanged && c.PendingAckMove == AnnoUnchanged:
		return ShapeRemovePendingHook
	default:
		return ShapeNone
	}
}

// TransitionIDForShape maps a shape to the enumerated transition ID for the
// given grant target kind, or "" if the shape is not valid for that target.
// KCP has no move; MD/MS uses the MD-* ids.
func TransitionIDForShape(shape Shape, kind TargetKind) string {
	switch kind {
	case TargetKubeadmControlPlane:
		switch shape {
		case ShapeAddInProgress:
			return TransitionKCPSetInProgress
		case ShapeVersionBump:
			return TransitionKCPVersionBump
		case ShapeAddPendingHook:
			return TransitionKCPPendingHook
		case ShapeRemovePendingHook:
			return TransitionKCPCleanupHook
		case ShapeRemoveInProgress:
			return TransitionKCPCleanupFlag
		}
	case TargetMachineDeployment:
		switch shape {
		case ShapeMove, ShapeMoveAlign:
			return TransitionMDMove
		case ShapeVersionBump:
			return TransitionMDFinalizeMachine
		case ShapeAddPendingHook:
			return TransitionMDTrigger
		case ShapeRemovePendingHook:
			return TransitionMDCleanupHook
		case ShapeRemoveInProgress:
			return TransitionMDCleanupFlag
		case ShapeAddInProgress:
			// The source MS adds update-in-progress only as part of MD-MOVE, never alone.
			return ""
		}
	}
	return ""
}

// EvaluateMachineUpdateInPlace is the grant-aware authorization for a protected
// Machine UPDATE. It classifies the diff, maps it to the grant's target kind,
// and verifies the grant (read-only). It returns allow() only for a valid,
// claimed (or dry-run), shape- and version-matched grant; otherwise deny().
//
// It reuses VerifyGrant (M2) unchanged and does NOT replace the strict
// EvaluateMachineUpdate — the admission adapter calls this only on the protected
// path, and falls back to the existing deny-by-default for everything else.
func EvaluateMachineUpdateInPlace(
	scope Scope,
	change *MachineChange,
	grant *Grant,
	clusterUID string,
	dryRun bool,
	now time.Time,
) Decision {

	shape := ClassifyMachineTransition(*change)
	if shape == ShapeNone {
		return deny("change is not a recognized in-place transition")
	}
	if grant == nil {
		return deny("in-place update requires an authorized upgrade grant")
	}
	id := TransitionIDForShape(shape, grant.TargetKind)
	if id == "" {
		return deny("transition is not valid for the grant target kind")
	}
	q := GrantQuery{
		DryRun:          dryRun,
		ClusterUID:      clusterUID,
		TargetKind:      grant.TargetKind,
		TargetUID:       grant.TargetUID,
		MachineUID:      grant.MachineUID,
		Transition:      id,
		VersionRelevant: shape == ShapeVersionBump,
		FromVersion:     change.VersionFrom,
		ToVersion:       change.VersionTo,
		Now:             now,
	}
	return VerifyGrant(scope, grant, q)
}
