// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import "testing"

func TestGrantConfigMap_CreateByApprover(t *testing.T) {
	d := VerifyGrantCreate(t, ActorApprover, validKCPGrantData())
	if !d.Allowed {
		t.Fatalf("approver create of a valid grant must be allowed: %s", d.Reason)
	}
}

// helper wrappers keep the table tests readable.
func VerifyGrantCreate(t *testing.T, actor GrantActor, newData map[string]string) Decision {
	t.Helper()
	return EvaluateGrantConfigMap(Create, actor, nil, newData)
}

func TestGrantConfigMap_CreateRejections(t *testing.T) {
	t.Run("non-approver create", func(t *testing.T) {
		if EvaluateGrantConfigMap(Create, ActorInitiator, nil, validKCPGrantData()).Allowed {
			t.Fatalf("only the approver may create a grant")
		}
	})
	t.Run("create with pre-set claim", func(t *testing.T) {
		d := validKCPGrantData()
		d[GrantClaimKey] = "sneaky"
		if EvaluateGrantConfigMap(Create, ActorApprover, nil, d).Allowed {
			t.Fatalf("a new grant must not be pre-claimed")
		}
	})
	t.Run("create with invalid data", func(t *testing.T) {
		d := validKCPGrantData()
		delete(d, grantKeyClusterUID)
		if EvaluateGrantConfigMap(Create, ActorApprover, nil, d).Allowed {
			t.Fatalf("invalid grant data must be rejected at create")
		}
	})
}

func TestGrantConfigMap_InitiatorClaim(t *testing.T) {
	old := validKCPGrantData() // unclaimed
	newData := validKCPGrantData()
	newData[GrantClaimKey] = "claim-1"
	if !EvaluateGrantConfigMap(Update, ActorInitiator, old, newData).Allowed {
		t.Fatalf("initiator adding a claim to an unclaimed grant must be allowed")
	}
}

// Two competing claims: the first wins; the second observes a claim already
// present on the OLD object and is denied (exactly one winner).
func TestGrantConfigMap_SecondClaimDenied(t *testing.T) {
	old := validKCPGrantData()
	old[GrantClaimKey] = "claim-first" // already claimed
	newData := validKCPGrantData()
	newData[GrantClaimKey] = "claim-second"
	if EvaluateGrantConfigMap(Update, ActorInitiator, old, newData).Allowed {
		t.Fatalf("a second claim must be denied (single-use)")
	}
}

func TestGrantConfigMap_UpdateRejections(t *testing.T) {
	t.Run("non-initiator update", func(t *testing.T) {
		old := validKCPGrantData()
		newData := validKCPGrantData()
		newData[GrantClaimKey] = "c"
		if EvaluateGrantConfigMap(Update, ActorApprover, old, newData).Allowed {
			t.Fatalf("only the initiator may claim")
		}
	})
	t.Run("update without claim", func(t *testing.T) {
		old := validKCPGrantData()
		newData := validKCPGrantData() // no claim added
		if EvaluateGrantConfigMap(Update, ActorInitiator, old, newData).Allowed {
			t.Fatalf("an update that does not set the claim must be denied")
		}
	})
	t.Run("mutate a data key while claiming", func(t *testing.T) {
		old := validKCPGrantData()
		newData := validKCPGrantData()
		newData[grantKeyToVersion] = "v9.9.9" // tamper
		newData[GrantClaimKey] = "c"
		if EvaluateGrantConfigMap(Update, ActorInitiator, old, newData).Allowed {
			t.Fatalf("grant data must be immutable on claim")
		}
	})
	t.Run("add a data key while claiming", func(t *testing.T) {
		old := validKCPGrantData()
		newData := validKCPGrantData()
		newData["extraKey"] = "x"
		newData[GrantClaimKey] = "c"
		if EvaluateGrantConfigMap(Update, ActorInitiator, old, newData).Allowed {
			t.Fatalf("adding a data key must be denied")
		}
	})
}

func TestGrantConfigMap_DeleteDenied(t *testing.T) {
	if EvaluateGrantConfigMap(Delete, ActorApprover, validKCPGrantData(), nil).Allowed {
		t.Fatalf("grant deletion must be denied during an in-place lifecycle")
	}
}
