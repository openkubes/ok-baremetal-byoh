// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"errors"
	"testing"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	controllers "github.com/openkubes/ok-baremetal-byoh/controllers/infrastructure"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newTokenRevocationFixture(t *testing.T) (context.Context, client.Client, *infrav1.BootstrapKubeconfig, *runtime.Scheme) {
	t.Helper()
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := infrav1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	request := &infrav1.BootstrapKubeconfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "revoke-me", Namespace: "default", UID: types.UID("revocation-request-uid"),
		},
		Spec: infrav1.BootstrapKubeconfigSpec{
			APIServer: "https://127.0.0.1:6443",
			CertificateAuthorityData: "dGVzdC1jYQ==",
		},
	}
	storage := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&infrav1.BootstrapKubeconfig{}).
		WithObjects(request).Build()
	return ctx, storage, request, scheme
}

func reconcileTokenRequest(t *testing.T, ctx context.Context, storage client.Client, scheme *runtime.Scheme, request *infrav1.BootstrapKubeconfig) {
	t.Helper()
	r := &controllers.BootstrapKubeconfigReconciler{Client: storage, Scheme: scheme}
	key := types.NamespacedName{Name: request.Name, Namespace: request.Namespace}
	// First reconcile installs finalizer. No Secret must exist yet.
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	existing := &infrav1.BootstrapKubeconfig{}
	if err := storage.Get(ctx, key, existing); err != nil {
		t.Fatal(err)
	}
	if len(existing.Finalizers) != 1 {
		t.Fatalf("expected finalizer before issuance; got %v", existing.Finalizers)
	}
	first := &corev1.SecretList{}
	if err := storage.List(ctx, first, client.InNamespace(metav1.NamespaceSystem)); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 0 {
		t.Fatal("token existed before finalizer")
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapTokenRevokedOnRequestDeletion(t *testing.T) {
	ctx, storage, request, scheme := newTokenRevocationFixture(t)
	reconcileTokenRequest(t, ctx, storage, scheme, request)
	secrets := &corev1.SecretList{}
	if err := storage.List(ctx, secrets, client.InNamespace(metav1.NamespaceSystem)); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("expected one Secret, got %d", len(secrets.Items))
	}
	key := client.ObjectKeyFromObject(&secrets.Items[0])
	if err := storage.Delete(ctx, request); err != nil {
		t.Fatal(err)
	}
	r := &controllers.BootstrapKubeconfigReconciler{Client: storage, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: request.Name, Namespace: request.Namespace}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := storage.Get(ctx, key, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("token Secret not revoked: %v", err)
	}
	if err := storage.Get(ctx, client.ObjectKeyFromObject(request), &infrav1.BootstrapKubeconfig{}); !apierrors.IsNotFound(err) {
		t.Fatalf("request finalizer not cleared: %v", err)
	}
}

func TestBootstrapTokenDeletionDoesNotRemoveUnownedSecret(t *testing.T) {
	ctx, storage, request, scheme := newTokenRevocationFixture(t)
	reconcileTokenRequest(t, ctx, storage, scheme, request)
	secrets := &corev1.SecretList{}
	if err := storage.List(ctx, secrets, client.InNamespace(metav1.NamespaceSystem)); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatal("expected one Secret")
	}
	secret := secrets.Items[0].DeepCopy()
	secret.Annotations["byoh.openkubes.io/bootstrap-request-uid"] = "different-uid"
	if err := storage.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if err := storage.Delete(ctx, request); err != nil {
		t.Fatal(err)
	}
	r := &controllers.BootstrapKubeconfigReconciler{Client: storage, Scheme: scheme}
	key := types.NamespacedName{Name: request.Name, Namespace: request.Namespace}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err == nil {
		t.Fatal("expected ownership check failure")
	}
	if err := storage.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{}); err != nil {
		t.Fatalf("foreign Secret deleted: %v", err)
	}
	stored := &infrav1.BootstrapKubeconfig{}
	if err := storage.Get(ctx, key, stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Finalizers) != 1 {
		t.Fatal("finalizer cleared despite failed credential revocation")
	}
}

var errSimulatedSecretDelete = errors.New("injected Secret deletion failure")

type failSecretDeleteClient struct {
	client.Client
	fail bool
}

func (c *failSecretDeleteClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, isSecret := obj.(*corev1.Secret); isSecret && c.fail {
		return errSimulatedSecretDelete
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func TestBootstrapTokenDeleteFailureRetainsFinalizer(t *testing.T) {
	ctx, storage, request, scheme := newTokenRevocationFixture(t)
	reconcileTokenRequest(t, ctx, storage, scheme, request)
	if err := storage.Delete(ctx, request); err != nil {
		t.Fatal(err)
	}
	wrapped := &failSecretDeleteClient{Client: storage, fail: true}
	r := &controllers.BootstrapKubeconfigReconciler{Client: wrapped, Scheme: scheme}
	key := types.NamespacedName{Name: request.Name, Namespace: request.Namespace}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); !errors.Is(err, errSimulatedSecretDelete) {
		t.Fatalf("expected injected Secret delete failure, got %v", err)
	}
	stored := &infrav1.BootstrapKubeconfig{}
	if err := storage.Get(ctx, key, stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Finalizers) != 1 {
		t.Fatal("finalizer removed before Secret revocation")
	}
	wrapped.fail = false
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Get(ctx, key, &infrav1.BootstrapKubeconfig{}); !apierrors.IsNotFound(err) {
		t.Fatalf("request not removed after successful retry: %v", err)
	}
}
