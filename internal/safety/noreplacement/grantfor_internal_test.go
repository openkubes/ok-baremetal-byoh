// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var grantNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func grantCM(name, machineUID string, notAfter time.Time, claim string) *corev1.ConfigMap {
	d := map[string]string{
		"grantID": name, "clusterUID": "c-uid", "targetKind": string(TargetKubeadmControlPlane),
		"targetName": "kcp", "targetUID": "kcp-uid", "machineUID": machineUID,
		"fromVersion": "v1.33.0", "toVersion": "v1.34.0", "transitions": "KCP-1,KCP-2",
		"payloadDigest": "sha256:p", "notAfter": notAfter.Format(time.RFC3339), "permittedUseCount": "1",
		"requestDigest": "sha256:r", "authorizationDigest": "sha256:a",
	}
	if claim != "" {
		d[GrantClaimKey] = claim
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "okx-grant-" + name, Namespace: "default",
			Labels: map[string]string{"safety.openkubes.org/grant": "true"}},
		Data: d,
	}
}

func readerWith(t *testing.T, objs ...*corev1.ConfigMap) KubernetesGrantReader {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(s)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return KubernetesGrantReader{Reader: b.Build()}
}

func TestGrantFor_ExpiryAndAmbiguity(t *testing.T) {
	past, future := grantNow.Add(-time.Minute), grantNow.Add(time.Hour)
	cases := []struct {
		name string
		objs []*corev1.ConfigMap
		want string // grantID or "" for nil
	}{
		{"single valid", []*corev1.ConfigMap{grantCM("a", "m1", future, "c")}, "a"},
		{"expired is skipped", []*corev1.ConfigMap{grantCM("a", "m1", past, "c")}, ""},
		{"expired does not cause ambiguity", []*corev1.ConfigMap{grantCM("old", "m1", past, "c"), grantCM("new", "m1", future, "")}, "new"},
		{"two valid are ambiguous (fail-closed)", []*corev1.ConfigMap{grantCM("a", "m1", future, "c"), grantCM("b", "m1", future, "")}, ""},
		{"other machine is not a match", []*corev1.ConfigMap{grantCM("a", "m1", future, "c"), grantCM("b", "m2", future, "")}, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := readerWith(t, tc.objs...).GrantFor(context.Background(), "default", "c-uid",
				TargetKubeadmControlPlane, "kcp-uid", "m1", grantNow)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if g != nil {
				got = g.GrantID
			}
			if got != tc.want {
				t.Fatalf("GrantFor = %q, want %q", got, tc.want)
			}
		})
	}
}

func kcpMachine(version string) *clusterv1.Machine {
	return &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-1", Namespace: "default", UID: "m1",
			Labels: map[string]string{clusterNameLabel: "c"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "controlplane.cluster.x-k8s.io/v1beta2",
				Kind: "KubeadmControlPlane", Name: "kcp", UID: "kcp-uid", Controller: ptr.To(true)}}},
		Spec: clusterv1.MachineSpec{ClusterName: "c", Version: version},
	}
}

// Denials on a recognized in-place shape carry the grant-path reason, so the
// negative tests can attribute them (missing vs. unclaimed vs. version mismatch).
func TestGuardMachineUpdate_DenyReasons(t *testing.T) {
	scope := Scope{Verified: true, Protected: true}
	future := grantNow.Add(time.Hour)
	cases := []struct {
		name   string
		objs   []*corev1.ConfigMap
		to     string
		dryRun bool
		allow  bool
		reason string
	}{
		{"missing grant", nil, "v1.34.0", false, false, "requires an authorized upgrade grant"},
		{"unclaimed grant, persistent write", []*corev1.ConfigMap{grantCM("a", "m1", future, "")}, "v1.34.0", false, false, "has not been claimed"},
		{"unclaimed grant, dry-run probe", []*corev1.ConfigMap{grantCM("a", "m1", future, "")}, "v1.34.0", true, true, ""},
		{"claimed grant, version mismatch", []*corev1.ConfigMap{grantCM("a", "m1", future, "c")}, "v1.35.0", false, false, "version transition does not match"},
		{"claimed grant, match", []*corev1.ConfigMap{grantCM("a", "m1", future, "c")}, "v1.34.0", false, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := GuardMachineUpdate(context.Background(), readerWith(t, tc.objs...), TargetResolver{}, scope, "c-uid",
				kcpMachine("v1.33.0"), kcpMachine(tc.to), tc.dryRun, grantNow)
			if d.Allowed != tc.allow {
				t.Fatalf("allowed=%v want %v (reason %q)", d.Allowed, tc.allow, d.Reason)
			}
			if !tc.allow && !strings.Contains(d.Reason, tc.reason) {
				t.Fatalf("reason %q does not contain %q", d.Reason, tc.reason)
			}
		})
	}
}
