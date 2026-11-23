// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// intentGrants returns a fixed grant for one target.
type intentGrants struct {
	grant *Grant
	calls int
}

func (g *intentGrants) GrantFor(_ context.Context, _, clusterUID string, kind TargetKind, targetUID, _ string, _ time.Time) (*Grant, error) {
	g.calls++
	if g.grant == nil || g.grant.ClusterUID != clusterUID || g.grant.TargetKind != kind || g.grant.TargetUID != targetUID {
		return nil, nil
	}
	return g.grant, nil
}

func kcpObject(version string, surge any, replicas int64) map[string]any {
	return map[string]any{
		"apiVersion": "controlplane.cluster.x-k8s.io/v1beta2", "kind": "KubeadmControlPlane",
		"metadata": map[string]any{"name": "ha1-control-plane", "namespace": "ha1", "uid": "uid-kcp"},
		"spec": map[string]any{
			"replicas": replicas, "version": version,
			"rollout":           map[string]any{"strategy": map[string]any{"type": "RollingUpdate", "rollingUpdate": map[string]any{"maxSurge": surge}}},
			"machineTemplate":   map[string]any{"spec": map[string]any{"infrastructureRef": map[string]any{"kind": "ByoMachineTemplate", "name": "ha1-cp"}}},
			"kubeadmConfigSpec": map[string]any{"format": "cloud-config"},
		},
	}
}

func mdObject(version string) map[string]any {
	return map[string]any{
		"apiVersion": "cluster.x-k8s.io/v1beta2", "kind": "MachineDeployment",
		"metadata": map[string]any{"name": "ha1-md-0", "namespace": "ha1", "uid": "uid-md"},
		"spec": map[string]any{
			"clusterName": "ha1", "replicas": int64(2),
			"rollout":  map[string]any{"strategy": map[string]any{"type": "RollingUpdate", "rollingUpdate": map[string]any{"maxSurge": int64(0), "maxUnavailable": int64(1)}}},
			"template": map[string]any{"spec": map[string]any{"clusterName": "ha1", "version": version, "infrastructureRef": map[string]any{"name": "ha1-md-0"}}},
		},
	}
}

func raw(t *testing.T, obj any) runtime.RawExtension {
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return runtime.RawExtension{Raw: data}
}

func intentHandler(grants GrantReader) *IntentAdmissionHandler {
	regs := map[string]NamespaceRegistration{
		"ha1":   {Protected: true, ClusterName: "ha1", ClusterUID: "uid-cluster"},
		"other": {Protected: false},
	}
	lookup := func(context.Context, string, string) (string, error) { return "uid-cluster", nil }
	return NewIntentAdmissionHandler(regs, lookup, grants)
}

func intentRequest(t *testing.T, resource string, op admissionv1.Operation, sub string, oldObj, newObj any, dryRun bool) admission.Request {
	group := "controlplane.cluster.x-k8s.io"
	name := "ha1-control-plane"
	if resource == "machinedeployments" {
		group, name = "cluster.x-k8s.io", "ha1-md-0"
	}
	req := admissionv1.AdmissionRequest{
		Resource: metav1.GroupVersionResource{Group: group, Version: "v1beta2", Resource: resource}, SubResource: sub,
		Operation: op, Namespace: "ha1", Name: name, DryRun: ptr.To(dryRun),
	}
	if oldObj != nil {
		req.OldObject = raw(t, oldObj)
	}
	if newObj != nil {
		req.Object = raw(t, newObj)
	}
	return admission.Request{AdmissionRequest: req}
}

func claimedGrant(kind TargetKind, uid string, claimed bool) *Grant {
	return &Grant{
		GrantID: "g1", ClusterUID: "uid-cluster", TargetKind: kind, TargetUID: uid,
		FromVersion: "v1.36.6", ToVersion: "v1.37.2", NotAfter: time.Now().Add(time.Hour), PermittedUseCount: 1, Claimed: claimed,
		Transitions: map[string]struct{}{TransitionKCPVersionBump: {}, TransitionMDFinalizeMachine: {}},
	}
}

