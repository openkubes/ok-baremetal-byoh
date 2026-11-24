// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openkubes/ok-baremetal-byoh/agent/inplaceupgrade"
	"github.com/openkubes/ok-baremetal-byoh/agent/reconciler"
	infrastructurev1beta1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	"github.com/openkubes/ok-baremetal-byoh/test/builder"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	v1beta1conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// statusOnlyClient is a host identity with least privilege: it may write the status of its ByoHost, a patch of the
// ByoHost itself is forbidden (as the per-host RBAC of a hardened management cluster does).
type statusOnlyClient struct {
	client.Client
	denied int
}

func (c *statusOnlyClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if _, ok := obj.(*infrastructurev1beta1.ByoHost); ok {
		c.denied++
		return apierrors.NewForbidden(schema.GroupResource{Group: infrastructurev1beta1.GroupVersion.Group, Resource: "byohosts"}, obj.GetName(), errors.New("only the status may be written"))
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

// fakeUpgrader records the upgrades and returns a configured outcome.
type fakeUpgrader struct {
	calls  []string
	steps  []inplaceupgrade.Step
	result inplaceupgrade.Result
	err    error
}

func (f *fakeUpgrader) Execute(_ context.Context, req *infrastructurev1beta1.InPlaceUpgradeRequest, onStep func(inplaceupgrade.Step)) (inplaceupgrade.Result, error) {
	f.calls = append(f.calls, req.ID)
	for _, step := range f.steps {
		onStep(step)
	}
	return f.result, f.err
}

var _ = Describe("Byohost Agent In-Place Upgrade", func() {
	var (
		ctx      = context.TODO()
		ns       = "default"
		upgrader *fakeUpgrader
		r        *reconciler.HostReconciler
		key      types.NamespacedName
	)

	upgradeRequest := func() *infrastructurev1beta1.InPlaceUpgradeRequest {
		return &infrastructurev1beta1.InPlaceUpgradeRequest{
			ID: "abc123", ClusterName: "ha1", ClusterUID: "uid-cluster",
			MachineName: "m1", MachineUID: "uid-m1", ByoMachineUID: "uid-bm1",
			NodeName: "upgrade-host", Role: infrastructurev1beta1.InPlaceUpgradeWorker,
			FromVersion: "v1.36.6", ToVersion: "v1.37.2",
			Packages: infrastructurev1beta1.InPlaceUpgradePackages{
				Repository:               "https://pkgs.k8s.io/core:/stable:/v1.37/deb/",
				RepositoryKeyFingerprint: "DE15B14486CD377B9E876E1A234654DA9A296436",
				Debs: []infrastructurev1beta1.PinnedDeb{
					{Name: "kubeadm", Version: "1.37.2-1.1", SHA256: "d1674bffcf80b36531375b35cf68d0dc23453a373c1b4537d78b6960732bc699"},
					{Name: "kubelet", Version: "1.37.2-1.1", SHA256: "f95d0b572a24563bb6e2a1a1f0e84a8a30a24e11c95068b2e81c294a0e1bee11"},
					{Name: "kubectl", Version: "1.37.2-1.1", SHA256: "e95d73aaf1da438cc8b287545b128fbe5e6a9f3723ecaa25eac1b42b8fe542ab"},
				},
			},
		}
	}
	reconcile := func() controllerruntime.Result {
		result, err := r.Reconcile(ctx, controllerruntime.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		return result
	}
	get := func() *infrastructurev1beta1.ByoHost {
		h := &infrastructurev1beta1.ByoHost{}
		Expect(k8sClient.Get(ctx, key, h)).To(Succeed())
		return h
	}

	BeforeEach(func() {
		upgrader = &fakeUpgrader{
			steps:  []inplaceupgrade.Step{inplaceupgrade.StepPreflight, inplaceupgrade.StepVerify},
			result: inplaceupgrade.Result{KubeletVersion: "v1.37.2", NodeVersion: "v1.37.2"},
		}
		r = &reconciler.HostReconciler{
			Client:              k8sClient,
			CmdRunner:           nil, // the upgrade path must not run commands itself
			Recorder:            events.NewFakeRecorder(32),
			UninstallScriptPath: GinkgoT().TempDir() + "/uninstall.sh",
			Upgrader:            upgrader,
		}
		byoHost := builder.ByoHost(ns, "upgrade-host").Build()
		Expect(k8sClient.Create(ctx, byoHost)).To(Succeed())
		key = types.NamespacedName{Namespace: ns, Name: byoHost.Name}
		byoHost.Spec.ByoMachineRef = &infrastructurev1beta1.ByoMachineReference{Name: "bm1", UID: "uid-bm1"}
		byoHost.Spec.BootstrapSecret = nil
		byoHost.Spec.InPlaceUpgrade = upgradeRequest()
		Expect(k8sClient.Update(ctx, byoHost)).To(Succeed())
		v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
		Expect(k8sClient.Status().Update(ctx, byoHost)).To(Succeed())
	})

	AfterEach(func() {
		h := &infrastructurev1beta1.ByoHost{}
		if err := k8sClient.Get(ctx, key, h); err == nil {
			Expect(k8sClient.Delete(ctx, h)).To(Succeed())
		}
	})

	It("reports Running before it changes the host, then the verified versions", func() {
		Expect(reconcile().RequeueAfter).To(BeNumerically(">", 0))
		Expect(upgrader.calls).To(BeEmpty())
		status := get().Status.InPlaceUpgrade
		Expect(status.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeRunning))
		Expect(status.ID).To(Equal("abc123"))

		reconcile()
		Expect(upgrader.calls).To(Equal([]string{"abc123"}))
		status = get().Status.InPlaceUpgrade
		Expect(status.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeSucceeded))
		Expect(status.ObservedKubeletVersion).To(Equal("v1.37.2"))
		Expect(status.ObservedNodeVersion).To(Equal("v1.37.2"))
		Expect(status.FinishedAt).NotTo(BeNil())

		// A finished attempt is not run again.
		reconcile()
		Expect(upgrader.calls).To(HaveLen(1))
	})

	// Seen on the ha1 lab: after the step reports the agent also patched the ByoHost itself (metadata only), which
	// its identity is not allowed to do: "failed to patch ByoHost ...: is forbidden", once per upgrade.
	It("writes only the status while it reports the steps", func() {
		limited := &statusOnlyClient{Client: k8sClient}
		r.Client = limited
		reconcile()
		reconcile()
		Expect(upgrader.calls).To(Equal([]string{"abc123"}))
		Expect(limited.denied).To(BeZero(), "the agent patched the ByoHost itself")
		status := get().Status.InPlaceUpgrade
		Expect(status.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeSucceeded))
		Expect(status.Step).To(Equal(string(inplaceupgrade.StepVerify)))

		upgrader.err = &inplaceupgrade.StepError{Step: inplaceupgrade.StepKubeadm, Err: errors.New("exit status 2")}
		h := get()
		h.Spec.InPlaceUpgrade.Attempt = 1
		Expect(k8sClient.Update(ctx, h)).To(Succeed())
		reconcile()
		reconcile()
		Expect(limited.denied).To(BeZero(), "the agent patched the ByoHost itself after a failed step")
		Expect(get().Status.InPlaceUpgrade.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeFailed))
	})

	It("reports a failed step and does not retry it until the attempt grows", func() {
		upgrader.err = &inplaceupgrade.StepError{Step: inplaceupgrade.StepKubeadmUpgrade, Err: errors.New("exit status 1")}
		upgrader.result = inplaceupgrade.Result{}
		reconcile()
		reconcile()
		status := get().Status.InPlaceUpgrade
		Expect(status.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeFailed))
		Expect(status.Step).To(Equal(string(inplaceupgrade.StepKubeadmUpgrade)))
		Expect(status.Message).To(ContainSubstring("exit status 1"))
		reconcile()
		Expect(upgrader.calls).To(HaveLen(1))

		By("an administrator raising the attempt after a forward recovery")
		upgrader.err = nil
		upgrader.result = inplaceupgrade.Result{KubeletVersion: "v1.37.2", NodeVersion: "v1.37.2"}
		h := get()
		h.Spec.InPlaceUpgrade.Attempt = 1
		Expect(k8sClient.Update(ctx, h)).To(Succeed())
		reconcile()
		reconcile()
		status = get().Status.InPlaceUpgrade
		Expect(status.Attempt).To(Equal(int32(1)))
		Expect(status.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeSucceeded))
		Expect(upgrader.calls).To(HaveLen(2))
	})

	It("never reports Succeeded when the executor fails without a step", func() {
		upgrader.err = context.DeadlineExceeded
		upgrader.result = inplaceupgrade.Result{KubeletVersion: "v1.37.2"}
		reconcile()
		reconcile()
		Expect(get().Status.InPlaceUpgrade.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeFailed))
	})

	It("resumes a Running attempt after a restart of the agent", func() {
		reconcile()
		// The agent stops while the upgrade runs: the status stays Running.
		h := get()
		h.Status.InPlaceUpgrade.Step = string(inplaceupgrade.StepKubeadmUpgrade)
		Expect(k8sClient.Status().Update(ctx, h)).To(Succeed())
		reconcile()
		Expect(upgrader.calls).To(Equal([]string{"abc123"}))
		Expect(get().Status.InPlaceUpgrade.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeSucceeded))
	})

	DescribeTable("fails without touching the host",
		func(mutate func(*infrastructurev1beta1.ByoHost, *reconciler.HostReconciler), message string) {
			// Each write returns the stored object, so the spec and the
			// status are mutated before their own write.
			h := get()
			mutate(h, r)
			Expect(k8sClient.Update(ctx, h)).To(Succeed())
			mutate(h, r)
			Expect(k8sClient.Status().Update(ctx, h)).To(Succeed())
			reconcile()
			status := get().Status.InPlaceUpgrade
			Expect(status.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeFailed))
			Expect(status.Message).To(ContainSubstring(message))
			Expect(upgrader.calls).To(BeEmpty())
		},
		Entry("a node that is not bootstrapped", func(h *infrastructurev1beta1.ByoHost, _ *reconciler.HostReconciler) {
			v1beta1conditions.MarkFalse(h, infrastructurev1beta1.K8sNodeBootstrapSucceeded, "x", clusterv1beta1.ConditionSeverityInfo, "")
		}, "not bootstrapped"),
		Entry("an agent without an upgrader", func(_ *infrastructurev1beta1.ByoHost, r *reconciler.HostReconciler) {
			r.Upgrader = nil
		}, "does not run in-place upgrades"),
		Entry("a request for another ByoMachine", func(h *infrastructurev1beta1.ByoHost, _ *reconciler.HostReconciler) {
			h.Spec.InPlaceUpgrade.ByoMachineUID = "uid-bm2"
		}, "another ByoMachine"),
	)

	It("leaves a reset to the reset path", func() {
		h := get()
		h.Spec.ResetRequested = true
		h.Spec.InPlaceUpgrade = nil
		Expect(k8sClient.Update(ctx, h)).To(Succeed())
		_, _ = r.Reconcile(ctx, controllerruntime.Request{NamespacedName: key})
		Expect(upgrader.calls).To(BeEmpty())
	})
})
