// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func TestProtectedMetadataPresenceCannotChange(t *testing.T) {
	scope := Scope{Verified: true, Protected: true}

	tests := []struct {
		name   string
		change func(*clusterv1.Machine)
	}{
		{
			name: "insert empty cluster identity label",
			change: func(m *clusterv1.Machine) {
				m.Labels = map[string]string{
					clusterNameLabel: "",
				}
			},
		},
		{
			name: "remove empty cluster identity label",
			change: func(m *clusterv1.Machine) {
				m.Labels = nil
			},
		},
		{
			name: "insert empty protection annotation",
			change: func(m *clusterv1.Machine) {
				m.Annotations = map[string]string{
					protectionAnnotation: "",
				}
			},
		},
		{
			name: "remove empty protection annotation",
			change: func(m *clusterv1.Machine) {
				m.Annotations = nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldMachine := &clusterv1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cp-0",
					Namespace: "byoh-lab",
				},
			}
			newMachine := oldMachine.DeepCopy()

			switch tt.name {
			case "remove empty cluster identity label":
				oldMachine.Labels = map[string]string{
					clusterNameLabel: "",
				}
			case "remove empty protection annotation":
				oldMachine.Annotations = map[string]string{
					protectionAnnotation: "",
				}
			}

			tt.change(newMachine)

			got := EvaluateMachineUpdate(
				scope,
				oldMachine,
				newMachine,
			)

			if got.Allowed {
				t.Fatal(
					"protected metadata presence changed without rejection",
				)
			}
		})
	}
}
