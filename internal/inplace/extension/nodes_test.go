// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package extension

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func pod(name string, owner string, annotations map[string]string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Annotations: annotations},
		Spec:       corev1.PodSpec{NodeName: "node1"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if owner != "" {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: owner, Name: "owner", Controller: ptr.To(true)}}
	}
	return p
}

func workload(objs ...runtime.Object) (*kfake.Clientset, WorkloadNodes) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.37.2"}}}
	cs := kfake.NewClientset(append([]runtime.Object{node}, objs...)...)
	return cs, WorkloadNodes{Clientset: func(context.Context, client.ObjectKey) (kubernetes.Interface, error) { return cs, nil }}
}

func evictions(cs *kfake.Clientset) []string {
	var names []string
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" && a.GetSubresource() == "eviction" {
			names = append(names, a.(k8stesting.CreateAction).GetObject().(metav1.Object).GetName())
		}
	}
	return names
}

func TestDrain(t *testing.T) {
	ctx, key := context.Background(), client.ObjectKey{Namespace: "ha1", Name: "ha1"}

	t.Run("cordons and evicts only what a drain moves", func(t *testing.T) {
		cs, w := workload(
			pod("app", "ReplicaSet", nil),
			pod("cilium", "DaemonSet", nil),
			pod("kube-apiserver-node1", "Node", map[string]string{mirrorPodAnnotation: "x"}),
		)
		// The evicted pod is gone at the next pass.
		cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
			if a.GetSubresource() == "eviction" {
				return true, nil, cs.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "default", "app")
			}
			return false, nil, nil
		})
		if err := w.Drain(ctx, key, "node1"); err == nil || !strings.Contains(err.Error(), "1 pods are still") {
			t.Fatalf("first pass: %v", err)
		}
		if got := evictions(cs); len(got) != 1 || got[0] != "app" {
			t.Fatalf("evicted %v", got)
		}
		if err := w.Drain(ctx, key, "node1"); err != nil {
			t.Fatalf("second pass: %v", err)
		}
		n, _ := cs.CoreV1().Nodes().Get(ctx, "node1", metav1.GetOptions{})
		if !n.Spec.Unschedulable {
			t.Fatal("not cordoned")
		}
		if err := w.Uncordon(ctx, key, "node1"); err != nil {
			t.Fatal(err)
		}
		n, _ = cs.CoreV1().Nodes().Get(ctx, "node1", metav1.GetOptions{})
		if n.Spec.Unschedulable {
			t.Fatal("not uncordoned")
		}
	})

	t.Run("stops at a pod without a controller", func(t *testing.T) {
		cs, w := workload(pod("bare", "", nil))
		if err := w.Drain(ctx, key, "node1"); err == nil || !strings.Contains(err.Error(), "has no controller") {
			t.Fatalf("error %v", err)
		}
		if len(evictions(cs)) != 0 {
			t.Fatal("evicted")
		}
	})

	t.Run("reports a PodDisruptionBudget", func(t *testing.T) {
		cs, w := workload(pod("app", "ReplicaSet", nil))
		cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 10)
		})
		if err := w.Drain(ctx, key, "node1"); err == nil || !strings.Contains(err.Error(), "disruption budget") {
			t.Fatalf("error %v", err)
		}
	})

	t.Run("reads the kubelet version", func(t *testing.T) {
		_, w := workload()
		if v, err := w.KubeletVersion(ctx, key, "node1"); err != nil || v != "v1.37.2" {
			t.Fatalf("%q %v", v, err)
		}
		if _, err := w.KubeletVersion(ctx, key, "node2"); !apierrors.IsNotFound(err) {
			t.Fatalf("%v", err)
		}
	})

	t.Run("fails without the workload kubeconfig", func(t *testing.T) {
		w := WorkloadNodes{Clientset: func(context.Context, client.ObjectKey) (kubernetes.Interface, error) {
			return nil, errors.New("secret ha1-inplace-drainer not found")
		}}
		if err := w.Drain(ctx, key, "node1"); err == nil {
			t.Fatal("no error")
		}
	})
}
