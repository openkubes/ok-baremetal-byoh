// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"strings"
	"testing"
)

func TestEvaluateMachineLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		scope     Scope
		operation Operation
		wantAllow bool
		reason    string
	}{
		{
			name: "deny replacement create",
			scope: Scope{
				Verified:  true,
				Protected: true,
			},
			operation: Create,
			reason:    "CREATE",
		},
		{
			name: "deny original machine delete",
			scope: Scope{
				Verified:  true,
				Protected: true,
			},
			operation: Delete,
			reason:    "DELETE",
		},
		{
			name: "allow unprotected create",
			scope: Scope{
				Verified: true,
			},
			operation: Create,
			wantAllow: true,
		},
		{
			name: "allow unprotected delete",
			scope: Scope{
				Verified: true,
			},
			operation: Delete,
			wantAllow: true,
		},
		{
			name:      "fail closed on unknown scope",
			scope:     Scope{},
			operation: Delete,
			reason:    "could not be verified",
		},
		{
			name: "fail closed on protected update",
			scope: Scope{
				Verified:  true,
				Protected: true,
			},
			operation: Update,
			reason:    "not yet supported",
		},
		{
			name: "fail closed on unknown operation",
			scope: Scope{
				Verified:  true,
				Protected: true,
			},
			operation: "UNEXPECTED",
			reason:    "not yet supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateMachineLifecycle(
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
