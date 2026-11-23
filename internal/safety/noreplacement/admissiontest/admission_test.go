// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package admissiontest runs the No-Replacement guard behind a real API server
// (envtest), with the ValidatingWebhookConfiguration of
// config/inplace/base/webhooks.yaml: namespace routing, the scale
// subresource, server-side dry-run and the fail-closed denials.
package admissiontest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	noreplacement "github.com/openkubes/ok-baremetal-byoh/internal/safety/noreplacement"
	"github.com/openkubes/ok-baremetal-byoh/test/utils/crds"
)

var (
	kcpGVK = schema.GroupVersionKind{Group: "controlplane.cluster.x-k8s.io", Version: "v1beta2", Kind: "KubeadmControlPlane"}
	mdGVK  = schema.GroupVersionKind{Group: "cluster.x-k8s.io", Version: "v1beta2", Kind: "MachineDeployment"}
)

// grants returns one grant for the KCP once it is set.
type grants struct{ grant *noreplacement.Grant }

func (g *grants) GrantFor(_ context.Context, _, clusterUID string, kind noreplacement.TargetKind, targetUID, _ string, _ time.Time) (*noreplacement.Grant, error) {
	if g.grant == nil || g.grant.ClusterUID != clusterUID || g.grant.TargetKind != kind || g.grant.TargetUID != targetUID {
		return nil, nil
	}
	return g.grant, nil
}

func object(gvk schema.GroupVersionKind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func kcp(ns string) *unstructured.Unstructured {
	return object(kcpGVK, ns, "ha1-control-plane", map[string]any{
		"version": "v1.36.6", "replicas": int64(3),
		"machineTemplate":   map[string]any{"spec": map[string]any{"infrastructureRef": map[string]any{"apiGroup": "infrastructure.cluster.x-k8s.io", "kind": "ByoMachineTemplate", "name": "ha1-cp"}}},
		"kubeadmConfigSpec": map[string]any{"format": "cloud-config"},
		"rollout":           map[string]any{"strategy": map[string]any{"type": "RollingUpdate", "rollingUpdate": map[string]any{"maxSurge": int64(1)}}},
	})
}

func md(ns string) *unstructured.Unstructured {
	return object(mdGVK, ns, "ha1-md-0", map[string]any{
		"clusterName": "ha1", "replicas": int64(2),
		"selector": map[string]any{"matchLabels": map[string]any{"pool": "md-0"}},
		"rollout":  map[string]any{"strategy": map[string]any{"type": "RollingUpdate", "rollingUpdate": map[string]any{"maxSurge": int64(0), "maxUnavailable": int64(1)}}},
		"template": map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"pool": "md-0"}},
			"spec": map[string]any{
				"clusterName": "ha1", "version": "v1.36.6",
				"bootstrap":         map[string]any{"configRef": map[string]any{"apiGroup": "bootstrap.cluster.x-k8s.io", "kind": "KubeadmConfigTemplate", "name": "ha1-md-0"}},
				"infrastructureRef": map[string]any{"apiGroup": "infrastructure.cluster.x-k8s.io", "kind": "ByoMachineTemplate", "name": "ha1-md-0"},
			},
		},
	})
}

func expectDenied(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "denied the request") || !strings.Contains(err.Error(), reason) {
		t.Fatalf("got %v, want a denial with %q", err, reason)
	}
}

