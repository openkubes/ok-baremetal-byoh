// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplace

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const validLock = `{"v1.37.2": {
  "repository": "https://pkgs.k8s.io/core:/stable:/v1.37/deb/",
  "repositoryKeyFingerprint": "DE15B14486CD377B9E876E1A234654DA9A296436",
  "debs": [
    {"name": "kubeadm", "version": "1.37.2-1.1", "sha256": "d1674bffcf80b36531375b35cf68d0dc23453a373c1b4537d78b6960732bc699"},
    {"name": "kubelet", "version": "1.37.2-1.1", "sha256": "f95d0b572a24563bb6e2a1a1f0e84a8a30a24e11c95068b2e81c294a0e1bee11"},
    {"name": "kubectl", "version": "1.37.2-1.1", "sha256": "e95d73aaf1da438cc8b287545b128fbe5e6a9f3723ecaa25eac1b42b8fe542ab"}
  ]}}`

func TestParsePackageLock(t *testing.T) {
	lock, err := ParsePackageLock([]byte(validLock))
	if err != nil {
		t.Fatal(err)
	}
	if len(lock["v1.37.2"].Debs) != 3 {
		t.Fatalf("%+v", lock)
	}
	for name, mutate := range map[string]func(string) string{
		"an unknown field":         func(s string) string { return strings.Replace(s, `"debs"`, `"extra": 1, "debs"`, 1) },
		"another repository":       func(s string) string { return strings.Replace(s, "pkgs.k8s.io", "example.com", 1) },
		"the repository of 1.36":   func(s string) string { return strings.Replace(s, "stable:/v1.37", "stable:/v1.36", 1) },
		"a lower-case fingerprint": func(s string) string { return strings.Replace(s, "DE15B144", "de15b144", 1) },
		"a package of another version": func(s string) string {
			return strings.Replace(s, `"kubelet", "version": "1.37.2-1.1"`, `"kubelet", "version": "1.37.1-1.1"`, 1)
		},
		"a missing digest": func(s string) string { return strings.Replace(s, "d1674bff", "", 1) },
		"a missing package": func(s string) string {
			return regexp.MustCompile(`,\s*\{"name": "kubectl"[^}]*\}`).ReplaceAllString(s, "")
		},
		"a duplicated package":    func(s string) string { return strings.Replace(s, `"name": "kubectl"`, `"name": "kubelet"`, 1) },
		"an unexpected package":   func(s string) string { return strings.Replace(s, `"name": "kubectl"`, `"name": "cri-tools"`, 1) },
		"a version with a suffix": func(s string) string { return strings.Replace(s, `"v1.37.2"`, `"v1.37.2+okx"`, 1) },
		"trailing data":           func(s string) string { return s + "{}" },
		"no versions":             func(string) string { return "{}" },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePackageLock([]byte(mutate(validLock))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// TestCommittedLock keeps the committed lock of the deployment valid.
func TestCommittedLock(t *testing.T) {
	data, err := os.ReadFile("../../config/inplace/base/packages-v1.37.2.json")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := ParsePackageLock(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lock["v1.37.2"]; !ok {
		t.Fatal("no v1.37.2")
	}
}

func TestSupportedTransition(t *testing.T) {
	for _, c := range [][2]string{{"v1.36.6", "v1.37.2"}, {"v1.37.0", "v1.37.2"}, {"v1.36.6", "v1.36.7"}} {
		if err := SupportedTransition(c[0], c[1]); err != nil {
			t.Errorf("%s to %s: %v", c[0], c[1], err)
		}
	}
	for _, c := range [][2]string{{"v1.36.6", "v1.36.6"}, {"v1.37.2", "v1.36.6"}, {"v1.35.0", "v1.37.0"}, {"v1.36.6", "v2.0.0"}, {"1.36.6", "v1.37.2"}, {"v1.36.6", "v1.37.2-rc.0"}} {
		if err := SupportedTransition(c[0], c[1]); err == nil {
			t.Errorf("%s to %s accepted", c[0], c[1])
		}
	}
}

func TestUpgradeID(t *testing.T) {
	a := UpgradeID("uid-1", "v1.37.2")
	if a != UpgradeID("uid-1", "v1.37.2") || a == UpgradeID("uid-2", "v1.37.2") || a == UpgradeID("uid-1", "v1.37.3") {
		t.Fatal("not bound to the Machine and the version")
	}
	if !regexp.MustCompile(`^[a-z0-9]{1,63}$`).MatchString(a) {
		t.Fatalf("%q does not match the API pattern", a)
	}
}
