// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// PackageLock pins the packages of each Kubernetes version that the extension
// may upgrade to. A target that is not in the lock is never upgraded to.
type PackageLock map[string]PinnedPackages

// PinnedPackages are the packages of a target version, as the extension puts
// them into spec.inPlaceUpgrade.packages of a ByoHost.
type PinnedPackages struct {
	Repository               string      `json:"repository"`
	RepositoryKeyFingerprint string      `json:"repositoryKeyFingerprint"`
	Debs                     []PinnedDeb `json:"debs"`
}

// PinnedDeb is a package with its version and the SHA256 of its .deb file.
type PinnedDeb struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// pinnedDebNames are the packages that a lock pins for each version.
var pinnedDebNames = []string{"kubeadm", "kubelet", "kubectl"}

var (
	versionRE     = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)
	fingerprintRE = regexp.MustCompile(`^[0-9A-F]{40}$`)
	sha256RE      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ParsePackageLock parses a lock strictly: unknown fields, an unpinned or
// duplicated package, a package of another version, or a repository other
// than the pkgs.k8s.io repository of the minor version are errors.
func ParsePackageLock(data []byte) (PackageLock, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	lock := PackageLock{}
	if err := dec.Decode(&lock); err != nil {
		return nil, fmt.Errorf("package lock: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("package lock: trailing data")
	}
	if len(lock) == 0 {
		return nil, fmt.Errorf("package lock: no versions")
	}
	for version, p := range lock {
		if err := p.validate(version); err != nil {
			return nil, fmt.Errorf("package lock %s: %w", version, err)
		}
	}
	return lock, nil
}

func (p PinnedPackages) validate(version string) error {
	m := versionRE.FindStringSubmatch(version)
	if m == nil {
		return fmt.Errorf("the version must be vX.Y.Z")
	}
	if want := fmt.Sprintf("https://pkgs.k8s.io/core:/stable:/v%s.%s/deb/", m[1], m[2]); p.Repository != want {
		return fmt.Errorf("the repository must be %s", want)
	}
	if !fingerprintRE.MatchString(p.RepositoryKeyFingerprint) {
		return fmt.Errorf("the key fingerprint must be 40 upper-case hexadecimal digits")
	}
	seen := map[string]bool{}
	for _, deb := range p.Debs {
		if !slices.Contains(pinnedDebNames, deb.Name) {
			return fmt.Errorf("unexpected package %q", deb.Name)
		}
		if seen[deb.Name] {
			return fmt.Errorf("package %s is pinned twice", deb.Name)
		}
		seen[deb.Name] = true
		if !strings.HasPrefix(deb.Version, strings.TrimPrefix(version, "v")+"-") {
			return fmt.Errorf("package %s %s is not of %s", deb.Name, deb.Version, version)
		}
		if !sha256RE.MatchString(deb.SHA256) {
			return fmt.Errorf("package %s has no SHA256", deb.Name)
		}
	}
	if len(seen) != len(pinnedDebNames) {
		return fmt.Errorf("kubeadm, kubelet and kubectl must be pinned")
	}
	return nil
}

// ParseVersion returns the major, minor and patch version of vX.Y.Z.
func ParseVersion(version string) (major, minor, patch int, ok bool) {
	m := versionRE.FindStringSubmatch(version)
	if m == nil {
		return 0, 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	patch, _ = strconv.Atoi(m[3])
	return major, minor, patch, true
}

// SupportedTransition reports whether an in-place upgrade from one version to
// another is supported: forward, the same major version, and at most one
// minor version, as kubeadm upgrades.
func SupportedTransition(from, to string) error {
	fMajor, fMinor, fPatch, ok := ParseVersion(from)
	if !ok {
		return fmt.Errorf("the current version %q is not vX.Y.Z", from)
	}
	tMajor, tMinor, tPatch, ok := ParseVersion(to)
	if !ok {
		return fmt.Errorf("the target version %q is not vX.Y.Z", to)
	}
	switch {
	case fMajor != tMajor:
		return fmt.Errorf("the major version cannot change (%s to %s)", from, to)
	case tMinor < fMinor || (tMinor == fMinor && tPatch <= fPatch):
		return fmt.Errorf("an in-place upgrade only moves forward (%s to %s)", from, to)
	case tMinor > fMinor+1:
		return fmt.Errorf("kubeadm upgrades by at most one minor version (%s to %s)", from, to)
	}
	return nil
}

// UpgradeID derives the ID of the upgrade of a Machine to a version, so that
// every UpdateMachine call for the same update writes the same request.
func UpgradeID(machineUID, toVersion string) string {
	sum := sha256.Sum256([]byte(machineUID + "/" + toVersion))
	return hex.EncodeToString(sum[:])[:20]
}
