// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import "testing"

func TestSandboxNetworkMode(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name: "CoreDNS POD network",
			input: `{"status":{"id":"coredns-1",
				"linux":{"namespaces":{"options":{"network":"POD"}}}}}`,
			want: "POD",
		},
		{
			name: "host network",
			input: `{"status":{"id":"host-1",
				"linux":{"namespaces":{"options":{"network":"NODE"}}}}}`,
			want: "NODE",
		},
		{
			name: "unknown mode",
			input: `{"status":{"id":"unknown-1",
				"linux":{"namespaces":{"options":{"network":"FUTURE"}}}}}`,
			wantErr: true,
		},
		{
			name:    "missing mode",
			input:   `{"status":{"id":"missing-1"}}`,
			wantErr: true,
		},
		{
			name:    "missing ID",
			input:   `{"status":{"linux":{"namespaces":{"options":{"network":"POD"}}}}}`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			input:   `{invalid`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sandboxNetworkMode([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v; wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("mode = %q; want %q", got, tt.want)
			}
		})
	}
}
