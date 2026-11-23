// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// validInPlaceUpgrade returns the request of the v1.37.2 upgrade of a worker
// attached to ByoMachine uid-of-byomachine1.
func validInPlaceUpgrade() *InPlaceUpgradeRequest {
	return &InPlaceUpgradeRequest{
		ID:            "abc123",
		ClusterName:   "ha1",
		ClusterUID:    "uid-of-cluster",
		MachineName:   "machine1",
		MachineUID:    "uid-of-machine1",
		ByoMachineUID: "uid-of-byomachine1",
		NodeName:      "host1",
		Role:          InPlaceUpgradeWorker,
		FromVersion:   "v1.36.6",
		ToVersion:     "v1.37.2",
		Packages: InPlaceUpgradePackages{
			Repository:               "https://pkgs.k8s.io/core:/stable:/v1.37/deb/",
			RepositoryKeyFingerprint: "DE15B14486CD377B9E876E1A234654DA9A296436",
			Debs: []PinnedDeb{
				{Name: "kubeadm", Version: "1.37.2-1.1", SHA256: "d1674bffcf80b36531375b35cf68d0dc23453a373c1b4537d78b6960732bc699"},
				{Name: "kubelet", Version: "1.37.2-1.1", SHA256: "f95d0b572a24563bb6e2a1a1f0e84a8a30a24e11c95068b2e81c294a0e1bee11"},
				{Name: "kubectl", Version: "1.37.2-1.1", SHA256: "e95d73aaf1da438cc8b287545b128fbe5e6a9f3723ecaa25eac1b42b8fe542ab"},
			},
		},
	}
}

