// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package hardeningtest checks config/inplace/base/hardening.yaml with the
// identities of config/inplace/overlays/ha1 behind a real API server (envtest):
// token minting for the grant identities (S4) and writes of the registry, the
// package lock, the identities and the Deployments (S5) are limited to the
// authorized identities, also for members of system:masters.
package hardeningtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const ns = "okx-inplace-system"

func apply(t *testing.T, c client.Client, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		u := &unstructured.Unstructured{}
		if err := dec.Decode(&u.Object); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatal(err)
		}
		if len(u.Object) > 0 {
			if err := c.Create(context.Background(), u); err != nil {
				t.Fatalf("creating %s %s: %v", u.GetKind(), u.GetName(), err)
			}
		}
	}
}

func as(t *testing.T, cfg *rest.Config, user string) kubernetes.Interface {
	c := rest.CopyConfig(cfg)
	c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:masters", "system:authenticated"}}
	return kubernetes.NewForConfigOrDie(c)
}

func denied(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("got %v, want a denial with %q", err, text)
	}
}

func TestHardening(t *testing.T) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, sa := range []string{"okx-upgrade-approver", "okx-upgrade-initiator"} {
		if _, err := cs.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join("..", "..", "..", "..", "config", "inplace")
	// The identities exist before the policies (deployment order).
	apply(t, c, filepath.Join(root, "overlays", "ha1", "authorized-identities.yaml"))
	for _, name := range []string{"okx-no-replacement-registry-abc", "okx-inplace-packages-abc", "ordinary"} {
		if _, err := cs.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	guard := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "no-replacement-guard", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "g"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "g"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "g", Image: "x"}}}},
		},
	}
	if _, err := cs.AppsV1().Deployments(ns).Create(ctx, guard, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	apply(t, c, filepath.Join(root, "base", "hardening.yaml"))

	admin, mallory := as(t, cfg, "kubernetes-admin"), as(t, cfg, "mallory")
	token := func(k kubernetes.Interface, sa string) error {
		_, err := k.CoreV1().ServiceAccounts(ns).CreateToken(ctx, sa, &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To(int64(600))}}, metav1.CreateOptions{DryRun: []string{"All"}})
		return err
	}
	// The policies are in effect after a short delay.
	deadline := time.Now().Add(30 * time.Second)
	for token(mallory, "okx-upgrade-approver") == nil {
		if time.Now().After(deadline) {
			t.Fatal("the policies are not in effect")
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Run("S4: an unauthorized member of system:masters mints no grant token", func(t *testing.T) {
		denied(t, token(mallory, "okx-upgrade-approver"), "minted only by its authorized identities, not mallory")
		denied(t, token(mallory, "okx-upgrade-initiator"), "minted only by its authorized identities, not mallory")
	})
	t.Run("S4: the authorized identity mints them", func(t *testing.T) {
		for _, sa := range []string{"okx-upgrade-approver", "okx-upgrade-initiator"} {
			if err := token(admin, sa); err != nil {
				t.Fatalf("%s: %v", sa, err)
			}
		}
	})
	t.Run("S4: no pod runs as a grant identity", func(t *testing.T) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Spec: corev1.PodSpec{ServiceAccountName: "okx-upgrade-initiator", Containers: []corev1.Container{{Name: "c", Image: "x"}}}}
		_, err := admin.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{DryRun: []string{"All"}})
		denied(t, err, "no pod runs as the upgrade approver or initiator")
	})
	t.Run("S4: no long-lived token Secret for a grant identity", func(t *testing.T) {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "t", Annotations: map[string]string{"kubernetes.io/service-account.name": "okx-upgrade-approver"}}, Type: corev1.SecretTypeServiceAccountToken}
		_, err := admin.CoreV1().Secrets(ns).Create(ctx, s, metav1.CreateOptions{DryRun: []string{"All"}})
		denied(t, err, "no long-lived token Secret")
	})
	patchCM := func(k kubernetes.Interface, name string) error {
		_, err := k.CoreV1().ConfigMaps(ns).Patch(ctx, name, types.MergePatchType, []byte(`{"data":{"x":"y"}}`), metav1.PatchOptions{DryRun: []string{"All"}})
		return err
	}
	for _, name := range []string{"okx-no-replacement-registry-abc", "okx-inplace-packages-abc", "okx-inplace-authorized-identities"} {
		t.Run("S5: an unauthorized member of system:masters does not write "+name, func(t *testing.T) {
			denied(t, patchCM(mallory, name), "written only by platform administrators, not mallory")
			denied(t, mallory.CoreV1().ConfigMaps(ns).Delete(ctx, name, metav1.DeleteOptions{DryRun: []string{"All"}}), "platform administrators")
			if err := patchCM(admin, name); err != nil {
				t.Fatalf("the platform administrator: %v", err)
			}
		})
	}
	t.Run("S5: a new registry ConfigMap is created only by a platform administrator", func(t *testing.T) {
		_, err := mallory.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "okx-no-replacement-registry-evil"}}, metav1.CreateOptions{DryRun: []string{"All"}})
		denied(t, err, "platform administrators")
	})
	t.Run("S5: the guard Deployment is changed only by a platform administrator", func(t *testing.T) {
		patch := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"g","env":[{"name":"BYOH_GRANT_APPROVER_USERS","value":"mallory"}]}]}}}}`)
		_, err := mallory.AppsV1().Deployments(ns).Patch(ctx, "no-replacement-guard", types.StrategicMergePatchType, patch, metav1.PatchOptions{DryRun: []string{"All"}})
		denied(t, err, "changed only by platform administrators, not mallory")
		if _, err := admin.AppsV1().Deployments(ns).Patch(ctx, "no-replacement-guard", types.StrategicMergePatchType, patch, metav1.PatchOptions{DryRun: []string{"All"}}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ordinary pods, Secrets and tokens are not affected", func(t *testing.T) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "guard-pod"}, Spec: corev1.PodSpec{ServiceAccountName: "no-replacement-guard", Containers: []corev1.Container{{Name: "c", Image: "x"}}}}
		replicaSetController := as(t, cfg, "system:serviceaccount:kube-system:replicaset-controller")
		if _, err := replicaSetController.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{DryRun: []string{"All"}}); err != nil {
			t.Fatalf("a guard pod: %v", err)
		}
		if _, err := mallory.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls"}}, metav1.CreateOptions{DryRun: []string{"All"}}); err != nil {
			t.Fatalf("an ordinary Secret: %v", err)
		}
	})
	t.Run("ordinary ConfigMaps are not affected", func(t *testing.T) {
		if err := patchCM(mallory, "ordinary"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("without the identities everything matching is denied", func(t *testing.T) {
		if err := admin.CoreV1().ConfigMaps(ns).Delete(ctx, "okx-inplace-authorized-identities", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			err := token(admin, "okx-upgrade-approver")
			if err != nil && strings.Contains(err.Error(), "no params found") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("still admitted without the identities: %v", err)
			}
			time.Sleep(200 * time.Millisecond)
		}
		denied(t, patchCM(admin, "okx-no-replacement-registry-abc"), "no params found")
	})
}
