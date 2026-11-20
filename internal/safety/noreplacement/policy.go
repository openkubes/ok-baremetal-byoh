// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package noreplacement contains policy decisions for in-place-only
// BYOH clusters. It does not query Kubernetes or authorize lifecycle
// exceptions. Callers must supply independently verified scope.
package noreplacement

import "fmt"

// Operation is the Kubernetes admission operation being evaluated.
type Operation string

const (
	Create Operation = "CREATE"
	Update Operation = "UPDATE"
	Delete Operation = "DELETE"
)

// Scope is resolved from a trusted cluster-level policy source.
// It must never be derived solely from incoming Machine labels.
type Scope struct {
	Verified  bool
	Protected bool
}

// Decision represents a policy decision and its explanation.
type Decision struct {
	Allowed bool
	Reason  string
}

func allow() Decision {
	return Decision{Allowed: true}
}

func deny(reason string) Decision {
	return Decision{
		Allowed: false,
		Reason:  reason,
	}
}

// EvaluateMachineLifecycle handles CREATE and DELETE protection.
//
// This is an initial policy core, not a complete admission guard.
// Machine UPDATE and authorized lifecycle exceptions must be
// implemented and tested before enabling the webhook.
//
// Unknown scope and unsupported operations fail closed.
func EvaluateMachineLifecycle(
	scope Scope,
	operation Operation,
) Decision {

	if !scope.Verified {
		return deny("BYOH protection scope could not be verified")
	}

	if !scope.Protected {
		return allow()
	}

	switch operation {
	case Create:
		return deny(
			"replacement or provisioning Machine CREATE " +
				"requires explicit lifecycle authorization",
		)
	case Delete:
		return deny(
			"Machine DELETE is forbidden during " +
				"in-place-only BYOH lifecycle",
		)
	default:
		return deny(fmt.Sprintf(
			"operation %q is not yet supported by the "+
				"no-replacement policy",
			operation,
		))
	}
}
