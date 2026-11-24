// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

//nolint:testpackage // Tests the private registry-reader injection seam.
package noreplacement

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func writeRegistry(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetupFromEnvironment(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	registryPath := writeRegistry(t, `{
		"registrations": [{
			"namespace": "byoh-lab",
			"clusterName": "ok-byoh",
			"clusterUID": "uid-123"
		}]
	}`)

	emptyPath := writeRegistry(t, `{"registrations": []}`)
	noKeyPath := writeRegistry(t, `{}`)

	tests := []struct {
		name        string
		enabled     string
		file        string
		wantHandler bool
		wantError   bool
	}{
		{
			name: "disabled by default",
		},
		{
			name:    "explicit false",
			enabled: "false",
		},
		{
			name:      "invalid flag",
			enabled:   "yes",
			wantError: true,
		},
		{
			name:      "enabled without registry",
			enabled:   "true",
			wantError: true,
		},
		{
			name:      "enabled missing registry file",
			enabled:   "true",
			file:      filepath.Join(t.TempDir(), "missing.json"),
			wantError: true,
		},
		{
			name:      "enabled with a registry without registrations key",
			enabled:   "true",
			file:      noKeyPath,
			wantError: true,
		},
		{
			name:      "enabled with an unreadable registry",
			enabled:   "true",
			file:      t.TempDir(),
			wantError: true,
		},
		{
			name:        "enabled with an explicitly empty registry",
			enabled:     "true",
			file:        emptyPath,
			wantHandler: true,
		},
		{
			name:        "enabled with valid registry",
			enabled:     "true",
			file:        registryPath,
			wantHandler: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(GuardEnabledEnv, tt.enabled)

			handler, err := setupFromEnvironment(reader, func() ([]byte, error) {
				return os.ReadFile(tt.file)
			})

			if (err != nil) != tt.wantError {
				t.Fatalf(
					"error=%v, wantError=%v",
					err,
					tt.wantError,
				)
			}
			if (handler != nil) != tt.wantHandler {
				t.Fatalf(
					"handler=%v, wantHandler=%v",
					handler != nil,
					tt.wantHandler,
				)
			}
		})
	}
}

func TestSetupFailsWithoutReader(t *testing.T) {
	t.Setenv(GuardEnabledEnv, "true")

	handler, err := SetupFromEnvironment(nil)

	if err == nil || handler != nil {
		t.Fatal("enabled guard must reject nil API reader")
	}
}
