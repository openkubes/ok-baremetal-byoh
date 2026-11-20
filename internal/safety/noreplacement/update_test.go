package noreplacement

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func TestEvaluateMachineUpdate(t *testing.T) {
	protected := Scope{Verified: true, Protected: true}

	original := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cp-0",
			Namespace: "byoh-lab",
			UID:       types.UID("machine-uid-001"),
			Labels: map[string]string{
				clusterNameLabel: "ok-byoh",
			},
			Annotations: map[string]string{
				protectionAnnotation: "true",
			},
		},
	}

	tests := []struct {
		name   string
		change func(*clusterv1.Machine)
		allow  bool
		reason string
	}{
		{
			name: "allow ordinary annotation",
			change: func(m *clusterv1.Machine) {
				m.Annotations["example.org/note"] = "test"
			},
			allow: true,
		},
		{
			name: "deny version change",
			change: func(m *clusterv1.Machine) {
				m.Spec.Version = "v1.34.0"
			},
			reason: "spec changes",
		},
		{
			name: "deny cluster label change",
			change: func(m *clusterv1.Machine) {
				m.Labels[clusterNameLabel] = "other-cluster"
			},
			reason: "cluster identity",
		},
		{
			name: "deny protection removal",
			change: func(m *clusterv1.Machine) {
				delete(m.Annotations, protectionAnnotation)
			},
			reason: "protection annotation",
		},
		{
			name: "deny owner reference change",
			change: func(m *clusterv1.Machine) {
				m.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: "cluster.x-k8s.io/v1beta2",
					Kind:       "MachineSet",
					Name:       "unexpected-owner",
					UID:        types.UID("owner-uid"),
				}}
			},
			reason: "owner references",
		},
		{
			name: "deny UID change",
			change: func(m *clusterv1.Machine) {
				m.UID = types.UID("new-uid")
			},
			reason: "identity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated := original.DeepCopy()
			tt.change(updated)

			got := EvaluateMachineUpdate(
				protected,
				original,
				updated,
			)

			if got.Allowed != tt.allow {
				t.Fatalf(
					"Allowed = %v, want %v: %s",
					got.Allowed,
					tt.allow,
					got.Reason,
				)
			}

			if tt.reason != "" &&
				!strings.Contains(got.Reason, tt.reason) {
				t.Fatalf(
					"Reason = %q, expected %q",
					got.Reason,
					tt.reason,
				)
			}
		})
	}
}

func TestMachineUpdateUnknownScopeFailsClosed(t *testing.T) {
	machine := &clusterv1.Machine{}

	got := EvaluateMachineUpdate(
		Scope{},
		machine,
		machine,
	)

	if got.Allowed {
		t.Fatal("unknown scope unexpectedly allowed")
	}
}
