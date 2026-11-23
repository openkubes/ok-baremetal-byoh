// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"fmt"
	"strconv"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// inPlaceUpgradeDebs are the packages of an in-place upgrade, each exactly once.
var inPlaceUpgradeDebs = []string{debKubeadm, debKubelet, debKubectl}

// Lengths of a version, a key fingerprint and a SHA256, and the kubeadm package.
const (
	versionParts      = 3
	fingerprintHexLen = 40
	sha256HexLen      = 64
	debKubeadm        = "kubeadm"
	debKubelet        = "kubelet"
	debKubectl        = "kubectl"
)

// minorOf returns the major and minor version of vX.Y.Z.
func minorOf(version string) (major, minor int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) != versionParts || !strings.HasPrefix(version, "v") {
		return 0, 0, false
	}
	nums := make([]int, versionParts)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], true
}

// compareVersions compares two vX.Y.Z versions that minorOf accepts.
func compareVersions(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := range versionParts {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ValidateInPlaceUpgradeRequest validates a request on its own and against the
// attachment of the ByoHost. An upgrade only moves forward, by at most one
// minor version, with exactly the pinned kubeadm, kubelet and kubectl of the
// target version from the repository of its minor version.
func ValidateInPlaceUpgradeRequest(spec *ByoHostSpec) field.ErrorList {
	var errs field.ErrorList
	req := spec.InPlaceUpgrade
	if req == nil {
		return nil
	}
	path := field.NewPath("spec", "inPlaceUpgrade")
	if spec.ByoMachineRef == nil {
		errs = append(errs, field.Forbidden(path, "an in-place upgrade needs an attached ByoHost (spec.byoMachineRef)"))
	} else if req.ByoMachineUID != spec.ByoMachineRef.UID {
		errs = append(errs, field.Invalid(path.Child("byoMachineUID"), req.ByoMachineUID, "must be the UID in spec.byoMachineRef"))
	}
	if spec.ResetRequested {
		errs = append(errs, field.Forbidden(path, "a host that is reset is not upgraded"))
	}
	for name, value := range map[string]string{
		"id": req.ID, "clusterName": req.ClusterName, "clusterUID": string(req.ClusterUID),
		"machineName": req.MachineName, "machineUID": string(req.MachineUID), "nodeName": req.NodeName,
	} {
		if value == "" {
			errs = append(errs, field.Required(path.Child(name), ""))
		}
	}
	switch req.Role {
	case InPlaceUpgradeControlPlaneFirst, InPlaceUpgradeControlPlane, InPlaceUpgradeWorker:
	default:
		errs = append(errs, field.NotSupported(path.Child("role"), req.Role, []InPlaceUpgradeRole{InPlaceUpgradeControlPlaneFirst, InPlaceUpgradeControlPlane, InPlaceUpgradeWorker}))
	}
	fromMajor, fromMinor, fromOK := minorOf(req.FromVersion)
	toMajor, toMinor, toOK := minorOf(req.ToVersion)
	if !fromOK {
		errs = append(errs, field.Invalid(path.Child("fromVersion"), req.FromVersion, "must be vX.Y.Z"))
	}
	if !toOK {
		errs = append(errs, field.Invalid(path.Child("toVersion"), req.ToVersion, "must be vX.Y.Z"))
	}
	if fromOK && toOK {
		switch {
		case fromMajor != toMajor:
			errs = append(errs, field.Invalid(path.Child("toVersion"), req.ToVersion, "the major version cannot change"))
		case compareVersions(req.ToVersion, req.FromVersion) <= 0:
			errs = append(errs, field.Invalid(path.Child("toVersion"), req.ToVersion, "an in-place upgrade only moves to a later version"))
		case toMinor > fromMinor+1:
			errs = append(errs, field.Invalid(path.Child("toVersion"), req.ToVersion, "kubeadm upgrades by at most one minor version"))
		}
	}
	errs = append(errs, validatePackages(path.Child("packages"), &req.Packages, req.ToVersion, toMajor, toMinor, toOK)...)
	return errs
}

func validatePackages(path *field.Path, p *InPlaceUpgradePackages, toVersion string, major, minor int, versionOK bool) field.ErrorList {
	var errs field.ErrorList
	if versionOK {
		want := fmt.Sprintf("https://pkgs.k8s.io/core:/stable:/v%d.%d/deb/", major, minor)
		if p.Repository != want {
			errs = append(errs, field.Invalid(path.Child("repository"), p.Repository, "must be "+want))
		}
	}
	if !isHex(p.RepositoryKeyFingerprint, fingerprintHexLen, true) {
		errs = append(errs, field.Invalid(path.Child("repositoryKeyFingerprint"), p.RepositoryKeyFingerprint, "must be 40 upper-case hexadecimal digits"))
	}
	seen := map[string]bool{}
	for i, deb := range p.Debs {
		debPath := path.Child("debs").Index(i)
		if seen[deb.Name] {
			errs = append(errs, field.Duplicate(debPath.Child("name"), deb.Name))
		}
		seen[deb.Name] = true
		if !strings.HasPrefix(deb.Version, strings.TrimPrefix(toVersion, "v")+"-") {
			errs = append(errs, field.Invalid(debPath.Child("version"), deb.Version, "must be a package of "+toVersion))
		}
		if !isHex(deb.SHA256, sha256HexLen, false) {
			errs = append(errs, field.Invalid(debPath.Child("sha256"), deb.SHA256, "must be 64 lower-case hexadecimal digits"))
		}
	}
	for _, name := range inPlaceUpgradeDebs {
		if !seen[name] {
			errs = append(errs, field.Required(path.Child("debs"), "a pinned "+name+" package"))
		}
	}
	if len(p.Debs) != len(inPlaceUpgradeDebs) {
		errs = append(errs, field.Invalid(path.Child("debs"), len(p.Debs), "must be exactly kubeadm, kubelet and kubectl"))
	}
	return errs
}

func isHex(s string, n int, upper bool) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case upper && c >= 'A' && c <= 'F':
		case !upper && c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// validateInPlaceUpgradeUpdate validates a change of spec.inPlaceUpgrade by
// the controller or the extension. A request is immutable while it runs,
// except for a larger attempt, and a running upgrade cannot be removed or
// replaced: the host would be left between two versions without an owner.
func validateInPlaceUpgradeUpdate(oldHost, newHost *ByoHost) field.ErrorList {
	errs := ValidateInPlaceUpgradeRequest(&newHost.Spec)
	oldReq, newReq := oldHost.Spec.InPlaceUpgrade, newHost.Spec.InPlaceUpgrade
	path := field.NewPath("spec", "inPlaceUpgrade")
	running := oldReq != nil && newHost.Status.InPlaceUpgrade != nil &&
		newHost.Status.InPlaceUpgrade.ID == oldReq.ID &&
		newHost.Status.InPlaceUpgrade.Phase == InPlaceUpgradeRunning
	if oldReq == nil || newReq == nil {
		if running && newReq == nil {
			errs = append(errs, field.Forbidden(path, "cannot remove an in-place upgrade while the agent runs it"))
		}
		return errs
	}
	if oldReq.ID != newReq.ID {
		if running {
			errs = append(errs, field.Forbidden(path.Child("id"), "cannot replace an in-place upgrade while the agent runs it"))
		}
		return errs
	}
	if newReq.Attempt < oldReq.Attempt {
		errs = append(errs, field.Invalid(path.Child("attempt"), newReq.Attempt, "cannot decrease"))
	}
	oldCopy, newCopy := *oldReq, *newReq
	oldCopy.Attempt, newCopy.Attempt = 0, 0
	if !apiequality.Semantic.DeepEqual(oldCopy, newCopy) {
		errs = append(errs, field.Forbidden(path, "an in-place upgrade is immutable except for spec.inPlaceUpgrade.attempt"))
	}
	return errs
}

// validateHostInPlaceUpgradeStatus validates what an agent reports in
// status.inPlaceUpgrade: only the request in the spec, never a request that
// the agent has made up.
func validateHostInPlaceUpgradeStatus(oldHost, newHost *ByoHost) field.ErrorList {
	status := newHost.Status.InPlaceUpgrade
	if status == nil || apiequality.Semantic.DeepEqual(status, oldHost.Status.InPlaceUpgrade) {
		return nil
	}
	path := field.NewPath("status", "inPlaceUpgrade")
	if !status.Matches(newHost.Spec.InPlaceUpgrade) {
		return field.ErrorList{field.Forbidden(path, "a host reports only the in-place upgrade of spec.inPlaceUpgrade, with its id and attempt")}
	}
	switch status.Phase {
	case InPlaceUpgradeRunning, InPlaceUpgradeFailed:
	case InPlaceUpgradeSucceeded:
		if status.ObservedKubeletVersion != newHost.Spec.InPlaceUpgrade.ToVersion || status.ObservedNodeVersion != newHost.Spec.InPlaceUpgrade.ToVersion {
			return field.ErrorList{field.Invalid(path.Child("phase"), status.Phase, "Succeeded needs the target version observed on the host and on the Node")}
		}
	default:
		return field.ErrorList{field.NotSupported(path.Child("phase"), status.Phase, []InPlaceUpgradePhase{InPlaceUpgradeRunning, InPlaceUpgradeSucceeded, InPlaceUpgradeFailed})}
	}
	return nil
}
