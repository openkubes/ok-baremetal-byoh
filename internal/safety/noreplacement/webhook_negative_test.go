// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"testing"
	"time"
)

// These negative tests exercise the policy layer end-to-end (scope resolution +
// grant verification + grant-ConfigMap role separation) for the three cases GPT
// asked for after M3: unprotected namespace, missing grant, wrong requester.
//
// They are runnable OFFLINE (stdlib only). The webhook ROUTING scope
// (namespaceSelector / objectSelector) and live apiserver admission behaviour are
// NOT covered here — those are marked PENDING and verified on the Mac / in CI.

func negTestRegistrations() map[string]NamespaceRegistration {
	return map[string]NamespaceRegistration{
		"protected-ns": {
			Protected:   true,
			ClusterName: "cluster-1",
			ClusterUID:  "cluster-uid-1",
		},
		"unprotected-ns": {Protected: false},
	}
}

func negTestLookup() ClusterLookup {
	return func(_ context.Context, ns, name string) (string, error) {
		if ns == "protected-ns" && name == "cluster-1" {
			return "cluster-uid-1", nil
		}
		return "", ErrClusterUnavailable
	}
}

// Unprotected namespace: scope resolves to verified-but-unprotected, so the
// in-place grant requirement does NOT apply and the change is allowed. This is
// the guard-side half of "no effect on unprotected namespaces" (the other half
// is the webhook namespaceSelector — PENDING, verified in k8s).
func TestNegative_UnprotectedNamespaceNotGated(t *testing.T) {
	scope := ResolveRegisteredScope(context.Background(), "unprotected-ns", negTestRegistrations(), negTestLookup())
	if !scope.Verified || scope.Protected {
		t.Fatalf("unprotected-ns must resolve to verified, unprotected; got %+v", scope)
	}
	// Even with no grant at all, an unprotected namespace is not gated.
	if d := VerifyGrant(scope, nil, baseKCPQuery()); !d.Allowed {
		t.Fatalf("unprotected namespace must not be gated by the grant requirement: %s", d.Reason)
	}
}

// Unregistered namespace fails closed (scope unverified) — this is safe (no
// bypass); routing such a namespace is prevented by the webhook namespaceSelector.
func TestNegative_UnregisteredNamespaceFailsClosed(t *testing.T) {
	scope := ResolveRegisteredScope(context.Background(), "random-ns", negTestRegistrations(), negTestLookup())
	if scope.Verified {
		t.Fatalf("unregistered namespace must not be verified; got %+v", scope)
	}
}

// Missing grant in a protected namespace: real in-place write is denied.
func TestNegative_ProtectedNamespaceMissingGrant(t *testing.T) {
	scope := ResolveRegisteredScope(context.Background(), "protected-ns", negTestRegistrations(), negTestLookup())
	if !scope.Verified || !scope.Protected {
		t.Fatalf("protected-ns must resolve to verified+protected; got %+v", scope)
	}
	if d := VerifyGrant(scope, nil, baseKCPQuery()); d.Allowed {
		t.Fatalf("protected namespace with no grant must deny a real in-place write")
	}
}

// Wrong requester on the grant CAS object: neither an approver nor an unknown
// actor may add a claim; only the initiator may, and only once.
func TestNegative_WrongRequesterCannotClaim(t *testing.T) {
	old := validKCPGrantData()
	claimed := validKCPGrantData()
	claimed[GrantClaimKey] = "c"

	for _, actor := range []GrantActor{ActorApprover, ActorOther} {
		if d := EvaluateGrantConfigMap(Update, actor, old, claimed); d.Allowed {
			t.Fatalf("actor %q must not be allowed to set the claim", actor)
		}
	}
	// And an unknown actor may not create a grant either.
	if d := EvaluateGrantConfigMap(Create, ActorOther, nil, validKCPGrantData()); d.Allowed {
		t.Fatalf("a non-approver must not create a grant")
	}
}

// Sanity: the happy path still holds alongside the negatives.
func TestNegative_InitiatorClaimStillAllowed(t *testing.T) {
	old := validKCPGrantData()
	claimed := validKCPGrantData()
	claimed[GrantClaimKey] = "c"
	if d := EvaluateGrantConfigMap(Update, ActorInitiator, old, claimed); !d.Allowed {
		t.Fatalf("the initiator must still be able to claim: %s", d.Reason)
	}
	_ = time.Now
}
