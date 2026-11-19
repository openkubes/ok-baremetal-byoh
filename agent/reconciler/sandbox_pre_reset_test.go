// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPreResetSandboxCleanupStopFailure(t *testing.T) {
	criCalled := false

	run := func(_ context.Context, _ ...string) ([]byte, error) {
		criCalled = true
		return nil, nil
	}

	stopKubelet := func(context.Context) error {
		return errors.New("kubelet stop failed")
	}

	err := preResetSandboxCleanup(
		context.Background(),
		stopKubelet,
		run,
	)

	if err == nil || !strings.Contains(err.Error(), "kubelet stop failed") {
		t.Fatalf("expected kubelet stop error, got %v", err)
	}

	if criCalled {
		t.Fatal("CRI must not be accessed after kubelet stop failure")
	}
}
