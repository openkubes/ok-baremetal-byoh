// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
)

func TestEvaluateNamespaceChange(t *testing.T) {
	for _, tc := range []struct {
		name       string
		op         admissionv1.Operation
		registered bool
		old, new   string
		allowed    bool
	}{
		{"removing the label of a registered namespace", admissionv1.Update, true, "true", "", false},
		{"changing the label of a registered namespace", admissionv1.Update, true, "true", "false", false},
		{"another change of a registered namespace", admissionv1.Update, true, "true", "true", true},
		{"protecting a registered namespace", admissionv1.Update, true, "", "true", true},
		{"deleting a registered namespace", admissionv1.Delete, true, "true", "", false},
		{"labelling an unregistered namespace", admissionv1.Update, false, "", "true", false},
		{"creating an unregistered labelled namespace", admissionv1.Create, false, "", "true", false},
		{"unlabelling an unregistered namespace after decommission", admissionv1.Update, false, "true", "", true},
		{"deleting an unregistered namespace", admissionv1.Delete, false, "true", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateNamespaceChange(tc.op, tc.registered, tc.old, tc.new); got.Allowed != tc.allowed {
				t.Fatalf("allowed=%v: %s", got.Allowed, got.Reason)
			}
		})
	}
}
