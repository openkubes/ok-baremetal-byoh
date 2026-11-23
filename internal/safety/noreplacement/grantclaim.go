// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import "fmt"

// GrantActor is the authenticated role of the requester mutating the shared
// grant CAS object. Identity is established from admission userInfo (and backed
// by RBAC); this policy enforces the field-level separation RBAC cannot express.
type GrantActor string

const (
	// ActorApprover creates the immutable grant data block (never the claim).
	ActorApprover GrantActor = "approver"
	// ActorInitiator may add the claim slot exactly once (never grant data).
	ActorInitiator GrantActor = "initiator"
	// ActorOther is any other identity; it may not mutate a grant at all.
	ActorOther GrantActor = "other"
)

// EvaluateGrantConfigMap enforces the role/field separation and single-use claim
// semantics of the shared CAS grant object (M1 §1.2, §1.3):
//
//   - CREATE  (Approver):   immutable data block, NO claim key.
//   - UPDATE  (Initiator):  the ONLY permitted delta is adding the claim key
//     exactly once; every data key must be byte-identical.
//   - DELETE / data mutation / re-claim / wrong actor: denied, fail-closed.
//
// The apiserver's optimistic concurrency (resourceVersion CAS) guarantees that
// of two concurrent claim UPDATEs exactly one is admitted; this policy denies
// any UPDATE whose OLD object already carries a claim, closing the race from the
// admission side as well. The guard writes nothing here — it only admits/denies.
func EvaluateGrantConfigMap(
	op Operation,
	actor GrantActor,
	oldData map[string]string,
	newData map[string]string,
) Decision {

	switch op {
	case Create:
		if actor != ActorApprover {
			return deny("only the approver may create an upgrade grant")
		}
		// A freshly created grant must be well-formed and must NOT be pre-claimed.
		if _, hasClaim := newData[GrantClaimKey]; hasClaim {
			return deny("a new upgrade grant must not contain a claim")
		}
		if _, err := ParseGrant(newData); err != nil {
			return deny(fmt.Sprintf("invalid upgrade grant: %v", err))
		}
		return allow()

	case Update:
		if actor != ActorInitiator {
			return deny("only the upgrade initiator may update an upgrade grant")
		}
		// Single-use: the grant must not already be claimed.
		if oldClaim, ok := oldData[GrantClaimKey]; ok && oldClaim != "" {
			return deny("upgrade grant has already been claimed (single-use)")
		}
		// The only permitted delta is adding a non-empty claim key.
		newClaim, hasNewClaim := newData[GrantClaimKey]
		if !hasNewClaim || newClaim == "" {
			return deny("upgrade grant update must set the claim")
		}
		// Every immutable data key must be byte-identical (no add/remove/change).
		if !sameExceptClaim(oldData, newData) {
			return deny("upgrade grant data is immutable; only the claim may be added")
		}
		return allow()

	case Delete:
		return deny("upgrade grant deletion is not permitted during an in-place lifecycle")

	default:
		return deny(fmt.Sprintf("unsupported upgrade grant operation %q", op))
	}
}

// sameExceptClaim reports whether oldData and newData are identical on every key
// other than the claim slot (which newData is allowed to add). It rejects any
// added, removed or modified data key.
func sameExceptClaim(oldData, newData map[string]string) bool {
	for k, ov := range oldData {
		if k == GrantClaimKey {
			continue
		}
		nv, ok := newData[k]
		if !ok || nv != ov {
			return false
		}
	}
	for k, nv := range newData {
		if k == GrantClaimKey {
			continue
		}
		ov, ok := oldData[k]
		if !ok || ov != nv {
			return false
		}
	}
	return true
}
