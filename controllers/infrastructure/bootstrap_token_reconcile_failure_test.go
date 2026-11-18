// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
    "context"
    "errors"
    "testing"

    infrastructurev1beta1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
    controllers "github.com/openkubes/ok-baremetal-byoh/controllers/infrastructure"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/types"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/client/fake"
    "sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var errInjectedStatusPatch = errors.New("injected status patch failure")

// statusPatchFailureClient intercepts only status patches; Secret creation and
// all reads are persisted by the underlying fake API. A new Reconciler is
// constructed for each retry to model loss of any in-memory state.
type statusPatchFailureClient struct {
    client.Client
    remaining int
}

type failingStatusWriter struct {
    client.SubResourceWriter
    remaining *int
}

func (w failingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
    if *w.remaining > 0 {
        *w.remaining = *w.remaining - 1
        return errInjectedStatusPatch
    }
    return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func (c *statusPatchFailureClient) Status() client.SubResourceWriter {
    return failingStatusWriter{SubResourceWriter: c.Client.Status(), remaining: &c.remaining}
}

func TestBootstrapKubeconfigReconcileStatusPatchFailureDoesNotMultiplyTokens(t *testing.T) {
    ctx := context.Background()
    scheme := runtime.NewScheme()
    if err := corev1.AddToScheme(scheme); err != nil {
        t.Fatal(err)
    }
    if err := infrastructurev1beta1.AddToScheme(scheme); err != nil {
        t.Fatal(err)
    }
    request := &infrastructurev1beta1.BootstrapKubeconfig{
        ObjectMeta: metav1.ObjectMeta{
            Name: "enroll",
            Namespace: "default",
            UID: types.UID("enroll-uid-1"),
        },
        Spec: infrastructurev1beta1.BootstrapKubeconfigSpec{
            APIServer: "https://127.0.0.1:6443",
            CertificateAuthorityData: "dGVzdC1jYQ==",
        },
    }
    storage := fake.NewClientBuilder().
        WithScheme(scheme).
        WithStatusSubresource(&infrastructurev1beta1.BootstrapKubeconfig{}).
        WithObjects(request).
        Build()
    broken := &statusPatchFailureClient{Client: storage, remaining: 5}
    key := types.NamespacedName{Namespace: request.Namespace, Name: request.Name}
    for i := 0; i < 6; i++ {
        r := &controllers.BootstrapKubeconfigReconciler{Client: broken, Scheme: scheme}
        _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
        if i < 5 && !errors.Is(err, errInjectedStatusPatch) {
            t.Fatalf("retry %d expected injected failure, got %v", i, err)
        }
        if i == 5 && err != nil {
            t.Fatalf("recovery failed: %v", err)
        }
        secrets := &corev1.SecretList{}
        if err := storage.List(ctx, secrets, client.InNamespace(metav1.NamespaceSystem)); err != nil {
            t.Fatal(err)
        }
        if len(secrets.Items) != 1 {
            t.Fatalf("retry %d: expected exactly one token Secret, got %d", i, len(secrets.Items))
        }
    }
    stored := &infrastructurev1beta1.BootstrapKubeconfig{}
    if err := storage.Get(ctx, key, stored); err != nil {
        t.Fatal(err)
    }
    if stored.Status.BootstrapKubeconfigData == nil {
        t.Fatal("successful retry did not persist bootstrap kubeconfig status")
    }
    r := &controllers.BootstrapKubeconfigReconciler{Client: storage, Scheme: scheme}
    if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}); err != nil {
        t.Fatal(err)
    }
    secrets := &corev1.SecretList{}
    if err := storage.List(ctx, secrets, client.InNamespace(metav1.NamespaceSystem)); err != nil {
        t.Fatal(err)
    }
    if len(secrets.Items) != 1 {
        t.Fatalf("post-success reconcile multiplied tokens: %d", len(secrets.Items))
    }
}
