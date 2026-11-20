// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubernetesClusterLookup(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "byoh-lab",
			Name:      "ok-byoh",
			UID:       types.UID("uid-123"),
		},
	}

	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cluster).
		Build()

	lookup := KubernetesClusterLookup(reader)

	t.Run("returns actual UID", func(t *testing.T) {
		uid, err := lookup(ctx, "byoh-lab", "ok-byoh")
		if err != nil {
			t.Fatal(err)
		}
		if uid != "uid-123" {
			t.Fatalf("UID = %q, want uid-123", uid)
		}
	})

	t.Run("missing cluster fails", func(t *testing.T) {
		_, err := lookup(ctx, "byoh-lab", "missing")
		if err == nil {
			t.Fatal("expected lookup error")
		}
	})

	t.Run("wrong namespace fails", func(t *testing.T) {
		_, err := lookup(ctx, "other", "ok-byoh")
		if err == nil {
			t.Fatal("expected lookup error")
		}
	})

	t.Run("empty identity fails", func(t *testing.T) {
		_, err := lookup(ctx, "", "ok-byoh")
		if err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("nil reader fails closed", func(t *testing.T) {
		nilReaderLookup := KubernetesClusterLookup(nil)
		_, err := nilReaderLookup(ctx, "byoh-lab", "ok-byoh")
		if err == nil {
			t.Fatal("expected reader error")
		}
	})

	t.Run("registration verifies API identity", func(t *testing.T) {
		scope := ResolveRegisteredScope(
			ctx,
			"byoh-lab",
			map[string]NamespaceRegistration{
				"byoh-lab": {
					Protected:   true,
					ClusterName: "ok-byoh",
					ClusterUID:  "uid-123",
				},
			},
			lookup,
		)

		if !scope.Verified || !scope.Protected {
			t.Fatalf("unexpected scope: %+v", scope)
		}

		for _, operation := range []Operation{
			Create,
			Delete,
		} {
			if decision := EvaluateMachineLifecycle(
				scope,
				operation,
			); decision.Allowed {
				t.Fatalf(
					"%s unexpectedly allowed",
					operation,
				)
			}
		}
	})

	t.Run("UID mismatch fails closed", func(t *testing.T) {
		scope := ResolveRegisteredScope(
			ctx,
			"byoh-lab",
			map[string]NamespaceRegistration{
				"byoh-lab": {
					Protected:   true,
					ClusterName: "ok-byoh",
					ClusterUID:  "old-uid",
				},
			},
			lookup,
		)

		if scope.Verified {
			t.Fatalf(
				"mismatched cluster unexpectedly verified: %+v",
				scope,
			)
		}
	})
}
