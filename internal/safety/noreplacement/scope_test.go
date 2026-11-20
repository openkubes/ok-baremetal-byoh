// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement_test

import (
	"testing"

	noreplacement "github.com/openkubes/ok-baremetal-byoh/internal/safety/noreplacement"
)

//nolint:funlen // Enumerates every scope-resolution outcome incl. duplicate name/UID and missing-UID fail-closed as one authoritative decision table.
func TestResolveScope(t *testing.T) {
	protected := noreplacement.ClusterIdentity{
		Name:      "ok-byoh",
		UID:       "cluster-uid-001",
		Protected: true,
	}

	other := noreplacement.ClusterIdentity{
		Name: "other-cluster",
		UID:  "cluster-uid-002",
	}

	tests := []struct {
		name string
		in   noreplacement.NamespaceInventory
		want noreplacement.Scope
	}{
		{
			name: "protected namespace",
			in: noreplacement.NamespaceInventory{
				Verified: true,
				Clusters: []noreplacement.ClusterIdentity{protected},
			},
			want: noreplacement.Scope{
				Verified:  true,
				Protected: true,
			},
		},
		{
			name: "mixed namespace remains protected",
			in: noreplacement.NamespaceInventory{
				Verified: true,
				Clusters: []noreplacement.ClusterIdentity{protected, other},
			},
			want: noreplacement.Scope{
				Verified:  true,
				Protected: true,
			},
		},
		{
			name: "verified unprotected namespace",
			in: noreplacement.NamespaceInventory{
				Verified: true,
				Clusters: []noreplacement.ClusterIdentity{other},
			},
			want: noreplacement.Scope{Verified: true},
		},
		{
			name: "verified empty inventory",
			in: noreplacement.NamespaceInventory{
				Verified: true,
			},
			want: noreplacement.Scope{Verified: true},
		},
		{
			name: "unverified inventory",
			in: noreplacement.NamespaceInventory{
				Clusters: []noreplacement.ClusterIdentity{protected},
			},
			want: noreplacement.Scope{},
		},
		{
			name: "missing cluster UID",
			in: noreplacement.NamespaceInventory{
				Verified: true,
				Clusters: []noreplacement.ClusterIdentity{{
					Name:      "ok-byoh",
					Protected: true,
				}},
			},
			want: noreplacement.Scope{},
		},
		{
			name: "duplicate cluster name",
			in: noreplacement.NamespaceInventory{
				Verified: true,
				Clusters: []noreplacement.ClusterIdentity{
					protected,
					{
						Name: "ok-byoh",
						UID:  "different-uid",
					},
				},
			},
			want: noreplacement.Scope{},
		},
		{
			name: "duplicate cluster UID",
			in: noreplacement.NamespaceInventory{
				Verified: true,
				Clusters: []noreplacement.ClusterIdentity{
					protected,
					{
						Name: "another-cluster",
						UID:  protected.UID,
					},
				},
			},
			want: noreplacement.Scope{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := noreplacement.ResolveScope(tt.in)
			if got != tt.want {
				t.Fatalf(
					"noreplacement.ResolveScope() = %+v, want %+v",
					got, tt.want,
				)
			}
		})
	}
}

func TestNamespaceGuardCannotBeBypassedByMachineMetadata(
	t *testing.T,
) {

	inventory := noreplacement.NamespaceInventory{
		Verified: true,
		Clusters: []noreplacement.ClusterIdentity{{
			Name:      "ok-byoh",
			UID:       "cluster-uid-001",
			Protected: true,
		}},
	}

	// The resolver deliberately has no Machine-label or
	// Machine-owner-reference input. Hence these metadata
	// manipulations cannot influence the scope decision.
	for _, operation := range []noreplacement.Operation{noreplacement.Create, noreplacement.Delete} {
		scope := noreplacement.ResolveScope(inventory)
		decision := noreplacement.EvaluateMachineLifecycle(scope, operation)

		if decision.Allowed {
			t.Fatalf(
				"%s allowed in protected namespace",
				operation,
			)
		}
	}
}