func TestGuardBehindAPIServer(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	core, err := crds.ClusterAPICoreDir()
	if err != nil {
		t.Fatal(err)
	}
	kcpCRDs, err := crds.ClusterAPIKubeadmControlPlaneDir()
	if err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{core, kcpCRDs},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "..", "..", "config", "inplace", "base", "webhooks.yaml")},
		},
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, clusterv1.AddToScheme, autoscalingv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	// The objects exist before the namespace is protected, as on ha1.
	for _, ns := range []string{"ha1", "free"} {
		if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "ha1", Namespace: ns}, Spec: clusterv1.ClusterSpec{Paused: ptr(false)}}); err != nil {
			t.Fatal(err)
		}
		for _, obj := range []*unstructured.Unstructured{kcp(ns), md(ns)} {
			if err := c.Create(ctx, obj); err != nil {
				t.Fatalf("creating %s: %v", obj.GetKind(), err)
			}
		}
	}
	existing := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "ha1-control-plane-nt28b", Namespace: "ha1"},
		Spec: clusterv1.MachineSpec{
			ClusterName:       "ha1",
			Bootstrap:         clusterv1.Bootstrap{DataSecretName: ptr("x")},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{APIGroup: "infrastructure.cluster.x-k8s.io", Kind: "ByoMachine", Name: "x"},
		},
	}
	if err := c.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	cluster := &clusterv1.Cluster{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "ha1", Name: "ha1"}, cluster); err != nil {
		t.Fatal(err)
	}
	liveKCP := kcp("ha1")
	if err := c.Get(ctx, client.ObjectKeyFromObject(liveKCP), liveKCP); err != nil {
		t.Fatal(err)
	}

	regs := map[string]noreplacement.NamespaceRegistration{"ha1": {Protected: true, ClusterName: "ha1", ClusterUID: string(cluster.UID)}}
	lookup := noreplacement.KubernetesClusterLookup(c)
	g := &grants{}
	opts := &env.WebhookInstallOptions
	server := webhook.NewServer(webhook.Options{Host: opts.LocalServingHost, Port: opts.LocalServingPort, CertDir: opts.LocalServingCertDir})
	server.Register(noreplacement.MachineAdmissionPath, &webhook.Admission{Handler: noreplacement.NewMachineAdmissionHandler(regs, lookup)})
	server.Register(noreplacement.IntentAdmissionPath, &webhook.Admission{Handler: noreplacement.NewIntentAdmissionHandler(regs, lookup, g)})
	server.Register(noreplacement.NamespaceAdmissionPath, &webhook.Admission{Handler: noreplacement.NewNamespaceAdmissionHandler(regs)})
	server.Register(noreplacement.GrantConfigMapAdmissionPath, &webhook.Admission{Handler: noreplacement.NewGrantConfigMapAdmissionHandler(noreplacement.GuardActors{
		Approvers:  map[string]struct{}{"system:serviceaccount:okx-inplace-system:okx-upgrade-approver": {}},
		Initiators: map[string]struct{}{"system:serviceaccount:okx-inplace-system:okx-upgrade-initiator": {}},
	})})
	serverCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	go func() { _ = server.Start(serverCtx) }()
	deadline := time.Now().Add(20 * time.Second)
	for server.StartedChecker()(nil) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the webhook server did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}

	ns := &corev1.Namespace{}
	if err := c.Get(ctx, client.ObjectKey{Name: "ha1"}, ns); err != nil {
		t.Fatal(err)
	}
	ns.Labels = map[string]string{noreplacement.ProtectedNamespaceLabel: "true"}
	if err := c.Update(ctx, ns); err != nil {
		t.Fatal(err)
	}

	patch := func(obj *unstructured.Unstructured, data string, dryRun bool) error {
		opts := []client.PatchOption{}
		if dryRun {
			opts = append(opts, client.DryRunAll)
		}
		target := &unstructured.Unstructured{}
		target.SetGroupVersionKind(obj.GroupVersionKind())
		target.SetNamespace(obj.GetNamespace())
		target.SetName(obj.GetName())
		return c.Patch(ctx, target, client.RawPatch(types.MergePatchType, []byte(data)), opts...)
	}

	t.Run("an unprotected namespace is not routed", func(t *testing.T) {
		if err := patch(kcp("free"), `{"spec":{"replicas":5}}`, false); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("KCP replicas", func(t *testing.T) {
		expectDenied(t, patch(kcp("ha1"), `{"spec":{"replicas":5}}`, false), "replicas")
	})
	t.Run("KCP replicas through the scale subresource", func(t *testing.T) {
		scale := &autoscalingv1.Scale{Spec: autoscalingv1.ScaleSpec{Replicas: 1}}
		expectDenied(t, c.SubResource("scale").Update(ctx, liveKCP, client.WithSubResourceBody(scale)), "replicas")
	})
	t.Run("MD template", func(t *testing.T) {
		expectDenied(t, patch(md("ha1"), `{"spec":{"template":{"spec":{"infrastructureRef":{"name":"other"}}}}}`, false), "unauthorized change of the protected MachineDeployment spec")
	})
	t.Run("KCP version without a grant", func(t *testing.T) {
		expectDenied(t, patch(kcp("ha1"), `{"spec":{"version":"v1.37.2"}}`, false), "requires an authorized upgrade grant")
	})
	t.Run("raising maxSurge", func(t *testing.T) {
		expectDenied(t, patch(md("ha1"), `{"spec":{"rollout":{"strategy":{"rollingUpdate":{"maxSurge":1}}}}}`, false), "may only be lowered to 0")
	})
	t.Run("lowering the KCP maxSurge to 0", func(t *testing.T) {
		if err := patch(kcp("ha1"), `{"spec":{"rollout":{"strategy":{"rollingUpdate":{"maxSurge":0}}}}}`, false); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("KCP version under a grant", func(t *testing.T) {
		g.grant = &noreplacement.Grant{
			GrantID: "g1", ClusterUID: string(cluster.UID), TargetKind: noreplacement.TargetKubeadmControlPlane, TargetUID: string(liveKCP.GetUID()),
			FromVersion: "v1.36.6", ToVersion: "v1.37.2", NotAfter: time.Now().Add(time.Hour), PermittedUseCount: 1,
			Transitions: map[string]struct{}{noreplacement.TransitionKCPVersionBump: {}},
		}
		// Unclaimed: a server-side dry-run passes, the real write does not.
		if err := patch(kcp("ha1"), `{"spec":{"version":"v1.37.2"}}`, true); err != nil {
			t.Fatalf("dry-run: %v", err)
		}
		expectDenied(t, patch(kcp("ha1"), `{"spec":{"version":"v1.37.2"}}`, false), "has not been claimed")
		g.grant.Claimed = true
		expectDenied(t, patch(kcp("ha1"), `{"spec":{"version":"v1.37.3"}}`, false), "does not match the upgrade grant")
		if err := patch(kcp("ha1"), `{"spec":{"version":"v1.37.2"}}`, false); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("deleting the MachineDeployment", func(t *testing.T) {
		expectDenied(t, c.Delete(ctx, md("ha1")), "decommission it first")
	})
	t.Run("creating a Machine", func(t *testing.T) {
		m := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: "replacement", Namespace: "ha1"},
			Spec: clusterv1.MachineSpec{
				ClusterName:       "ha1",
				Bootstrap:         clusterv1.Bootstrap{DataSecretName: ptr("x")},
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{APIGroup: "infrastructure.cluster.x-k8s.io", Kind: "ByoMachine", Name: "x"},
			},
		}
		err := c.Create(ctx, m)
		if err == nil || !strings.Contains(err.Error(), "denied the request") {
			t.Fatalf("a replacement Machine was created: %v", err)
		}
	})
	t.Run("Q1: the routing label of a registered namespace cannot be removed", func(t *testing.T) {
		n := &corev1.Namespace{}
		_ = c.Get(ctx, client.ObjectKey{Name: "ha1"}, n)
		delete(n.Labels, noreplacement.ProtectedNamespaceLabel)
		expectDenied(t, c.Update(ctx, n), "cannot be removed or changed")
		_ = c.Get(ctx, client.ObjectKey{Name: "ha1"}, n)
		n.Labels[noreplacement.ProtectedNamespaceLabel] = "false"
		expectDenied(t, c.Update(ctx, n), "cannot be removed or changed")
	})
	t.Run("Q1: a registered namespace cannot be deleted", func(t *testing.T) {
		expectDenied(t, c.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ha1"}}), "decommission it first")
	})
	t.Run("Q1: the routing label is not set outside the registry", func(t *testing.T) {
		n := &corev1.Namespace{}
		_ = c.Get(ctx, client.ObjectKey{Name: "free"}, n)
		n.Labels = map[string]string{noreplacement.ProtectedNamespaceLabel: "true"}
		expectDenied(t, c.Update(ctx, n), "only on namespaces in the No-Replacement registry")
	})
	t.Run("Q2: a grant without the label is still guarded", func(t *testing.T) {
		forged := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "okx-grant-forged", Namespace: "ha1"}, Data: map[string]string{"grantID": "x"}}
		expectDenied(t, c.Create(ctx, forged), "approver")
		if err := c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ordinary", Namespace: "ha1"}}); err != nil {
			t.Fatalf("an ordinary ConfigMap was refused: %v", err)
		}
	})
	t.Run("a controller fallback deletion is denied despite full RBAC", func(t *testing.T) {
		asController := rest.CopyConfig(cfg)
		asController.Impersonate = rest.ImpersonationConfig{
			UserName: "system:serviceaccount:capi-kubeadm-control-plane-system:capi-kubeadm-control-plane-manager",
			Groups:   []string{"system:masters", "system:serviceaccounts", "system:authenticated"},
		}
		controller, err := client.New(asController, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		expectDenied(t, controller.Delete(ctx, existing.DeepCopy()), "")
		m := &clusterv1.Machine{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(existing), m); err != nil || m.UID != existing.UID || m.DeletionTimestamp != nil {
			t.Fatalf("the Machine changed: %v", err)
		}
	})
	t.Run("a webhook outage fails closed", func(t *testing.T) {
		stopServer()
		time.Sleep(500 * time.Millisecond)
		err := patch(kcp("ha1"), `{"spec":{"rollout":{"strategy":{"rollingUpdate":{"maxSurge":0}}}}}`, false)
		if err == nil || !strings.Contains(err.Error(), "failed calling webhook") {
			t.Fatalf("got %v, want a failed webhook call", err)
		}
		if err := patch(kcp("free"), `{"spec":{"replicas":3}}`, false); err != nil {
			t.Fatalf("an unprotected namespace depends on the guard: %v", err)
		}
	})
}

func ptr[T any](v T) *T { return &v }
