// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package admissiontest_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	noreplacement "github.com/openkubes/ok-baremetal-byoh/internal/safety/noreplacement"
	"github.com/openkubes/ok-baremetal-byoh/test/utils/crds"
)

const (
	inProgress  = "in-place-updates.internal.cluster.x-k8s.io/update-in-progress"
	receiveFrom = "in-place-updates.internal.cluster.x-k8s.io/receive-machines-from-machinesets"
	moveTo      = "in-place-updates.internal.cluster.x-k8s.io/move-machines-to-machineset"
	kcpManager  = "capi-kubeadmcontrolplane"
	approver    = "system:serviceaccount:okx-inplace-system:okx-upgrade-approver"
	initiator   = "system:serviceaccount:okx-inplace-system:okx-upgrade-initiator"
)

var byoMachineGVK = schema.GroupVersionKind{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1", Kind: "ByoMachine"}

// TestClusterAPIWrites sends, through a real API server with the deployed
// webhook configuration and the real grant reader, the requests that Cluster
// API v1.14.3 sends for an InfraMachine and a MachineSet in an in-place
// update, in their wire form (server-side apply, dry-run).
//
//nolint:gocyclo,funlen // one API server for all requests.
func TestClusterAPIWrites(t *testing.T) {
	core, err := crds.ClusterAPICoreDir()
	if err != nil {
		t.Fatal(err)
	}
	kcpCRDs, err := crds.ClusterAPIKubeadmControlPlaneDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "..", "..", "config")
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{core, kcpCRDs, filepath.Join(root, "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{Paths: []string{filepath.Join(root, "inplace", "base", "webhooks.yaml")}},
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, clusterv1.AddToScheme} {
		if err = add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	as := func(user string) client.Client {
		u := rest.CopyConfig(cfg)
		u.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:masters", "system:authenticated"}}
		k, err := client.New(u, client.Options{Scheme: scheme})
		must(err)
		return k
	}

	const ns = "ha1"
	must(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "ha1", Namespace: ns}, Spec: clusterv1.ClusterSpec{Paused: ptr(false)}}
	must(c.Create(ctx, cluster))
	k, d := kcp(ns), md(ns)
	must(c.Create(ctx, k))
	must(c.Create(ctx, d))
	yes := true
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-1", Namespace: ns, Labels: map[string]string{"cluster.x-k8s.io/cluster-name": "ha1"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "controlplane.cluster.x-k8s.io/v1beta2", Kind: "KubeadmControlPlane", Name: k.GetName(), UID: k.GetUID(), Controller: &yes}}},
		Spec: clusterv1.MachineSpec{ClusterName: "ha1", Version: "v1.36.6", Bootstrap: clusterv1.Bootstrap{DataSecretName: ptr("x")},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{APIGroup: byoMachineGVK.Group, Kind: byoMachineGVK.Kind, Name: "bm-cp-1"}},
	}
	must(c.Create(ctx, machine))
	// The InfraMachine as Cluster API applies it: spec, owner, labels and annotations.
	infra := func(annotations map[string]any, edit func(map[string]any)) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1", "kind": "ByoMachine",
			"metadata": map[string]any{"name": "bm-cp-1", "namespace": ns, "annotations": annotations,
				"labels":          map[string]any{"cluster.x-k8s.io/cluster-name": "ha1"},
				"ownerReferences": []any{map[string]any{"apiVersion": "cluster.x-k8s.io/v1beta2", "kind": "Machine", "name": machine.Name, "uid": string(machine.UID), "controller": true}}},
			"spec": map[string]any{},
		}}
		if edit != nil {
			edit(u.Object)
		}
		return u
	}
	cloned := map[string]any{"cluster.x-k8s.io/cloned-from-name": "ha1-cp"}
	apply := func(u *unstructured.Unstructured, opts ...client.PatchOption) error {
		data, err := json.Marshal(u.Object)
		must(err)
		return c.Patch(ctx, u, client.RawPatch(types.ApplyPatchType, data), append([]client.PatchOption{client.FieldOwner(kcpManager), client.ForceOwnership}, opts...)...)
	}
	must(apply(infra(cloned, nil)))
	source := &clusterv1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{Name: "ha1-md-0-old", Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "cluster.x-k8s.io/v1beta2", Kind: "MachineDeployment", Name: d.GetName(), UID: d.GetUID(), Controller: &yes}}},
		Spec: clusterv1.MachineSetSpec{ClusterName: "ha1", Replicas: ptr(int32(1)), Selector: metav1.LabelSelector{MatchLabels: map[string]string{"pool": "a"}},
			Template: clusterv1.MachineTemplateSpec{ObjectMeta: clusterv1.ObjectMeta{Labels: map[string]string{"pool": "a"}}, Spec: clusterv1.MachineSpec{
				ClusterName: "ha1", Version: "v1.36.6", Bootstrap: clusterv1.Bootstrap{DataSecretName: ptr("x")},
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{APIGroup: byoMachineGVK.Group, Kind: "ByoMachineTemplate", Name: "t"}}}},
	}
	must(c.Create(ctx, source))

	regs := map[string]noreplacement.NamespaceRegistration{ns: {Protected: true, ClusterName: "ha1", ClusterUID: string(cluster.UID)}}
	lookup := noreplacement.KubernetesClusterLookup(c)
	reader := noreplacement.KubernetesGrantReader{Reader: c}
	resolver := noreplacement.TargetResolver{Reader: c}
	opts := &env.WebhookInstallOptions
	server := webhook.NewServer(webhook.Options{Host: opts.LocalServingHost, Port: opts.LocalServingPort, CertDir: opts.LocalServingCertDir})
	server.Register(noreplacement.MachineAdmissionPath, &webhook.Admission{Handler: noreplacement.NewMachineAdmissionHandler(regs, lookup).WithGrantReader(reader).WithResolver(resolver)})
	server.Register(noreplacement.MachineSetAdmissionPath, &webhook.Admission{Handler: noreplacement.NewMachineSetAdmissionHandler(regs, lookup, reader)})
	server.Register(noreplacement.ByoMachineAdmissionPath, &webhook.Admission{Handler: noreplacement.NewByoMachineAdmissionHandler(regs, lookup, reader, resolver)})
	server.Register(noreplacement.IntentAdmissionPath, &webhook.Admission{Handler: noreplacement.NewIntentAdmissionHandler(regs, lookup, reader)})
	server.Register(noreplacement.NamespaceAdmissionPath, &webhook.Admission{Handler: noreplacement.NewNamespaceAdmissionHandler(regs)})
	server.Register(noreplacement.GrantConfigMapAdmissionPath, &webhook.Admission{Handler: noreplacement.NewGrantConfigMapAdmissionHandler(noreplacement.GuardActors{
		Approvers: map[string]struct{}{approver: {}}, Initiators: map[string]struct{}{initiator: {}},
	})})
	go func() { _ = server.Start(ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for server.StartedChecker()(nil) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the webhook server did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	must(c.Patch(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"`+noreplacement.ProtectedNamespaceLabel+`":"true"}}}`))))

	allowed := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Errorf("%s was denied: %v", what, err)
		}
	}
	denied := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "denied the request") {
			t.Errorf("%s: got %v, want an admission denial", what, err)
		}
	}
	grant := func(fixture, targetUID string) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "testdata", fixture))
		must(err)
		cm := &corev1.ConfigMap{}
		must(json.Unmarshal(raw, cm))
		cm.Namespace = ns
		cm.Data["clusterUID"], cm.Data["targetUID"] = string(cluster.UID), targetUID
		cm.Data["notAfter"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		must(as(approver).Create(ctx, cm))
		must(as(initiator).Patch(ctx, cm, client.RawPatch(types.MergePatchType, []byte(`{"data":{"claim":"c"}}`))))
	}
	marked := map[string]any{"cluster.x-k8s.io/cloned-from-name": "ha1-cp", inProgress: ""}
	msPatch := func(key, value string) error {
		return c.Patch(ctx, source.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"`+key+`":`+value+`}}}`)))
	}

	// No grant exists: the probes of CanUpdateMachine pass, nothing else does.
	allowed("the dry-run apply of the current InfraMachine (CanUpdateMachine)", apply(infra(cloned, nil), client.DryRunAll))
	allowed("the dry-run apply of the desired InfraMachine with new metadata", apply(infra(map[string]any{"cluster.x-k8s.io/cloned-from-name": "ha1-cp-new"}, nil), client.DryRunAll))
	denied("the dry-run apply of an InfraMachine with another spec", apply(infra(cloned, func(o map[string]any) { o["spec"] = map[string]any{"providerID": "byoh://other"} }), client.DryRunAll))
	allowed("the apply of an unchanged InfraMachine", apply(infra(cloned, nil)))
	denied("the apply of other metadata of the InfraMachine", apply(infra(map[string]any{"cluster.x-k8s.io/cloned-from-name": "ha1-cp-new"}, nil)))
	denied("the dry-run of the mark without a grant", apply(infra(marked, nil), client.DryRunAll))
	denied("the mark without a grant", apply(infra(marked, nil)))
	denied("the receive annotation without a grant", msPatch(receiveFrom, `"ha1-md-0-old"`))
	denied("the move annotation without a grant", msPatch(moveTo, `"ha1-md-0-new"`))

	// Under the grants: the mark, its retry, and the routing of the move.
	grant("rendered-grant-kcp.json", string(k.GetUID()))
	grant("rendered-grant-md.json", string(d.GetUID()))
	allowed("the mark of the InfraMachine (KCP-IM)", apply(infra(marked, nil)))
	allowed("the same apply again, when the trigger is retried", apply(infra(marked, nil)))
	allowed("the dry-run apply of the marked InfraMachine", apply(infra(marked, nil), client.DryRunAll))
	denied("a spec change of the InfraMachine under the grant", apply(infra(marked, func(o map[string]any) { o["spec"] = map[string]any{"providerID": "byoh://other"} })))
	allowed("the receive annotation under the grant", msPatch(receiveFrom, `"ha1-md-0-old"`))
	allowed("the move annotation under the grant", msPatch(moveTo, `"ha1-md-0-new"`))

	// The replacement fallback stays closed, with and without a dry-run.
	denied("Machine DELETE (fallback)", c.Delete(ctx, machine.DeepCopy()))
	denied("Machine DELETE (fallback, dry-run)", c.Delete(ctx, machine.DeepCopy(), client.DryRunAll))
	replacement := machine.DeepCopy()
	replacement.ObjectMeta = metav1.ObjectMeta{Name: "cp-new", Namespace: ns, Labels: machine.Labels, OwnerReferences: machine.OwnerReferences}
	denied("Machine CREATE (fallback)", c.Create(ctx, replacement))
	denied("ByoMachine DELETE", c.Delete(ctx, infra(nil, nil)))
}
