// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement_test

import (
	"context"
	"testing"

	noreplacement "github.com/openkubes/ok-baremetal-byoh/internal/safety/noreplacement"
)

//nolint:funlen // Single table for every registered-scope outcome (UID match, recreated-UID reject, lookup error, missing, unregistered, unprotected, contradictory, empty) kept as one fail-closed contract.
func TestResolveRegisteredScope(t *testing.T) {
	const (
		namespace = "protected-byoh"
		name      = "ok-byoh"
		uid       = "cluster-uid-001"
	)

	protected := noreplacement.NamespaceRegistration{
		Protected:   true,
		ClusterName: name,
		ClusterUID:  uid,
	}

	tests := []struct {
		name          string
		namespace     string
		registrations map[string]noreplacement.NamespaceRegistration
		actualUID     string
		lookupErr     error
		want          noreplacement.Scope
	}{
		{
			name:      "matching protected cluster UID",
			namespace: namespace,
			registrations: map[string]noreplacement.NamespaceRegistration{
				namespace: protected,
			},
			actualUID: uid,
			want: noreplacement.Scope{
				Verified:  true,
				Protected: true,
			},
		},
		{
			name:      "recreated cluster is rejected",
			namespace: namespace,
			registrations: map[string]noreplacement.NamespaceRegistration{
				namespace: protected,
			},
			actualUID: "different-cluster-uid",
		},
		{
			name:      "API lookup error fails closed",
			namespace: namespace,
			registrations: map[string]noreplacement.NamespaceRegistration{
				namespace: protected,
			},
			lookupErr: noreplacement.ErrClusterUnavailable,
		},
		{
			name:      "missing cluster fails closed",
			namespace: namespace,
			registrations: map[string]noreplacement.NamespaceRegistration{
				namespace: protected,
			},
		},
		{
			name:      "unregistered namespace fails closed",
			namespace: "unknown",
			registrations: map[string]noreplacement.NamespaceRegistration{
				namespace: protected,
			},
		},
		{
			name:      "explicit unprotected namespace",
			namespace: "ordinary",
			registrations: map[string]noreplacement.NamespaceRegistration{
				"ordinary": {},
			},
			want: noreplacement.Scope{Verified: true},
		},
		{
			name:      "contradictory unprotected registration",
			namespace: "ordinary",
			registrations: map[string]noreplacement.NamespaceRegistration{
				"ordinary": {
					ClusterName: name,
					ClusterUID:  uid,
				},
			},
		},
		{
			name:      "empty namespace rejected",
			namespace: "",
			registrations: map[string]noreplacement.NamespaceRegistration{
				namespace: protected,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(
				_ context.Context,
				ns, cluster string,
			) (string, error) {

				if ns != namespace || cluster != name {
					t.Fatalf(
						"unexpected lookup: %s/%s",
						ns, cluster,
					)
				}
				return tt.actualUID, tt.lookupErr
			}

			got := noreplacement.ResolveRegisteredScope(
				context.Background(),
				tt.namespace,
				tt.registrations,
				lookup,
			)

			if got != tt.want {
				t.Fatalf(
					"got %+v, want %+v",
					got, tt.want,
				)
			}
		})
	}
}

func TestRegisteredScopeBlocksReplacement(t *testing.T) {
	scope := noreplacement.ResolveRegisteredScope(
		context.Background(),
		"byoh-lab",
		map[string]noreplacement.NamespaceRegistration{
			"byoh-lab": {
				Protected:   true,
				ClusterName: "ok-byoh",
				ClusterUID:  "uid-123",
			},
		},
		func(
			_ context.Context,
			_, _ string,
		) (string, error) {

			return "uid-123", nil
		},
	)

	for _, operation := range []noreplacement.Operation{
		noreplacement.Create,
		noreplacement.Delete,
	} {
		result := noreplacement.EvaluateMachineLifecycle(
			scope,
			operation,
		)
		if result.Allowed {
			t.Fatalf(
				"%s unexpectedly allowed",
				operation,
			)
		}
	}
}
