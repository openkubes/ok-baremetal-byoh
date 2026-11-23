// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplaceupgrade

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
)

// TestRealRepositoryIndex runs the Repository and Index steps with gpg, gpgv
// and pkgs.k8s.io against the committed package lock. It needs the network and
// runs only with OKX_NETWORK_TESTS=1.
func TestRealRepositoryIndex(t *testing.T) {
	if os.Getenv("OKX_NETWORK_TESTS") != "1" {
		t.Skip("set OKX_NETWORK_TESTS=1 to check the real repository")
	}
	data, err := os.ReadFile("../../config/inplace/base/packages-v1.37.2.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock map[string]infrav1.InPlaceUpgradePackages
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "Release.key")
	keyring := filepath.Join(dir, "byoh-kubernetes.gpg")
	if err := (HTTPFetcher{}).Fetch(context.Background(), lock["v1.37.2"].Repository+"Release.key", key); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("gpg", "--dearmor", "--yes", "-o", keyring, key).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	req := request(infrav1.InPlaceUpgradeWorker)
	req.Packages = lock["v1.37.2"]
	e := &Executor{
		Runner: ExecRunner{}, Fetcher: HTTPFetcher{}, StateDir: dir, Hostname: "host1", Arch: "amd64",
		Paths:    Paths{Keyring: keyring, Sources: filepath.Join(dir, "sources"), Manifests: dir},
		Timeouts: DefaultTimeouts,
	}
	for _, step := range []Step{StepRepository, StepIndex} {
		if err := e.runStep(context.Background(), step, req, &Result{}); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	// A digest that the repository has not signed is refused.
	req.Packages.Debs[1].SHA256 = digest([]byte("forged"))
	if err := e.runStep(context.Background(), StepIndex, req, &Result{}); err == nil {
		t.Fatal("an unsigned digest was accepted")
	}
}
