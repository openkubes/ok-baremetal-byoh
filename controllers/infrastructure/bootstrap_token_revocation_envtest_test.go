// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	controllers "github.com/openkubes/ok-baremetal-byoh/controllers/infrastructure"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("Bootstrap token revocation against envtest API server", func() {
	var (
		ctx     context.Context
		api     client.Client
		request *infrav1.BootstrapKubeconfig
		rec     *controllers.BootstrapKubeconfigReconciler
		key     client.ObjectKey
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		api, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		rec = &controllers.BootstrapKubeconfigReconciler{Client: api, Scheme: scheme.Scheme}
		request = &infrav1.BootstrapKubeconfig{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "revocation-envtest-", Namespace: "default"},
			Spec: infrav1.BootstrapKubeconfigSpec{
				APIServer:                "https://127.0.0.1:6443",
				CertificateAuthorityData: "dGVzdC1jYQ==",
			},
		}
		Expect(api.Create(ctx, request)).To(Succeed())
		key = client.ObjectKeyFromObject(request)
	})

	AfterEach(func() {
		// Best-effort cleanup only for requests the test has not already deleted.
		current := &infrav1.BootstrapKubeconfig{}
		if err := api.Get(ctx, key, current); apierrors.IsNotFound(err) {
			return
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
		if current.DeletionTimestamp.IsZero() {
			Expect(api.Delete(ctx, current)).To(Succeed())
		}
		_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			err := api.Get(ctx, key, &infrav1.BootstrapKubeconfig{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("persists a finalizer before issuing a token and revokes it on deletion", func() {
		_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		withFinalizer := &infrav1.BootstrapKubeconfig{}
		Expect(api.Get(ctx, key, withFinalizer)).To(Succeed())
		Expect(withFinalizer.Finalizers).To(ContainElement("infrastructure.cluster.x-k8s.io/bootstrap-token-cleanup"))
		digest := sha256.Sum256([]byte(withFinalizer.UID))
		tokenID := hex.EncodeToString(digest[:])[:6]
		secretKey := client.ObjectKey{Namespace: metav1.NamespaceSystem, Name: "bootstrap-token-" + tokenID}
		Expect(api.Get(ctx, secretKey, &corev1.Secret{})).To(Satisfy(apierrors.IsNotFound))

		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		secret := &corev1.Secret{}
		Expect(api.Get(ctx, secretKey, secret)).To(Succeed())
		Expect(secret.Annotations["byoh.openkubes.io/bootstrap-request-uid"]).To(Equal(string(request.UID)))

		Expect(api.Delete(ctx, withFinalizer)).To(Succeed())
		terminating := &infrav1.BootstrapKubeconfig{}
		Expect(api.Get(ctx, key, terminating)).To(Succeed())
		Expect(terminating.DeletionTimestamp.IsZero()).To(BeFalse())

		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(api.Get(ctx, secretKey, &corev1.Secret{})).To(Satisfy(apierrors.IsNotFound))
		Eventually(func() bool {
			err := api.Get(ctx, key, &infrav1.BootstrapKubeconfig{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("keeps a request terminating when the token Secret has foreign ownership", func() {
		_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		digest := sha256.Sum256([]byte(request.UID))
		tokenID := hex.EncodeToString(digest[:])[:6]
		secretKey := client.ObjectKey{Namespace: metav1.NamespaceSystem, Name: "bootstrap-token-" + tokenID}
		secret := &corev1.Secret{}
		Expect(api.Get(ctx, secretKey, secret)).To(Succeed())
		secret.Annotations["byoh.openkubes.io/bootstrap-request-uid"] = "foreign-request-uid"
		Expect(api.Update(ctx, secret)).To(Succeed())

		current := &infrav1.BootstrapKubeconfig{}
		Expect(api.Get(ctx, key, current)).To(Succeed())
		Expect(api.Delete(ctx, current)).To(Succeed())
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(HaveOccurred())
		Expect(api.Get(ctx, secretKey, &corev1.Secret{})).To(Succeed())
		Expect(api.Get(ctx, key, current)).To(Succeed())
		Expect(current.DeletionTimestamp.IsZero()).To(BeFalse())

		// Restore the correct ownership to exercise a safe retry and cleanup.
		Expect(api.Get(ctx, secretKey, secret)).To(Succeed())
		secret.Annotations["byoh.openkubes.io/bootstrap-request-uid"] = string(request.UID)
		Expect(api.Update(ctx, secret)).To(Succeed())
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(api.Get(ctx, secretKey, &corev1.Secret{})).To(Satisfy(apierrors.IsNotFound))
	})
})
