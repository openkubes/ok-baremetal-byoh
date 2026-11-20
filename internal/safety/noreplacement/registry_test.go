// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"testing"
)

func TestResolveRegisteredScope(t *testing.T) {
	const (
		namespace = "protected-byoh"
		name      = "ok-byoh"
		uid       = "cluster-uid-001"
	)

	protected := NamespaceRegistration{
		Protected:   true,
		ClusterName: name,
		ClusterUID:  uid,
	}

	tests := []struct {
		name          string
		namespace     string
		registrations map[string]NamespaceRegistration
		actualUID     string
		lookupErr     error
		want          Scope
	}{
		{
			name:      "matching protected cluster UID",
			namespace: namespace,
			registrations: map[string]NamespaceRegistration{
				namespace: protected,
			},
			actualUID: uid,
			want: Scope{
				Verified:  true,
				Protected: true,
			},
		},
		{
			name:      "recreated cluster is rejected",
			namespace: namespace,
			registrations: map[string]NamespaceRegistration{
				namespace: protected,
			},
			actualUID: "different-cluster-uid",
		},
		{
			name:      "API lookup error fails closed",
			namespace: namespace,
			registrations: map[string]NamespaceRegistration{
				namespace: protected,
			},
			lookupErr: ErrClusterUnavailable,
		},
		{
			name:      "missing cluster fails closed",
			namespace: namespace,
			registrations: map[string]NamespaceRegistration{
				namespace: protected,
			},
		},
		{
			name:      "unregistered namespace fails closed",
			namespace: "unknown",
			registrations: map[string]NamespaceRegistration{
				namespace: protected,
			},
		},
		{
			name:      "explicit unprotected namespace",
			namespace: "ordinary",
			registrations: map[string]NamespaceRegistration{
				"ordinary": {},
			},
			want: Scope{Verified: true},
		},
		{
			name:      "contradictory unprotected registration",
			namespace: "ordinary",
			registrations: map[string]NamespaceRegistration{
				"ordinary": {
					ClusterName: name,
					ClusterUID:  uid,
				},
			},
		},
		{
			name:      "empty namespace rejected",
			namespace: "",
			registrations: map[string]NamespaceRegistration{
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

			got := ResolveRegisteredScope(
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
	scope := ResolveRegisteredScope(
		context.Background(),
		"byoh-lab",
		map[string]NamespaceRegistration{
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

	for _, operation := range []Operation{
		Create,
		Delete,
	} {
		result := EvaluateMachineLifecycle(
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
