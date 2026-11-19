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

func TestCleanupPodNetworkSandboxesRetriesRemaining(t *testing.T) {
	firstID := strings.Repeat("a", 64)
	secondID := strings.Repeat("b", 64)

	exists := map[string]bool{
		firstID:  true,
		secondID: true,
	}
	failSecond := true
	removed := map[string]int{}

	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "pods":
			items := ""
			for _, id := range []string{firstID, secondID} {
				if exists[id] {
					items += fmt.Sprintf(`{"id":%q},`, id)
				}
			}
			items = strings.TrimSuffix(items, ",")
			return []byte(`{"items":[` + items + `]}`), nil

		case "inspectp":
			id := args[3]
			return []byte(fmt.Sprintf(
				`{"status":{"id":%q,"linux":{"namespaces":{"options":{"network":"POD"}}}}}`,
				id,
			)), nil

		case "rmp":
			id := args[2]
			if id == secondID && failSecond {
				return nil, errors.New("CNI DEL failed")
			}
			removed[id]++
			delete(exists, id)
			return nil, nil
		}

		return nil, errors.New("unexpected command")
	}

	ctx := context.Background()

	// First attempt removes A, then fails on B.
	if err := cleanupPodNetworkSandboxes(ctx, run); err == nil {
		t.Fatal("expected first cleanup to fail")
	}
	if exists[firstID] || !exists[secondID] {
		t.Fatalf("unexpected remaining sandboxes: %v", exists)
	}

	// CNI recovers; retry should remove only B.
	failSecond = false

	if err := cleanupPodNetworkSandboxes(ctx, run); err != nil {
		t.Fatalf("retry failed: %v", err)
	}

	if len(exists) != 0 {
		t.Fatalf("sandboxes remain: %v", exists)
	}
	if removed[firstID] != 1 || removed[secondID] != 1 {
		t.Fatalf("unexpected removal counts: %v", removed)
	}
}
