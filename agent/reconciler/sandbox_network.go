// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"encoding/json"
	"fmt"
)

const (
	sandboxNetworkPOD = "POD"
	sandboxNetworkNODE = "NODE"
)

// sandboxNetworkMode reads the CRI PodSandboxStatus network namespace
// mode. Unknown or missing modes must never be treated as safe.
func sandboxNetworkMode(data []byte) (string, error) {
	var status struct {
		Status struct {
			ID    string `json:"id"`
			Linux struct {
				Namespaces struct {
					Options struct {
						Network string `json:"network"`
					} `json:"options"`
				} `json:"namespaces"`
			} `json:"linux"`
		} `json:"status"`
	}

	if err := json.Unmarshal(data, &status); err != nil {
		return "", fmt.Errorf("decode CRI sandbox status: %w", err)
	}
	if status.Status.ID == "" {
		return "", fmt.Errorf("CRI sandbox status is missing an ID")
	}

	switch status.Status.Linux.Namespaces.Options.Network {
	case sandboxNetworkPOD, sandboxNetworkNODE:
		return status.Status.Linux.Namespaces.Options.Network, nil
	default:
		return "", fmt.Errorf(
			"unknown network namespace mode for sandbox %s: %q",
			status.Status.ID,
			status.Status.Linux.Namespaces.Options.Network,
		)
	}
}
