// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// InPlaceUpgradeRole is what the host is in the upgrade of its cluster. It
// selects the kubeadm upgrade command.
// +kubebuilder:validation:Enum=ControlPlaneFirst;ControlPlane;Worker
type InPlaceUpgradeRole string

const (
	// InPlaceUpgradeControlPlaneFirst is the first control-plane host of the
	// upgrade: it runs kubeadm upgrade apply.
	InPlaceUpgradeControlPlaneFirst InPlaceUpgradeRole = "ControlPlaneFirst"
	// InPlaceUpgradeControlPlane is another control-plane host: it runs
	// kubeadm upgrade node after the first one has finished.
	InPlaceUpgradeControlPlane InPlaceUpgradeRole = "ControlPlane"
	// InPlaceUpgradeWorker is a worker host: it runs kubeadm upgrade node
	// after all the control-plane hosts have finished.
	InPlaceUpgradeWorker InPlaceUpgradeRole = "Worker"
)

// InPlaceUpgradePhase is the phase of an in-place upgrade on the host.
// +kubebuilder:validation:Enum=Running;Succeeded;Failed
type InPlaceUpgradePhase string

const (
	// InPlaceUpgradeRunning means that the agent works on the upgrade, or
	// resumes it from its checkpoint after a restart.
	InPlaceUpgradeRunning InPlaceUpgradePhase = "Running"
	// InPlaceUpgradeSucceeded means that the agent has verified the target
	// version on the host and on its Node.
	InPlaceUpgradeSucceeded InPlaceUpgradePhase = "Succeeded"
	// InPlaceUpgradeFailed means that a step failed, or that the agent could
	// not verify its result. The agent does not retry it until spec.attempt
	// grows (docs/scaleway-kubeadm-e2e/inplace-upgrade-runbook.md).
	InPlaceUpgradeFailed InPlaceUpgradePhase = "Failed"
)

// InPlaceUpgradeRequest is an in-place upgrade of Kubernetes on a host. It is
// bound to the exact Cluster, Machine and ByoMachine of the attachment of the
// host, and it carries the pinned packages, so that the agent installs nothing
// that it has not verified.
type InPlaceUpgradeRequest struct {
	// ID identifies the upgrade: the extension derives it from the Machine UID
	// and the target version, so repeated UpdateMachine calls write the same
	// request. The agent keeps its checkpoint under it.
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]{1,63}$`
	ID string `json:"id"`

	// Attempt grows when an administrator asks the agent to resume a failed
	// upgrade; the agent resumes from its checkpoint.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Attempt int32 `json:"attempt,omitempty"`

	// ClusterName and ClusterUID are the Cluster of the Machine.
	// +required
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`
	// +required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	ClusterUID types.UID `json:"clusterUID"`

	// MachineName and MachineUID are the CAPI Machine that is updated.
	// +required
	// +kubebuilder:validation:MinLength=1
	MachineName string `json:"machineName"`
	// +required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	MachineUID types.UID `json:"machineUID"`

	// ByoMachineUID must be the UID in spec.byoMachineRef.
	// +required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	ByoMachineUID types.UID `json:"byoMachineUID"`

	// NodeName is the Node of the Machine, which the agent verifies.
	// +required
	// +kubebuilder:validation:MinLength=1
	NodeName string `json:"nodeName"`

	// Role selects the kubeadm upgrade command.
	// +required
	Role InPlaceUpgradeRole `json:"role"`

	// FromVersion is the version on the host before the upgrade, and
	// ToVersion the target version, such as v1.37.2. The minor version grows
	// by at most one.
	// +required
	// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+\.[0-9]+$`
	FromVersion string `json:"fromVersion"`
	// +required
	// +kubebuilder:validation:Pattern=`^v[0-9]+\.[0-9]+\.[0-9]+$`
	ToVersion string `json:"toVersion"`

	// Packages are the pinned packages of ToVersion.
	// +required
	Packages InPlaceUpgradePackages `json:"packages"`
}

// InPlaceUpgradePackages are the pinned packages of an upgrade.
type InPlaceUpgradePackages struct {
	// Repository is the apt repository of the minor version of ToVersion,
	// such as https://pkgs.k8s.io/core:/stable:/v1.37/deb/.
	// +required
	// +kubebuilder:validation:Pattern=`^https://`
	Repository string `json:"repository"`

	// RepositoryKeyFingerprint is the fingerprint of the signing key of the
	// repository, which the keyring on the host must have.
	// +required
	// +kubebuilder:validation:Pattern=`^[0-9A-F]{40}$`
	RepositoryKeyFingerprint string `json:"repositoryKeyFingerprint"`

	// Debs are the kubeadm, kubelet and kubectl packages, each with the
	// SHA256 that the agent verifies before it installs it.
	// +required
	// +kubebuilder:validation:MinItems=3
	// +kubebuilder:validation:MaxItems=3
	Debs []PinnedDeb `json:"debs"`
}

// PinnedDeb is a package with its exact version and digest.
type PinnedDeb struct {
	// Name is kubeadm, kubelet or kubectl.
	// +required
	// +kubebuilder:validation:Enum=kubeadm;kubelet;kubectl
	Name string `json:"name"`
	// Version is the package version, such as 1.37.2-1.1.
	// +required
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.]+$`
	Version string `json:"version"`
	// SHA256 is the digest of the .deb file.
	// +required
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	SHA256 string `json:"sha256"`
}

// InPlaceUpgradeStatus is the progress of an in-place upgrade on the host.
type InPlaceUpgradeStatus struct {
	// ID and Attempt are those of the request that the status reports.
	// +required
	ID string `json:"id"`
	// +optional
	Attempt int32 `json:"attempt,omitempty"`

	// Phase is the phase of the upgrade.
	// +required
	Phase InPlaceUpgradePhase `json:"phase"`

	// Step is the last step that the agent has started or finished.
	// +optional
	Step string `json:"step,omitempty"`

	// Message tells why the upgrade failed.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedKubeletVersion and ObservedNodeVersion are the versions that
	// the agent verified on the host (kubelet --version) and on the Node
	// (status.nodeInfo.kubeletVersion).
	// +optional
	ObservedKubeletVersion string `json:"observedKubeletVersion,omitempty"`
	// +optional
	ObservedNodeVersion string `json:"observedNodeVersion,omitempty"`

	// StartedAt and FinishedAt are when the agent started the attempt and
	// when it reached Succeeded or Failed.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// Deb returns the pinned package of the given name, or nil.
func (p *InPlaceUpgradePackages) Deb(name string) *PinnedDeb {
	for i := range p.Debs {
		if p.Debs[i].Name == name {
			return &p.Debs[i]
		}
	}
	return nil
}

// Matches returns whether the status reports the given request.
func (s *InPlaceUpgradeStatus) Matches(req *InPlaceUpgradeRequest) bool {
	return s != nil && req != nil && s.ID == req.ID && s.Attempt == req.Attempt
}
