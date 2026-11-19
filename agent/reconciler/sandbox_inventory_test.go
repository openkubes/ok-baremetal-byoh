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

func TestPodNetworkSandboxes(t *testing.T) {
	podID := strings.Repeat("a", 64)
	nodeID := strings.Repeat("b", 64)

	t.Run("selects POD but preserves NODE", func(t *testing.T) {
		run := func(_ context.Context, args ...string) ([]byte, error) {
			switch args[0] {
			case "pods":
				return []byte(fmt.Sprintf(
					`{"items":[{"id":%q},{"id":%q}]}`,
					podID, nodeID,
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
			}
			return nil, errors.New("unexpected command")
		}

		got, err := podNetworkSandboxes(context.Background(), run)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != podID {
			t.Fatalf("selected %v; want only %s", got, podID)
		}
	})

	t.Run("rejects unknown network mode", func(t *testing.T) {
		run := func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "pods" {
				return []byte(fmt.Sprintf(`{"items":[{"id":%q}]}`, podID)), nil
			}
			return []byte(fmt.Sprintf(`{"status":{"id":%q}}`, podID)), nil
		}

		_, err := podNetworkSandboxes(context.Background(), run)
		if err == nil {
			t.Fatal("expected unknown network mode to fail")
		}
	})

	t.Run("rejects mismatched sandbox identity", func(t *testing.T) {
		run := func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "pods" {
				return []byte(fmt.Sprintf(`{"items":[{"id":%q}]}`, podID)), nil
			}
			return []byte(fmt.Sprintf(`{"status":{"id":%q}}`, nodeID)), nil
		}

		_, err := podNetworkSandboxes(context.Background(), run)
		if err == nil {
			t.Fatal("expected identity mismatch to fail")
		}
	})

	t.Run("rejects failed inspection", func(t *testing.T) {
		run := func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "pods" {
				return []byte(fmt.Sprintf(`{"items":[{"id":%q}]}`, podID)), nil
			}
			return nil, errors.New("inspection unavailable")
		}

		_, err := podNetworkSandboxes(context.Background(), run)
		if err == nil {
			t.Fatal("expected inspection error")
		}
	})

	t.Run("accepts empty inventory", func(t *testing.T) {
		run := func(_ context.Context, args ...string) ([]byte, error) {
			return []byte(`{"items":[]}`), nil
		}
		got, err := podNetworkSandboxes(context.Background(), run)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, error %v", got, err)
		}
	})
}
