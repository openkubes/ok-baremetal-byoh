// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
)

// cleanupPodNetworkSandboxes removes only explicitly inspected POD-network
// sandboxes. NODE-network sandboxes are intentionally left untouched.
//
// The caller must ensure kubelet is stopped before invoking this function.
func cleanupPodNetworkSandboxes(
	ctx context.Context,
	run sandboxCommand,
) error {
	ids, err := podNetworkSandboxes(ctx, run)
	if err != nil {
		return fmt.Errorf("pre-reset sandbox inventory: %w", err)
	}

	for _, id := range ids {
		if _, err := run(ctx, "rmp", "--force", id); err != nil {
			return fmt.Errorf("remove POD-network sandbox %s: %w", id, err)
		}
	}

	remaining, err := podNetworkSandboxes(ctx, run)
	if err != nil {
		return fmt.Errorf("verify pre-reset sandbox cleanup: %w", err)
	}
	if len(remaining) != 0 {
		return fmt.Errorf(
			"pre-reset cleanup incomplete: %d POD-network sandboxes remain",
			len(remaining),
		)
	}

	return nil
}

// preResetSandboxCleanup keeps kubelet from recreating pods and
// removes POD-network sandboxes before kubeadm reset.
func preResetSandboxCleanup(
	ctx context.Context,
	stopKubelet func(context.Context) error,
	run sandboxCommand,
) error {
	if err := stopKubelet(ctx); err != nil {
		return fmt.Errorf("stop kubelet: %w", err)
	}
	if err := cleanupPodNetworkSandboxes(ctx, run); err != nil {
		return fmt.Errorf("cleanup POD-network sandboxes: %w", err)
	}
	return nil
}
