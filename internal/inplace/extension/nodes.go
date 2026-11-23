// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package extension

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// mirrorPodAnnotation marks the mirror pod of a static pod.
const mirrorPodAnnotation = "kubernetes.io/config.mirror"

// DrainerSecretSuffix names the management Secret <cluster>-inplace-drainer
// with the kubeconfig (key "value") of the least-privilege drainer identity in
// the workload cluster (config/inplace/workload/drainer.yaml). The admin
// kubeconfig of the cluster is never used.
const DrainerSecretSuffix = "-inplace-drainer"

// WorkloadNodes reaches the Nodes of a workload cluster with the drainer
// kubeconfig, which RBAC gives the extension by name.
type WorkloadNodes struct {
	Reader client.Reader
	// Clientset overrides the clientset of the workload cluster, for tests.
	Clientset func(ctx context.Context, cluster client.ObjectKey) (kubernetes.Interface, error)
}

func (w WorkloadNodes) clientset(ctx context.Context, cluster client.ObjectKey) (kubernetes.Interface, error) {
	if w.Clientset != nil {
		return w.Clientset(ctx, cluster)
	}
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name + DrainerSecretSuffix}
	if err := w.Reader.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("reading the drainer kubeconfig %s: %w", key, err)
	}
	data := secret.Data["value"]
	if len(data) == 0 {
		return nil, fmt.Errorf("the drainer kubeconfig %s has no value", key)
	}
	config, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return nil, err
	}
	config.Timeout = 30 * time.Second
	return kubernetes.NewForConfig(config)
}

// Drain implements NodeAccess. It cordons the Node and asks for the eviction
// of every pod that a drain moves, and succeeds only when none is left. Each
// call is one pass, so the UpdateMachine retries are the drain loop: a
// PodDisruptionBudget that blocks an eviction or a pod that still terminates
// is an error for this pass. A pod without a controller stops the drain, as
// it would be lost.
func (w WorkloadNodes) Drain(ctx context.Context, cluster client.ObjectKey, node string) error {
	cs, err := w.clientset(ctx, cluster)
	if err != nil {
		return err
	}
	if err := setUnschedulable(ctx, cs, node, true); err != nil {
		return err
	}
	pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", node).String(),
	})
	if err != nil {
		return err
	}
	left := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		evict, err := drainsPod(pod)
		if err != nil {
			return err
		}
		if !evict {
			continue
		}
		left++
		if pod.DeletionTimestamp != nil {
			continue
		}
		eviction := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}}
		if err := cs.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("evicting pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
	}
	if left > 0 {
		return fmt.Errorf("%d pods are still on Node %s", left, node)
	}
	return nil
}

// drainsPod reports whether a drain evicts the pod. DaemonSet pods and the
// mirror pods of static pods stay; finished pods do not matter; a pod without
// a controller is an error.
func drainsPod(pod *corev1.Pod) (bool, error) {
	if _, ok := pod.Annotations[mirrorPodAnnotation]; ok {
		return false, nil
	}
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false, nil
	}
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return false, fmt.Errorf("pod %s/%s has no controller and would be lost", pod.Namespace, pod.Name)
	}
	return owner.Kind != "DaemonSet", nil
}

func setUnschedulable(ctx context.Context, cs kubernetes.Interface, node string, unschedulable bool) error {
	n, err := cs.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if n.Spec.Unschedulable == unschedulable {
		return nil
	}
	patch := fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, unschedulable)
	_, err = cs.CoreV1().Nodes().Patch(ctx, node, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// Uncordon implements NodeAccess.
func (w WorkloadNodes) Uncordon(ctx context.Context, cluster client.ObjectKey, node string) error {
	cs, err := w.clientset(ctx, cluster)
	if err != nil {
		return err
	}
	return setUnschedulable(ctx, cs, node, false)
}

// KubeletVersion implements NodeAccess.
func (w WorkloadNodes) KubeletVersion(ctx context.Context, cluster client.ObjectKey, node string) (string, error) {
	cs, err := w.clientset(ctx, cluster)
	if err != nil {
		return "", err
	}
	n, err := cs.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return n.Status.NodeInfo.KubeletVersion, nil
}
