// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

const criEndpoint = "unix:///run/containerd/containerd.sock"

var sandboxIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// sandboxCommand executes crictl and returns its output.
// Tests inject a fake implementation.
type sandboxCommand func(context.Context, ...string) ([]byte, error)

func runCRICommand(ctx context.Context, args ...string) ([]byte, error) {
	argv := append([]string{
		"--runtime-endpoint", criEndpoint,
	}, args...)

	out, err := exec.CommandContext(ctx, "crictl", argv...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf(
			"crictl %s failed: %w: %s",
			args[0], err, strings.TrimSpace(string(out)),
		)
	}
	return out, nil
}

// podNetworkSandboxes selects only sandboxes using their own network
// namespace. It rejects incomplete, inconsistent or duplicate records.
func podNetworkSandboxes(
	ctx context.Context,
	run sandboxCommand,
) ([]string, error) {
	output, err := run(ctx, "pods", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("list CRI sandboxes: %w", err)
	}

	var inventory struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}

	if err := json.Unmarshal(output, &inventory); err != nil {
		return nil, fmt.Errorf("decode CRI sandbox list: %w", err)
	}
	if inventory.Items == nil {
		return nil, fmt.Errorf("CRI sandbox list has no items array")
	}

	selected := make([]string, 0)
	seen := make(map[string]bool)

	for _, item := range inventory.Items {
		id := item.ID

		if !sandboxIDPattern.MatchString(id) {
			return nil, fmt.Errorf("invalid CRI sandbox ID %q", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate CRI sandbox ID %s", id)
		}
		seen[id] = true

		details, err := run(ctx, "inspectp", "-o", "json", id)
		if err != nil {
			return nil, fmt.Errorf("inspect CRI sandbox %s: %w", id, err)
		}

		var identity struct {
			Status struct {
				ID string `json:"id"`
			} `json:"status"`
		}
		if err := json.Unmarshal(details, &identity); err != nil {
			return nil, fmt.Errorf("decode sandbox identity: %w", err)
		}
		if identity.Status.ID != id {
			return nil, fmt.Errorf(
				"CRI sandbox identity mismatch: listed %s, inspected %s",
				id, identity.Status.ID,
			)
		}

		mode, err := sandboxNetworkMode(details)
		if err != nil {
			return nil, err
		}

		if mode == "POD" {
			selected = append(selected, id)
		}
	}

	return selected, nil
}
