// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package-internal tests intentionally exercise the unexported token Secret helper.

//nolint:testpackage // Ensure retries and Secret ownership are tested without exporting internals.
package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	infrastructurev1beta1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBootstrapTokenSecretRetryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	request := &infrastructurev1beta1.BootstrapKubeconfig{
		ObjectMeta: metav1.ObjectMeta{Name: "enroll", Namespace: "default", UID: types.UID("stable-object-uid")},
	}
	var first string
	for i := 0; i < 10; i++ {
		token, err := ensureBootstrapTokenSecret(ctx, c, request, 30*time.Minute)
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if i == 0 {
			first = token
		} else if token != first {
			t.Fatalf("retry %d issued a different token", i)
		}
	}
	tokens := &corev1.SecretList{}
	if err := c.List(ctx, tokens, client.InNamespace(metav1.NamespaceSystem)); err != nil {
		t.Fatal(err)
	}
	if len(tokens.Items) != 1 {
		t.Fatalf("want one Secret after ten retries, got %d", len(tokens.Items))
	}

	// Re-create the controller's decision using the same API client; no
	// in-memory token cache should be necessary after a manager restart.
	token, err := ensureBootstrapTokenSecret(ctx, c, request, 30*time.Minute)
	if err != nil || token != first {
		t.Fatalf("retry after restart: err=%v equal=%t", err, token == first)
	}
}

func TestBootstrapTokenSecretRejectsForeignOrExpiredCredentials(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	request := &infrastructurev1beta1.BootstrapKubeconfig{
		ObjectMeta: metav1.ObjectMeta{Name: "enroll", Namespace: "default", UID: types.UID("stable-object-uid")},
	}
	first, err := ensureBootstrapTokenSecret(ctx, c, request, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &corev1.SecretList{}
	if err := c.List(ctx, secrets, client.InNamespace(metav1.NamespaceSystem)); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatal("expected exactly one Secret")
	}
	stored := secrets.Items[0].DeepCopy()
	key := client.ObjectKeyFromObject(stored)

	stored.Annotations[bootstrapRequestUIDAnnotation] = "another-uid"
	if err := c.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureBootstrapTokenSecret(ctx, c, request, 30*time.Minute); err == nil {
		t.Fatal("foreign Secret was accepted")
	}

	if err := c.Get(ctx, key, stored); err != nil {
		t.Fatal(err)
	}
	stored.Annotations[bootstrapRequestUIDAnnotation] = string(request.UID)
	stored.Data[bootstrapapi.BootstrapTokenExpirationKey] = []byte(time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	if err := c.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureBootstrapTokenSecret(ctx, c, request, 30*time.Minute); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired-token error; got %v", err)
	}

	if err := c.Get(ctx, key, stored); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%s.%s", stored.Data[bootstrapapi.BootstrapTokenIDKey], stored.Data[bootstrapapi.BootstrapTokenSecretKey]); got != first {
		t.Fatal("expiry handling mutated existing credentials")
	}
}

func TestBootstrapTokenSecretHashCollisionFailsClosed(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Different UIDs with the same first six SHA-256 hex characters (a72c92).
	// Keeping a real collision fixture makes this independent of hash mocks.
	firstRequest := &infrastructurev1beta1.BootstrapKubeconfig{
		ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "default", UID: types.UID("collision-8515")},
	}
	secondRequest := &infrastructurev1beta1.BootstrapKubeconfig{
		ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "default", UID: types.UID("collision-11163")},
	}
	firstToken, err := ensureBootstrapTokenSecret(ctx, c, firstRequest, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &corev1.SecretList{}
	if err := c.List(ctx, secrets, client.InNamespace(metav1.NamespaceSystem)); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("expected one Secret, got %d", len(secrets.Items))
	}
	original := secrets.Items[0].DeepCopy()

	if _, err := ensureBootstrapTokenSecret(ctx, c, secondRequest, 30*time.Minute); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("expected fail-closed hash collision, got %v", err)
	}
	after := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(original), after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original.Data, after.Data) || !reflect.DeepEqual(original.Annotations, after.Annotations) {
		t.Fatal("collision mutated the existing token Secret")
	}
	if token, err := ensureBootstrapTokenSecret(ctx, c, firstRequest, 30*time.Minute); err != nil || token != firstToken {
		t.Fatalf("collision affected legitimate owner: err=%v equal=%t", err, token == firstToken)
	}
	if err := c.List(ctx, secrets, client.InNamespace(metav1.NamespaceSystem)); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("collision created additional Secrets: %d", len(secrets.Items))
	}
}
