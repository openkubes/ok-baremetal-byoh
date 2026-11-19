// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCleanupPodNetworkSandboxesPreservesNode(t *testing.T) {
	podID := strings.Repeat("a", 64)
	nodeID := strings.Repeat("b", 64)

	podExists := true
	removed := 0

	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "pods":
			if podExists {
				return []byte(fmt.Sprintf(
					`{"items":[{"id":%q},{"id":%q}]}`,
					podID, nodeID,
				)), nil
			}
			return []byte(fmt.Sprintf(
				`{"items":[{"id":%q}]}`, nodeID,
			)), nil
		case "inspectp":
			mode := "POD"
			if args[3] == nodeID {
				mode = "NODE"
			}
			return []byte(fmt.Sprintf(
				`{"status":{"id":%q,"linux":{"namespaces":{"options":{"network":%q}}}}}`,
				args[3], mode,
			)), nil
		case "rmp":
			if len(args) != 3 || args[2] != podID {
				return nil, errors.New("unexpected sandbox removal")
			}
			podExists = false
			removed++
			return nil, nil
		}
		return nil, errors.New("unexpected command")
	}

	if err := cleanupPodNetworkSandboxes(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed %d sandboxes, want 1", removed)
	}
}

func TestCleanupPodNetworkSandboxesRemovalFailure(t *testing.T) {
	podID := strings.Repeat("a", 64)

	removals := 0

	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "pods":
			return []byte(fmt.Sprintf(
				`{"items":[{"id":%q}]}`, podID,
			)), nil
		case "inspectp":
			return []byte(fmt.Sprintf(
				`{"status":{"id":%q,"linux":{"namespaces":{"options":{"network":"POD"}}}}}`,
				podID,
			)), nil
		case "rmp":
			removals++
			return nil, errors.New("CNI DEL failed")
		}
		return nil, errors.New("unexpected command")
	}

	err := cleanupPodNetworkSandboxes(context.Background(), run)
	if err == nil || !strings.Contains(err.Error(), "CNI DEL failed") {
		t.Fatalf("expected CNI failure, got %v", err)
	}
	if removals != 1 {
		t.Fatalf("removal attempts = %d, want 1", removals)
	}
}

func TestCleanupPodNetworkSandboxesRejectsIdentity(t *testing.T) {
	podID := strings.Repeat("a", 64)

	removals := 0

	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "pods":
			return []byte(fmt.Sprintf(
				`{"items":[{"id":%q}]}`, podID,
			)), nil
		case "inspectp":
			return []byte(`{"status":{"id":"wrong-id"}}`), nil
		case "rmp":
			removals++
		}
		return nil, nil
	}

	if err := cleanupPodNetworkSandboxes(context.Background(), run); err == nil {
		t.Fatal("expected invalid sandbox identity to fail")
	}
	if removals != 0 {
		t.Fatal("sandbox deletion must not begin after failed validation")
	}
}
