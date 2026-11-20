// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"strings"
	"testing"
)

func TestParseProtectedRegistry(t *testing.T) {
	const valid = `{
		"registrations": [{
			"namespace": "byoh-lab",
			"clusterName": "ok-byoh",
			"clusterUID": "uid-123"
		}]
	}`

	tests := []struct {
		name      string
		input     string
		wantError bool
	}{
		{"valid", valid, false},
		{"empty", `{}`, true},
		{"empty list", `{"registrations":[]}`, true},
		{"invalid json", `{`, true},
		{"trailing data", valid + `{}`, true},
		{
			"unknown field",
			`{"registrations":[{"namespace":"byoh-lab",
			"clusterName":"ok-byoh","clusterUID":"uid-123",
			"protected":false}]}`,
			true,
		},
		{
			"missing UID",
			`{"registrations":[{"namespace":"byoh-lab",
			"clusterName":"ok-byoh"}]}`,
			true,
		},
		{
			"invalid namespace",
			`{"registrations":[{"namespace":"BAD_NAMESPACE",
			"clusterName":"ok-byoh","clusterUID":"uid-123"}]}`,
			true,
		},
		{
			"duplicate namespace",
			`{"registrations":[
				{"namespace":"byoh-lab","clusterName":"ok-byoh",
				"clusterUID":"uid-123"},
				{"namespace":"byoh-lab","clusterName":"other",
				"clusterUID":"uid-456"}
			]}`,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, err := ParseProtectedRegistry([]byte(tt.input))

			if tt.wantError {
				if err == nil {
					t.Fatal("expected registry validation error")
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			got, exists := registry["byoh-lab"]
			if !exists || !got.Protected ||
				got.ClusterName != "ok-byoh" ||
				got.ClusterUID != "uid-123" {
				t.Fatalf("unexpected registration: %+v", got)
			}
		})
	}
}

func TestRegistryCannotDisableProtection(t *testing.T) {
	input := `{
		"registrations": [{
			"namespace":"byoh-lab",
			"clusterName":"ok-byoh",
			"clusterUID":"uid-123",
			"protected":false
		}]
	}`

	_, err := ParseProtectedRegistry([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field rejection, got %v", err)
	}
}
