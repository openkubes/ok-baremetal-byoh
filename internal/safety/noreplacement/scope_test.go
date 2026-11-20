// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import "testing"

func TestResolveScope(t *testing.T) {
	protected := ClusterIdentity{
		Name:      "ok-byoh",
		UID:       "cluster-uid-001",
		Protected: true,
	}

	other := ClusterIdentity{
		Name: "other-cluster",
		UID:  "cluster-uid-002",
	}

	tests := []struct {
		name string
		in   NamespaceInventory
		want Scope
	}{
		{
			name: "protected namespace",
			in: NamespaceInventory{
				Verified: true,
				Clusters: []ClusterIdentity{protected},
			},
			want: Scope{
				Verified:  true,
				Protected: true,
			},
		},
		{
			name: "mixed namespace remains protected",
			in: NamespaceInventory{
				Verified: true,
				Clusters: []ClusterIdentity{protected, other},
			},
			want: Scope{
				Verified:  true,
				Protected: true,
			},
		},
		{
			name: "verified unprotected namespace",
			in: NamespaceInventory{
				Verified: true,
				Clusters: []ClusterIdentity{other},
			},
			want: Scope{Verified: true},
		},
		{
			name: "verified empty inventory",
			in: NamespaceInventory{
				Verified: true,
			},
			want: Scope{Verified: true},
		},
		{
			name: "unverified inventory",
			in: NamespaceInventory{
				Clusters: []ClusterIdentity{protected},
			},
			want: Scope{},
		},
		{
			name: "missing cluster UID",
			in: NamespaceInventory{
				Verified: true,
				Clusters: []ClusterIdentity{{
					Name:      "ok-byoh",
					Protected: true,
				}},
			},
			want: Scope{},
		},
		{
			name: "duplicate cluster name",
			in: NamespaceInventory{
				Verified: true,
				Clusters: []ClusterIdentity{
					protected,
					{
						Name: "ok-byoh",
						UID:  "different-uid",
					},
				},
			},
			want: Scope{},
		},
		{
			name: "duplicate cluster UID",
			in: NamespaceInventory{
				Verified: true,
				Clusters: []ClusterIdentity{
					protected,
					{
						Name: "another-cluster",
						UID:  protected.UID,
					},
				},
			},
			want: Scope{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveScope(tt.in)
			if got != tt.want {
				t.Fatalf(
					"ResolveScope() = %+v, want %+v",
					got, tt.want,
				)
			}
		})
	}
}

func TestNamespaceGuardCannotBeBypassedByMachineMetadata(
	t *testing.T,
) {
	inventory := NamespaceInventory{
		Verified: true,
		Clusters: []ClusterIdentity{{
			Name:      "ok-byoh",
			UID:       "cluster-uid-001",
			Protected: true,
		}},
	}

	// The resolver deliberately has no Machine-label or
	// Machine-owner-reference input. Hence these metadata
	// manipulations cannot influence the scope decision.
	for _, operation := range []Operation{Create, Delete} {
		scope := ResolveScope(inventory)
		decision := EvaluateMachineLifecycle(scope, operation)

		if decision.Allowed {
			t.Fatalf(
				"%s allowed in protected namespace",
				operation,
			)
		}
	}
}