var _ = Describe("ByohostWebhook/InPlaceUpgrade", func() {
	schema := runtime.NewScheme()
	Expect(AddToScheme(schema)).To(Succeed())
	v := NewByoHostValidator(schema)

	attached := func() *ByoHost {
		return &ByoHost{
			TypeMeta:   metav1.TypeMeta{Kind: "ByoHost", APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1"},
			ObjectMeta: metav1.ObjectMeta{Name: "host1", Namespace: "default"},
			Spec:       ByoHostSpec{ByoMachineRef: &ByoMachineReference{Name: "byomachine1", UID: "uid-of-byomachine1"}},
		}
	}
	raw := func(h *ByoHost) runtime.RawExtension {
		data, err := json.Marshal(h)
		Expect(err).NotTo(HaveOccurred())
		return runtime.RawExtension{Raw: data}
	}
	update := func(subResource string, oldHost, newHost *ByoHost, user v1.UserInfo) admission.Response {
		return v.Handle(context.TODO(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Update, SubResource: subResource, UserInfo: user,
			Object: raw(newHost), OldObject: raw(oldHost),
		}})
	}
	extension := v1.UserInfo{Username: "system:serviceaccount:byoh-system:okx-inplace-extension", Groups: []string{"system:serviceaccounts", "system:authenticated"}}
	agent := v1.UserInfo{Username: "byoh:host:default:host1", Groups: []string{"byoh:hosts", "system:authenticated"}}

	Context("When the extension sets an upgrade", func() {
		It("Should allow a valid request for the attachment", func() {
			newHost := attached()
			newHost.Spec.InPlaceUpgrade = validInPlaceUpgrade()
			Expect(update("", attached(), newHost, extension).Allowed).To(BeTrue())
		})

		DescribeTable("Should reject an invalid request",
			func(mutate func(*ByoHost), message string) {
				newHost := attached()
				newHost.Spec.InPlaceUpgrade = validInPlaceUpgrade()
				mutate(newHost)
				resp := update("", attached(), newHost, extension)
				Expect(resp.Allowed).To(BeFalse())
				Expect(resp.Result.Message).To(ContainSubstring(message))
			},
			Entry("another ByoMachine", func(h *ByoHost) { h.Spec.InPlaceUpgrade.ByoMachineUID = "uid-of-byomachine2" }, "must be the UID in spec.byoMachineRef"),
			Entry("a free host", func(h *ByoHost) { h.Spec.ByoMachineRef = nil }, "needs an attached ByoHost"),
			Entry("a host that is reset", func(h *ByoHost) { h.Spec.ResetRequested = true }, "a host that is reset is not upgraded"),
			Entry("a downgrade", func(h *ByoHost) { h.Spec.InPlaceUpgrade.FromVersion = "v1.37.3" }, "only moves to a later version"),
			Entry("the same version", func(h *ByoHost) { h.Spec.InPlaceUpgrade.FromVersion = "v1.37.2" }, "only moves to a later version"),
			Entry("a skipped minor version", func(h *ByoHost) { h.Spec.InPlaceUpgrade.FromVersion = "v1.35.9" }, "at most one minor version"),
			Entry("another major version", func(h *ByoHost) { h.Spec.InPlaceUpgrade.ToVersion = "v2.0.0" }, "major version cannot change"),
			Entry("a version with a suffix", func(h *ByoHost) { h.Spec.InPlaceUpgrade.ToVersion = "v1.37.2+okx" }, "must be vX.Y.Z"),
			Entry("the repository of another minor version", func(h *ByoHost) {
				h.Spec.InPlaceUpgrade.Packages.Repository = "https://pkgs.k8s.io/core:/stable:/v1.36/deb/"
			}, "must be https://pkgs.k8s.io/core:/stable:/v1.37/deb/"),
			Entry("a foreign repository", func(h *ByoHost) {
				h.Spec.InPlaceUpgrade.Packages.Repository = "https://example.com/v1.37/deb/"
			}, "must be https://pkgs.k8s.io"),
			Entry("a short fingerprint", func(h *ByoHost) { h.Spec.InPlaceUpgrade.Packages.RepositoryKeyFingerprint = "DE15B144" }, "40 upper-case"),
			Entry("a package of another version", func(h *ByoHost) { h.Spec.InPlaceUpgrade.Packages.Debs[1].Version = "1.37.1-1.1" }, "must be a package of v1.37.2"),
			Entry("a malformed digest", func(h *ByoHost) { h.Spec.InPlaceUpgrade.Packages.Debs[0].SHA256 = "XYZ" }, "64 lower-case"),
			Entry("a missing package", func(h *ByoHost) {
				h.Spec.InPlaceUpgrade.Packages.Debs = h.Spec.InPlaceUpgrade.Packages.Debs[:2]
			}, "a pinned kubectl package"),
			Entry("a duplicated package", func(h *ByoHost) {
				h.Spec.InPlaceUpgrade.Packages.Debs[2] = h.Spec.InPlaceUpgrade.Packages.Debs[0]
			}, "Duplicate value"),
			Entry("an unknown role", func(h *ByoHost) { h.Spec.InPlaceUpgrade.Role = "Etcd" }, "Unsupported value"),
			Entry("no Machine UID", func(h *ByoHost) { h.Spec.InPlaceUpgrade.MachineUID = "" }, "machineUID"),
		)
	})

	Context("When an upgrade is set", func() {
		var oldHost *ByoHost
		BeforeEach(func() {
			oldHost = attached()
			oldHost.Spec.InPlaceUpgrade = validInPlaceUpgrade()
			oldHost.Status.InPlaceUpgrade = &InPlaceUpgradeStatus{ID: "abc123", Phase: InPlaceUpgradeRunning}
		})

		It("Should allow a larger attempt", func() {
			newHost := oldHost.DeepCopy()
			newHost.Spec.InPlaceUpgrade.Attempt = 1
			Expect(update("", oldHost, newHost, extension).Allowed).To(BeTrue())
		})

		DescribeTable("Should reject a change of a running upgrade",
			func(mutate func(*ByoHost), message string) {
				newHost := oldHost.DeepCopy()
				mutate(newHost)
				resp := update("", oldHost, newHost, extension)
				Expect(resp.Allowed).To(BeFalse())
				Expect(resp.Result.Message).To(ContainSubstring(message))
			},
			Entry("another target", func(h *ByoHost) {
				h.Spec.InPlaceUpgrade.ToVersion = "v1.37.1"
				for i := range h.Spec.InPlaceUpgrade.Packages.Debs {
					h.Spec.InPlaceUpgrade.Packages.Debs[i].Version = "1.37.1-1.1"
				}
			}, "immutable except for spec.inPlaceUpgrade.attempt"),
			Entry("another digest", func(h *ByoHost) {
				h.Spec.InPlaceUpgrade.Packages.Debs[0].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
			}, "immutable"),
			Entry("a smaller attempt", func(h *ByoHost) { h.Spec.InPlaceUpgrade.Attempt = -1 }, "cannot decrease"),
			Entry("its removal", func(h *ByoHost) { h.Spec.InPlaceUpgrade = nil }, "cannot remove an in-place upgrade while the agent runs it"),
			Entry("another request", func(h *ByoHost) { h.Spec.InPlaceUpgrade.ID = "def456" }, "cannot replace an in-place upgrade while the agent runs it"),
		)

		It("Should allow the removal of a finished upgrade", func() {
			oldHost.Status.InPlaceUpgrade.Phase = InPlaceUpgradeSucceeded
			newHost := oldHost.DeepCopy()
			newHost.Spec.InPlaceUpgrade = nil
			Expect(update("", oldHost, newHost, extension).Allowed).To(BeTrue())
		})
	})

	Context("When the agent reports an upgrade", func() {
		var oldHost *ByoHost
		BeforeEach(func() {
			oldHost = attached()
			oldHost.Spec.InPlaceUpgrade = validInPlaceUpgrade()
		})

		It("Should allow the report of the request", func() {
			newHost := oldHost.DeepCopy()
			newHost.Status.InPlaceUpgrade = &InPlaceUpgradeStatus{ID: "abc123", Phase: InPlaceUpgradeRunning, Step: "Download"}
			Expect(update("status", oldHost, newHost, agent).Allowed).To(BeTrue())
		})

		It("Should allow Succeeded with the target version observed on the host and on the Node", func() {
			newHost := oldHost.DeepCopy()
			newHost.Status.InPlaceUpgrade = &InPlaceUpgradeStatus{ID: "abc123", Phase: InPlaceUpgradeSucceeded, ObservedKubeletVersion: "v1.37.2", ObservedNodeVersion: "v1.37.2"}
			Expect(update("status", oldHost, newHost, agent).Allowed).To(BeTrue())
		})

		DescribeTable("Should reject a report that does not match the request",
			func(status InPlaceUpgradeStatus, message string) {
				newHost := oldHost.DeepCopy()
				newHost.Status.InPlaceUpgrade = &status
				resp := update("status", oldHost, newHost, agent)
				Expect(resp.Allowed).To(BeFalse())
				Expect(resp.Result.Message).To(ContainSubstring(message))
			},
			Entry("another request", InPlaceUpgradeStatus{ID: "def456", Phase: InPlaceUpgradeRunning}, "only the in-place upgrade of spec.inPlaceUpgrade"),
			Entry("another attempt", InPlaceUpgradeStatus{ID: "abc123", Attempt: 2, Phase: InPlaceUpgradeRunning}, "with its id and attempt"),
			Entry("Succeeded without the observed versions", InPlaceUpgradeStatus{ID: "abc123", Phase: InPlaceUpgradeSucceeded}, "Succeeded needs the target version"),
			Entry("Succeeded with the old Node version", InPlaceUpgradeStatus{ID: "abc123", Phase: InPlaceUpgradeSucceeded, ObservedKubeletVersion: "v1.37.2", ObservedNodeVersion: "v1.36.6"}, "Succeeded needs the target version"),
			Entry("an unknown phase", InPlaceUpgradeStatus{ID: "abc123", Phase: "Done"}, "Unsupported value"),
		)

		It("Should reject a report without a request", func() {
			oldHost.Spec.InPlaceUpgrade = nil
			newHost := oldHost.DeepCopy()
			newHost.Status.InPlaceUpgrade = &InPlaceUpgradeStatus{ID: "abc123", Phase: InPlaceUpgradeSucceeded, ObservedKubeletVersion: "v1.37.2", ObservedNodeVersion: "v1.37.2"}
			Expect(update("status", oldHost, newHost, agent).Allowed).To(BeFalse())
		})

		It("Should keep an unchanged report of an earlier request", func() {
			oldHost.Status.InPlaceUpgrade = &InPlaceUpgradeStatus{ID: "old", Phase: InPlaceUpgradeSucceeded}
			newHost := oldHost.DeepCopy()
			newHost.Status.HostInfo.OSImage = "Ubuntu 24.04.5 LTS"
			Expect(update("status", oldHost, newHost, agent).Allowed).To(BeTrue())
		})
	})
})
