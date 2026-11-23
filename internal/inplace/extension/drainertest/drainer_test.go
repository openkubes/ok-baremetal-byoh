// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package drainertest checks the least-privilege drainer identity of
// config/inplace/workload/drainer.yaml behind a real API server (envtest):
// what the extension needs works, everything else is refused.
package drainertest_test

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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/openkubes/ok-baremetal-byoh/internal/inplace/extension"
)

const drainerUser = "system:serviceaccount:kube-system:okx-inplace-drainer"

func applyManifest(t *testing.T, c client.Client, path string) {
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
		if len(u.Object) == 0 {
			continue
		}
		if err := c.Create(context.Background(), u); err != nil {
			t.Fatalf("creating %s %s: %v", u.GetKind(), u.GetName(), err)
		}
	}
}

//nolint:gocyclo,funlen // one envtest API server for all permission cases.
func TestDrainerIdentity(t *testing.T) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	ctx := context.Background()
	admin, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	applyManifest(t, admin, filepath.Join("..", "..", "..", "..", "config", "inplace", "workload", "drainer.yaml"))

	cs := kubernetes.NewForConfigOrDie(cfg)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{"role": "worker"}}}
	if _, err := cs.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	node.Status.NodeInfo.KubeletVersion = "v1.37.2"
	if _, err := cs.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "uid-rs", Controller: ptr.To(true)}}},
		Spec:       corev1.PodSpec{NodeName: "node1", Containers: []corev1.Container{{Name: "c", Image: "x"}}},
	}
	if _, err := cs.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Secrets("default").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	asDrainer := rest.CopyConfig(cfg)
	asDrainer.Impersonate = rest.ImpersonationConfig{UserName: drainerUser, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:kube-system", "system:authenticated"}}
	drainer := kubernetes.NewForConfigOrDie(asDrainer)
	nodes := extension.WorkloadNodes{Clientset: func(context.Context, client.ObjectKey) (kubernetes.Interface, error) { return drainer, nil }}
	key := client.ObjectKey{Namespace: "ha1", Name: "ha1"}

	// RBAC and the admission policy are in effect after a short delay.
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := drainer.CoreV1().Nodes().Patch(ctx, "node1", types.MergePatchType, []byte(`{"metadata":{"labels":{"role":"control-plane"}}}`), metav1.PatchOptions{DryRun: []string{"All"}})
		if err != nil && strings.Contains(err.Error(), "may only change spec.unschedulable") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the admission policy is not in effect: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Run("drains with evictions", func(t *testing.T) {
		err := nodes.Drain(ctx, key, "node1")
		// Without a kubelet the evicted pod stays terminating: the first pass
		// reports it, which proves the eviction was admitted.
		if err == nil || !strings.Contains(err.Error(), "still on Node node1") {
			t.Fatalf("drain: %v", err)
		}
		p, err := cs.CoreV1().Pods("default").Get(ctx, "app", metav1.GetOptions{})
		if err == nil && p.DeletionTimestamp == nil {
			t.Fatal("the pod was not evicted")
		}
		n, _ := cs.CoreV1().Nodes().Get(ctx, "node1", metav1.GetOptions{})
		if !n.Spec.Unschedulable {
			t.Fatal("not cordoned")
		}
	})
	t.Run("uncordons and reads the kubelet version", func(t *testing.T) {
		if err := nodes.Uncordon(ctx, key, "node1"); err != nil {
			t.Fatal(err)
		}
		if v, err := nodes.KubeletVersion(ctx, key, "node1"); err != nil || v != "v1.37.2" {
			t.Fatalf("%q %v", v, err)
		}
	})
	for name, attempt := range map[string]func() error{
		"reading a Secret": func() error {
			_, err := drainer.CoreV1().Secrets("default").Get(ctx, "s", metav1.GetOptions{})
			return err
		},
		"deleting a pod": func() error {
			return drainer.CoreV1().Pods("default").Delete(ctx, "app", metav1.DeleteOptions{})
		},
		"deleting a Node": func() error {
			return drainer.CoreV1().Nodes().Delete(ctx, "node1", metav1.DeleteOptions{})
		},
		"creating a pod": func() error {
			_, err := drainer.CoreV1().Pods("default").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "x"}}}}, metav1.CreateOptions{})
			return err
		},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			if err := attempt(); !apierrors.IsForbidden(err) {
				t.Fatalf("got %v, want Forbidden", err)
			}
		})
	}
	for name, patch := range map[string]string{
		"a Node label":      `{"metadata":{"labels":{"role":"control-plane"}}}`,
		"a Node annotation": `{"metadata":{"annotations":{"x":"y"}}}`,
		"a Node taint":      `{"spec":{"taints":[{"key":"k","effect":"NoSchedule"}]}}`,
		"the provider ID":   `{"spec":{"providerID":"byoh://other"}}`,
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := drainer.CoreV1().Nodes().Patch(ctx, "node1", types.MergePatchType, []byte(patch), metav1.PatchOptions{})
			if err == nil || !strings.Contains(err.Error(), "may only change spec.unschedulable") {
				t.Fatalf("got %v", err)
			}
		})
	}
	t.Run("the extension reads only the drainer Secret", func(t *testing.T) {
		mgmt := extension.WorkloadNodes{Reader: admin}
		_, err := mgmt.KubeletVersion(ctx, key, "node1")
		if err == nil || !strings.Contains(err.Error(), "ha1/ha1-inplace-drainer") {
			t.Fatalf("got %v", err)
		}
	})
}
