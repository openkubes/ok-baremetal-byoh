// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

//nolint:testpackage // White-box tests for private Machine safety metadata.
package noreplacement

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

//nolint:funlen // White-box hardening matrix over reserved CAPI/BYOH/OpenKubes metadata keys; each row guards a distinct replacement-bypass vector.
func TestMachineUpdateMetadataHardening(t *testing.T) {
	scope := Scope{Verified: true, Protected: true}

	base := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cp-0",
			Namespace: "byoh-lab",
			UID:       types.UID("machine-uid"),
			Labels: map[string]string{
				clusterNameLabel: "ok-byoh",
			},
			Annotations: map[string]string{
				protectionAnnotation: "true",
			},
			Finalizers: []string{
				"machine.cluster.x-k8s.io",
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
			name: "ordinary annotation allowed",
			change: func(m *clusterv1.Machine) {
				m.Annotations["example.org/note"] = "ok"
			},
			allow: true,
		},
		{
			name: "CAPI label insertion denied",
			change: func(m *clusterv1.Machine) {
				m.Labels["cluster.x-k8s.io/control-plane"] = ""
			},
			reason: "reserved labels",
		},
		{
			name: "CAPI cluster label removal denied",
			change: func(m *clusterv1.Machine) {
				delete(m.Labels, clusterNameLabel)
			},
			reason: "cluster identity",
		},
		{
			name: "BYOH label insertion denied",
			change: func(m *clusterv1.Machine) {
				m.Labels["byoh.infrastructure.cluster.x-k8s.io/host"] = "other"
			},
			reason: "reserved labels",
		},
		{
			name: "CAPI annotation insertion denied",
			change: func(m *clusterv1.Machine) {
				m.Annotations["cluster.x-k8s.io/delete-machine"] = ""
			},
			reason: "reserved annotations",
		},
		{
			name: "protection annotation removal denied",
			change: func(m *clusterv1.Machine) {
				delete(m.Annotations, protectionAnnotation)
			},
			reason: "protection annotation",
		},
		{
			name: "safety annotation insertion denied",
			change: func(m *clusterv1.Machine) {
				m.Annotations["safety.openkubes.org/bypass"] = "true"
			},
			reason: "reserved annotations",
		},
		{
			name: "finalizer removal denied",
			change: func(m *clusterv1.Machine) {
				m.Finalizers = nil
			},
			reason: "finalizers",
		},
		{
			name: "finalizer insertion denied",
			change: func(m *clusterv1.Machine) {
				m.Finalizers = append(m.Finalizers, "example.org/other")
			},
			reason: "finalizers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated := base.DeepCopy()
			tt.change(updated)

			got := EvaluateMachineUpdate(scope, base, updated)

			if got.Allowed != tt.allow {
				t.Fatalf(
					"Allowed=%v, want=%v: %s",
					got.Allowed, tt.allow, got.Reason,
				)
			}

			if tt.reason != "" &&
				!strings.Contains(got.Reason, tt.reason) {
				t.Fatalf(
					"reason %q does not contain %q",
					got.Reason, tt.reason,
				)
			}
		})
	}
}
