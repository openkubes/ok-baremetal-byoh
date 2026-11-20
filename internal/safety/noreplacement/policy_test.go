// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement_test

import (
	"strings"
	"testing"

	noreplacement "github.com/openkubes/ok-baremetal-byoh/internal/safety/noreplacement"
)

func TestEvaluateMachineLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		scope     noreplacement.Scope
		operation noreplacement.Operation
		wantAllow bool
		reason    string
	}{
		{
			name: "deny replacement create",
			scope: noreplacement.Scope{
				Verified:  true,
				Protected: true,
			},
			operation: noreplacement.Create,
			reason:    "CREATE",
		},
		{
			name: "deny original machine delete",
			scope: noreplacement.Scope{
				Verified:  true,
				Protected: true,
			},
			operation: noreplacement.Delete,
			reason:    "DELETE",
		},
		{
			name: "allow unprotected create",
			scope: noreplacement.Scope{
				Verified: true,
			},
			operation: noreplacement.Create,
			wantAllow: true,
		},
		{
			name: "allow unprotected delete",
			scope: noreplacement.Scope{
				Verified: true,
			},
			operation: noreplacement.Delete,
			wantAllow: true,
		},
		{
			name:      "fail closed on unknown scope",
			scope:     noreplacement.Scope{},
			operation: noreplacement.Delete,
			reason:    "could not be verified",
		},
		{
			name: "fail closed on protected update",
			scope: noreplacement.Scope{
				Verified:  true,
				Protected: true,
			},
			operation: noreplacement.Update,
			reason:    "not yet supported",
		},
		{
			name: "fail closed on unknown operation",
			scope: noreplacement.Scope{
				Verified:  true,
				Protected: true,
			},
			operation: "UNEXPECTED",
			reason:    "not yet supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := noreplacement.EvaluateMachineLifecycle(
				tt.scope,
				tt.operation,
			)

			if got.Allowed != tt.wantAllow {
				t.Fatalf(
					"Allowed = %v, want %v: %s",
					got.Allowed,
					tt.wantAllow,
					got.Reason,
				)
			}

			if tt.reason != "" &&
				!strings.Contains(got.Reason, tt.reason) {
				t.Fatalf(
					"Reason = %q, expected %q",
					got.Reason,
					tt.reason,
				)
			}
		})
	}
}
