// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package inplace holds the pure, dependency-free decision logic for the
// OpenKubes BYOH in-place update RuntimeSDK extension (CanUpdateMachine,
// CanUpdateMachineSet, UpdateMachine). It performs NO I/O so it can be unit
// tested offline; the CAPI-typed glue lives in internal/inplace/extension.
package inplace

import "time"

// SpecView is the in-place-relevant subset of a Machine (or MachineSet template)
// spec that the OpenKubes BYOH extension can update WITHOUT replacing the host.
//
// Eligibility is deliberately VERSION-ONLY and identity-preserving. Verified
// against cluster-api v1.14.3: a version-only upgrade never mutates
// spec.failureDomain — KCP passes the existing machine's failureDomain as the
// desired value (helpers.go:311 -> desiredstate), and the MachineSet move only
// copies the template's failureDomain (machineset_controller.go:445), which is
// unchanged in a version-only upgrade. Accepting failureDomain is therefore not
// required; for BYOH bare-metal a failureDomain change cannot be performed
// in-place anyway, so anything beyond spec.version is left to CAPI (which then
// declines in-place), and the No-Replacement guard safely refuses any resulting
// replacement.
type SpecView struct {
	Version string
}

// AcceptInPlace returns a copy of current with spec.version advanced to desired
// (the only field this extension updates in-place), plus the names of the fields
// it actually changed. Pure (no I/O). This is the decision core of the
// CanUpdate* handlers: the handler marshals a JSON patch (current -> accepted)
// and returns it; CAPI then checks whether any diff remains and, if so, declines
// in-place (which, under the guard, cannot become a replacement).
func AcceptInPlace(current, desired SpecView) (accepted SpecView, fields []string) {
	accepted = current
	if current.Version != desired.Version {
		accepted.Version = desired.Version
		fields = append(fields, "spec.version")
	}
	return accepted, fields
}

// VersionChanged reports whether the Kubernetes version differs — the only
// transition Phase 0 actually performs on the host.
func VersionChanged(current, desired SpecView) bool {
	return current.Version != desired.Version
}

// DefaultUpdateCompleteAfter is how long the CAPD test handler simulates an
// in-place update before reporting completion. The handler performs NO real
// work (no kubeadm upgrade, no host signal, no drain, no agent) — that is the
// production executor, explicitly OUT of Phase 0 scope.
const DefaultUpdateCompleteAfter = 10 * time.Second

// UpdateDone reports whether a simulated in-place update that started at
// startedAt is complete as of now. Pure; used by the CAPD UpdateMachine test
// handler to drive the RetryAfterSeconds>0 (in progress) -> =0 (done) protocol.
func UpdateDone(startedAt, now time.Time, completeAfter time.Duration) bool {
	return !now.Before(startedAt.Add(completeAfter))
}