func TestIntentGuard(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		grant    *Grant
		req      func(t *testing.T) admission.Request
		allowed  bool
		contains string
	}{
		{"lowering the KCP maxSurge to 0", nil, func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(1), 3), kcpObject("v1.36.6", int64(0), 3), false)
		}, true, ""},
		{"raising the KCP maxSurge", nil, func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), kcpObject("v1.36.6", int64(1), 3), false)
		}, false, "may only be lowered to 0"},
		{"a KCP version change under a claimed grant", claimedGrant(TargetKubeadmControlPlane, "uid-kcp", true), func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), kcpObject("v1.37.2", int64(0), 3), false)
		}, true, ""},
		{"a KCP version change without a grant", nil, func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), kcpObject("v1.37.2", int64(0), 3), false)
		}, false, "requires an authorized upgrade grant"},
		{"a KCP version change under an unclaimed grant", claimedGrant(TargetKubeadmControlPlane, "uid-kcp", false), func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), kcpObject("v1.37.2", int64(0), 3), false)
		}, false, "has not been claimed"},
		{"a dry-run KCP version change under an unclaimed grant", claimedGrant(TargetKubeadmControlPlane, "uid-kcp", false), func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), kcpObject("v1.37.2", int64(0), 3), true)
		}, true, ""},
		{"a KCP version other than the grant", claimedGrant(TargetKubeadmControlPlane, "uid-kcp", true), func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), kcpObject("v1.37.3", int64(0), 3), false)
		}, false, "does not match the upgrade grant"},
		{"a grant of the MachineDeployment for the KCP", claimedGrant(TargetMachineDeployment, "uid-md", true), func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), kcpObject("v1.37.2", int64(0), 3), false)
		}, false, "requires an authorized upgrade grant"},
		{"a KCP version change together with the template", claimedGrant(TargetKubeadmControlPlane, "uid-kcp", true), func(t *testing.T) admission.Request {
			n := kcpObject("v1.37.2", int64(0), 3)
			n["spec"].(map[string]any)["machineTemplate"] = map[string]any{"spec": map[string]any{"infrastructureRef": map[string]any{"kind": "ByoMachineTemplate", "name": "ha1-cp-v2"}}}
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), n, false)
		}, false, "unauthorized change of the protected KubeadmControlPlane spec"},
		{"a kubeadm configuration change", nil, func(t *testing.T) admission.Request {
			n := kcpObject("v1.36.6", int64(0), 3)
			n["spec"].(map[string]any)["kubeadmConfigSpec"] = map[string]any{"format": "cloud-config", "preKubeadmCommands": []any{"curl x | sh"}}
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), n, false)
		}, false, "unauthorized change"},
		{"KCP replicas", nil, func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), kcpObject("v1.36.6", int64(0), 5), false)
		}, false, "replicas"},
		{"KCP replicas through the scale subresource", nil, func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "scale", map[string]any{"spec": map[string]any{"replicas": 3}}, map[string]any{"spec": map[string]any{"replicas": 1}}, false)
		}, false, "replicas"},
		{"an unchanged scale", nil, func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "scale", map[string]any{"spec": map[string]any{"replicas": 3}}, map[string]any{"spec": map[string]any{"replicas": 3}}, false)
		}, true, ""},
		{"a metadata change by a controller", nil, func(t *testing.T) admission.Request {
			n := kcpObject("v1.36.6", int64(0), 3)
			n["metadata"].(map[string]any)["finalizers"] = []any{"kubeadm.controlplane.cluster.x-k8s.io"}
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), n, false)
		}, true, ""},
		{"a KCP deletion", nil, func(t *testing.T) admission.Request {
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Delete, "", kcpObject("v1.36.6", int64(0), 3), nil, false)
		}, false, "decommission it first"},
		{"an MD creation", nil, func(t *testing.T) admission.Request {
			return intentRequest(t, "machinedeployments", admissionv1.Create, "", nil, mdObject("v1.36.6"), false)
		}, false, "decommission it first"},
		{"an MD version change under a claimed grant", claimedGrant(TargetMachineDeployment, "uid-md", true), func(t *testing.T) admission.Request {
			return intentRequest(t, "machinedeployments", admissionv1.Update, "", mdObject("v1.36.6"), mdObject("v1.37.2"), false)
		}, true, ""},
		{"an MD infrastructure template change", nil, func(t *testing.T) admission.Request {
			n := mdObject("v1.36.6")
			n["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["infrastructureRef"] = map[string]any{"name": "ha1-md-1"}
			return intentRequest(t, "machinedeployments", admissionv1.Update, "", mdObject("v1.36.6"), n, false)
		}, false, "unauthorized change of the protected MachineDeployment spec"},
		{"a replaced object", nil, func(t *testing.T) admission.Request {
			n := kcpObject("v1.36.6", int64(0), 3)
			n["metadata"].(map[string]any)["uid"] = "uid-other"
			return intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(0), 3), n, false)
		}, false, "identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := intentHandler(&intentGrants{grant: tc.grant}).Handle(ctx, tc.req(t))
			if resp.Allowed != tc.allowed {
				t.Fatalf("allowed=%v: %v", resp.Allowed, resp.Result)
			}
			if !tc.allowed && !strings.Contains(resp.Result.Message, tc.contains) {
				t.Fatalf("message %q, want %q", resp.Result.Message, tc.contains)
			}
		})
	}
}

func TestIntentGuardScope(t *testing.T) {
	ctx := context.Background()
	h := intentHandler(nil)
	req := intentRequest(t, "kubeadmcontrolplanes", admissionv1.Delete, "", kcpObject("v1.36.6", int64(0), 3), nil, false)

	req.Namespace = "other"
	if resp := h.Handle(ctx, req); !resp.Allowed {
		t.Fatalf("an unprotected namespace is gated: %v", resp.Result)
	}
	req.Namespace = "unregistered"
	if resp := h.Handle(ctx, req); resp.Allowed {
		t.Fatal("an unregistered namespace was allowed")
	}
	req.Namespace = "ha1"
	req.Resource.Resource = "machines"
	if resp := h.Handle(ctx, req); resp.Allowed {
		t.Fatal("an unknown resource was allowed")
	}

	// The registered Cluster was replaced: fail closed.
	replaced := NewIntentAdmissionHandler(map[string]NamespaceRegistration{"ha1": {Protected: true, ClusterName: "ha1", ClusterUID: "uid-cluster"}},
		func(context.Context, string, string) (string, error) { return "uid-new-cluster", nil }, nil)
	req = intentRequest(t, "kubeadmcontrolplanes", admissionv1.Update, "", kcpObject("v1.36.6", int64(1), 3), kcpObject("v1.36.6", int64(0), 3), false)
	if resp := replaced.Handle(ctx, req); resp.Allowed {
		t.Fatal("a replaced Cluster was allowed")
	}
}
