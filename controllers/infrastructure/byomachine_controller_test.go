// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	infrastructurev1beta1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	controllers "github.com/openkubes/ok-baremetal-byoh/controllers/infrastructure"
	"github.com/openkubes/ok-baremetal-byoh/test/builder"
	eventutils "github.com/openkubes/ok-baremetal-byoh/test/utils/events"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	v1beta1conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions"
	v1beta1patch "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("Controllers/ByomachineController", func() {
	var (
		byoMachineLookupKey        types.NamespacedName
		byoHostLookupKey           types.NamespacedName
		byoMachine                 *infrastructurev1beta1.ByoMachine
		machine                    *clusterv1.Machine
		node                       *corev1.Node
		k8sClientUncached          client.Client
		k8sInstallerConfigTemplate *infrastructurev1beta1.K8sInstallerConfigTemplate
		k8sInstallerConfig         *infrastructurev1beta1.K8sInstallerConfig
		byoHost                    *infrastructurev1beta1.ByoHost
		testClusterVersion         = "v1.22.1_xyz"
	)

	BeforeEach(func() {
		ctx = context.Background()

		var clientErr error
		k8sClientUncached, clientErr = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(clientErr).NotTo(HaveOccurred())

		machine = builder.Machine(defaultNamespace, defaultMachineName).
			WithClusterName(defaultClusterName).
			WithClusterVersion(testClusterVersion).
			WithBootstrapDataSecret(fakeBootstrapSecret).
			Build()
		Expect(k8sClientUncached.Create(ctx, machine)).Should(Succeed())

		byoMachine = builder.ByoMachine(defaultNamespace, defaultByoMachineName).
			WithClusterLabel(defaultClusterName).
			WithOwnerMachine(machine).
			Build()
		Expect(k8sClientUncached.Create(ctx, byoMachine)).Should(Succeed())

		WaitForObjectsToBePopulatedInCache(machine, byoMachine)
		byoMachineLookupKey = types.NamespacedName{Name: byoMachine.Name, Namespace: byoMachine.Namespace}

		k8sInstallerConfigTemplate = builder.K8sInstallerConfigTemplate(defaultNamespace, defaultK8sInstallerConfigTemplateName).
			Build()
		Expect(k8sClientUncached.Create(ctx, k8sInstallerConfigTemplate)).Should(Succeed())
		WaitForObjectsToBePopulatedInCache(k8sInstallerConfigTemplate)
	})

	AfterEach(func() {
		eventutils.DrainEvents(recorder.Events)
	})

	It("should ignore byomachine if it is not found", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "non-existent-byomachine",
				Namespace: "non-existent-namespace"}})
		Expect(err).NotTo(HaveOccurred())
	})

	It("should return error when cluster does not exist", func() {
		machineForByoMachineWithoutCluster := builder.Machine(defaultNamespace, "machine-for-a-byomachine-without-cluster").
			WithClusterName(defaultClusterName).
			Build()
		Expect(k8sClientUncached.Create(ctx, machineForByoMachineWithoutCluster)).Should(Succeed())

		byoMachineWithNonExistingCluster := builder.ByoMachine(defaultNamespace, defaultByoMachineName).
			WithClusterLabel("non-existent-cluster").
			WithOwnerMachine(machine).
			Build()
		Expect(k8sClientUncached.Create(ctx, byoMachineWithNonExistingCluster)).Should(Succeed())

		WaitForObjectsToBePopulatedInCache(machineForByoMachineWithoutCluster, byoMachineWithNonExistingCluster)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      byoMachineWithNonExistingCluster.Name,
				Namespace: byoMachineWithNonExistingCluster.Namespace}})
		Expect(err).To(MatchError("failed to get Cluster default/non-existent-cluster: Cluster.cluster.x-k8s.io \"non-existent-cluster\" not found"))
	})

	Context("When cluster infrastructure is ready", func() {
		BeforeEach(func() {
			ph, err := patch.NewHelper(capiCluster, k8sClientUncached)
			Expect(err).ShouldNot(HaveOccurred())
			capiCluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
			Expect(ph.Patch(ctx, capiCluster, patch.WithStatusObservedGeneration{})).Should(Succeed())

			WaitForObjectToBeUpdatedInCache(capiCluster, func(object client.Object) bool {
				return ptr.Deref(object.(*clusterv1.Cluster).Status.Initialization.InfrastructureProvisioned, false)
			})
		})

		It("should return error when node is not available", func() {
			byoHost = builder.ByoHost(defaultNamespace, "host-with-node-missing").Build()
			Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())

			WaitForObjectsToBePopulatedInCache(byoHost)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
			Expect(err).To(MatchError("nodes \"" + byoHost.Name + "\" not found"))
		})

		Context("When node.Spec.ProviderID is already set", func() {

			BeforeEach(func() {
				byoHost = builder.ByoHost(defaultNamespace, "test-node-providerid-host").Build()
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())
				WaitForObjectsToBePopulatedInCache(byoHost)
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost)).ToNot(HaveOccurred())
			})

			It("should not return error when node.Spec.ProviderID is with correct value", func() {
				node = builder.Node(defaultNamespace, byoHost.Name).
					WithProviderID(fmt.Sprintf("%s%s/%s", controllers.ProviderIDPrefix, byoHost.Name, util.RandomString(controllers.ProviderIDSuffixLength))).
					Build()
				Expect(clientFake.Create(ctx, node)).Should(Succeed())
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())
			})

			It("should return error when node.Spec.ProviderID has stale value", func() {
				node = builder.Node(defaultNamespace, byoHost.Name).
					WithProviderID(fmt.Sprintf("%sanother-host/%s", controllers.ProviderIDPrefix, util.RandomString(controllers.ProviderIDSuffixLength))).
					Build()
				Expect(clientFake.Create(ctx, node)).Should(Succeed())
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).To(MatchError("invalid format for node.Spec.ProviderID"))
			})
		})

		Context("When BYO Hosts are not available", func() {
			It("should mark BYOHostReady as False", func() {
				res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(reconciler.RequeuePeriod))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())
				actualCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)

				Expect(*actualCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BYOHostsUnavailableReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					"Warning ByoHostSelectionFailed No available ByoHost",
				}))
			})

			It("should attach a ByoHost that is registered while it waits", func() {
				res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(reconciler.RequeuePeriod))

				byoHost = builder.ByoHost(defaultNamespace, "host-registered-while-waiting").Build()
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())
				DeferCleanup(func() {
					Expect(k8sClientUncached.Delete(ctx, byoHost)).Should(Succeed())
				})
				WaitForObjectsToBePopulatedInCache(byoHost)
				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, byoHost.Name).Build())).Should(Succeed())

				res, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(BeZero())

				createdByoHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClientUncached.Get(ctx, types.NamespacedName{Name: byoHost.Name, Namespace: defaultNamespace}, createdByoHost)).Should(Succeed())
				Expect(createdByoHost.Spec.ByoMachineRef).To(Equal(&infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}))
			})

			It("should add MachineFinalizer on ByoMachine", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())

				updatedByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, updatedByoMachine)
				Expect(err).ToNot(HaveOccurred())
				Expect(controllerutil.ContainsFinalizer(updatedByoMachine, infrastructurev1beta1.MachineFinalizer)).To(BeTrue())
			})

			It("should be able to delete ByoMachine", func() {
				ph, err := v1beta1patch.NewHelper(byoMachine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())
				controllerutil.AddFinalizer(byoMachine, infrastructurev1beta1.MachineFinalizer)
				Expect(ph.Patch(ctx, byoMachine, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())

				Expect(k8sClientUncached.Delete(ctx, byoMachine)).Should(Succeed())
				WaitForObjectToBeUpdatedInCache(byoMachine, func(object client.Object) bool {
					return !object.(*infrastructurev1beta1.ByoMachine).ObjectMeta.DeletionTimestamp.IsZero()
				})
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(len(events)).Should(Equal(0))

				// assert ByoMachine does not exists
				deletedByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, deletedByoMachine)
				Expect(err).To(MatchError(fmt.Sprintf("byomachines.infrastructure.cluster.x-k8s.io %q not found", byoMachineLookupKey.Name)))

			})
		})

		Context("When a single BYO Host is available", func() {
			BeforeEach(func() {
				byoHost = builder.ByoHost(defaultNamespace, "single-available-default-host").Build()
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())

				node = builder.Node(defaultNamespace, byoHost.Name).Build()
				Expect(clientFake.Create(ctx, node)).Should(Succeed())
				WaitForObjectsToBePopulatedInCache(byoHost)

				byoHostLookupKey = types.NamespacedName{Name: byoHost.Name, Namespace: byoHost.Namespace}
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost)).ToNot(HaveOccurred())
			})

			It("claims the first available host", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoHost := &infrastructurev1beta1.ByoHost{}
				err = k8sClientUncached.Get(ctx, byoHostLookupKey, createdByoHost)
				Expect(err).ToNot(HaveOccurred())
				Expect(createdByoHost.Spec.ByoMachineRef).To(Equal(&infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}))
				Expect(createdByoHost.Spec.BootstrapSecret).NotTo(BeNil())
				Expect(createdByoHost.Spec.BootstrapSecret.Namespace).To(Equal(byoMachine.Namespace))
				Expect(createdByoHost.Spec.BootstrapSecret.Name).To(Equal(fakeBootstrapSecret))

				// The controller writes only the spec of the ByoHost.
				Expect(createdByoHost.GetLabels()).To(BeEmpty())
				Expect(createdByoHost.GetAnnotations()).To(BeEmpty())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())
				Expect(createdByoMachine.Spec.ProviderID).To(ContainSubstring(controllers.ProviderIDPrefix))
				Expect(createdByoMachine.Status.Ready).To(BeTrue())

				actualCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:   infrastructurev1beta1.BYOHostReady,
					Status: corev1.ConditionTrue,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					fmt.Sprintf("Normal ByoHostAttachSucceeded Attached to ByoMachine %s", createdByoMachine.Name),
					fmt.Sprintf("Normal NodeProvisionedSucceeded Provisioned Node %s", createdByoHost.Name),
					fmt.Sprintf("Normal ByoHostAttachSucceeded Attached ByoHost %s", createdByoHost.Name),
				}))

				node := corev1.Node{}
				err = clientFake.Get(ctx, types.NamespacedName{Name: byoHost.Name, Namespace: defaultNamespace}, &node)
				Expect(err).NotTo(HaveOccurred())

				Expect(node.Spec.ProviderID).To(ContainSubstring(controllers.ProviderIDPrefix))
			})

			Context("When ByoMachine is attached to a host", func() {
				BeforeEach(func() {
					attachByoHostToByoMachine(k8sClientUncached, byoHost, byoMachine)
				})

				It("should not pause the host when the ByoMachine is paused", func() {
					ph, err := v1beta1patch.NewHelper(byoMachine, k8sClientUncached)
					Expect(err).ShouldNot(HaveOccurred())
					pauseAnnotations := map[string]string{
						clusterv1.PausedAnnotation: "paused",
					}
					annotations.AddAnnotations(byoMachine, pauseAnnotations)
					Expect(ph.Patch(ctx, byoMachine, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())
					WaitForObjectToBeUpdatedInCache(byoMachine, func(object client.Object) bool {
						return annotations.HasPaused(object.(*infrastructurev1beta1.ByoMachine))
					})

					_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
					Expect(err).ToNot(HaveOccurred())

					createdByoHost := &infrastructurev1beta1.ByoHost{}
					err = k8sClientUncached.Get(ctx, byoHostLookupKey, createdByoHost)
					Expect(err).ToNot(HaveOccurred())
					Expect(createdByoHost.Annotations).NotTo(HaveKey(clusterv1.PausedAnnotation))
				})

				It("should keep the paused annotation that an administrator has added to the host", func() {
					ph, err := v1beta1patch.NewHelper(byoHost, k8sClientUncached)
					Expect(err).ShouldNot(HaveOccurred())
					pauseAnnotations := map[string]string{
						clusterv1.PausedAnnotation: "",
					}
					annotations.AddAnnotations(byoHost, pauseAnnotations)
					Expect(ph.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())
					WaitForObjectToBeUpdatedInCache(byoHost, func(object client.Object) bool {
						return annotations.HasPaused(object.(*infrastructurev1beta1.ByoHost))
					})

					_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
					Expect(err).ToNot(HaveOccurred())

					createdByoHost := &infrastructurev1beta1.ByoHost{}
					err = k8sClientUncached.Get(ctx, byoHostLookupKey, createdByoHost)
					Expect(err).ToNot(HaveOccurred())
					Expect(createdByoHost.Annotations).To(HaveKey(clusterv1.PausedAnnotation))
				})

				It("should set host platform info from byohost to byomachine", func() {
					ph, err := v1beta1patch.NewHelper(byoHost, k8sClientUncached)
					Expect(err).ShouldNot(HaveOccurred())
					byoHost.Status.HostInfo = infrastructurev1beta1.HostInfo{
						OperatingSystem: "linux",
						OSImage:         "Ubuntu 20.04.4 LTS",
						Architecture:    "arm64",
					}
					Expect(ph.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())

					WaitForObjectToBeUpdatedInCache(byoHost, func(object client.Object) bool {
						return object.(*infrastructurev1beta1.ByoHost).Status.HostInfo == infrastructurev1beta1.HostInfo{
							OperatingSystem: "linux",
							OSImage:         "Ubuntu 20.04.4 LTS",
							Architecture:    "arm64",
						}
					})

					_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
					Expect(err).ToNot(HaveOccurred())
					patchedByoMachine := &infrastructurev1beta1.ByoMachine{}
					err = k8sClientUncached.Get(ctx, byoMachineLookupKey, patchedByoMachine)
					Expect(err).ToNot(HaveOccurred())
					Expect(patchedByoMachine.Status.HostInfo).To(Equal(byoHost.Status.HostInfo))

				})

				Context("When ByoMachine is deleted", func() {
					BeforeEach(func() {
						ph, err := v1beta1patch.NewHelper(byoMachine, k8sClientUncached)
						Expect(err).ShouldNot(HaveOccurred())
						controllerutil.AddFinalizer(byoMachine, infrastructurev1beta1.MachineFinalizer)
						Expect(ph.Patch(ctx, byoMachine, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())

						Expect(k8sClientUncached.Delete(ctx, byoMachine)).Should(Succeed())

						WaitForObjectToBeUpdatedInCache(byoMachine, func(object client.Object) bool {
							return !object.(*infrastructurev1beta1.ByoMachine).ObjectMeta.DeletionTimestamp.IsZero()
						})
					})

					It("should request the agent to reset the host, and keep the ByoMachine until then", func() {
						res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
						Expect(err).NotTo(HaveOccurred())
						// The ByoHost watch reconciles the ByoMachine again.
						Expect(res.IsZero()).To(BeTrue())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						Expect(k8sClientUncached.Get(ctx, byoHostLookupKey, updatedByoHost)).Should(Succeed())
						Expect(updatedByoHost.Spec.ResetRequested).To(BeTrue())
						Expect(updatedByoHost.Spec.ByoMachineRef).NotTo(BeNil())

						updatedByoMachine := &infrastructurev1beta1.ByoMachine{}
						Expect(k8sClientUncached.Get(ctx, byoMachineLookupKey, updatedByoMachine)).Should(Succeed())
						Expect(controllerutil.ContainsFinalizer(updatedByoMachine, infrastructurev1beta1.MachineFinalizer)).To(BeTrue())
						Expect(*v1beta1conditions.Get(updatedByoMachine, infrastructurev1beta1.BYOHostReady)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
							Type:     infrastructurev1beta1.BYOHostReady,
							Status:   corev1.ConditionFalse,
							Reason:   infrastructurev1beta1.WaitingForHostResetReason,
							Severity: clusterv1beta1.ConditionSeverityInfo,
						}))

						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							fmt.Sprintf("Normal ByoHostResetRequested Requested the reset of ByoHost %s", byoHost.Name),
							fmt.Sprintf("Normal ByoHostResetRequested Reset requested by ByoMachine %s", byoMachine.Name),
						}))
					})

					// A host that was upgraded in place keeps the request in its
					// spec, and the ByoHost webhook refuses a reset beside a
					// request. The request of an ended upgrade leaves the spec in
					// the patch that requests the reset.
					Context("When the ByoHost has an in-place upgrade in its spec", func() {
						var hostBefore *infrastructurev1beta1.ByoHost

						// reconcileOnce reconciles the ByoMachine and returns whether
						// the reconciler asked for nothing more (no requeue).
						reconcileOnce := func() bool {
							res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
							Expect(err).NotTo(HaveOccurred())
							return res.IsZero()
						}
						hostNow := func() *infrastructurev1beta1.ByoHost {
							host := &infrastructurev1beta1.ByoHost{}
							Expect(k8sClientUncached.Get(ctx, byoHostLookupKey, host)).Should(Succeed())
							return host
						}
						expectWaitingForUpgrade := func() {
							host := hostNow()
							Expect(host.Spec.ResetRequested).To(BeFalse())
							Expect(host.Spec.InPlaceUpgrade).To(Equal(hostBefore.Spec.InPlaceUpgrade))
							Expect(host.ResourceVersion).To(Equal(hostBefore.ResourceVersion), "the ByoHost was written")
							updatedByoMachine := &infrastructurev1beta1.ByoMachine{}
							Expect(k8sClientUncached.Get(ctx, byoMachineLookupKey, updatedByoMachine)).Should(Succeed())
							Expect(controllerutil.ContainsFinalizer(updatedByoMachine, infrastructurev1beta1.MachineFinalizer)).To(BeTrue())
							condition := v1beta1conditions.Get(updatedByoMachine, infrastructurev1beta1.BYOHostReady)
							Expect(condition.Status).To(Equal(corev1.ConditionFalse))
							Expect(condition.Reason).To(Equal(infrastructurev1beta1.WaitingForInPlaceUpgradeReason))
							Expect(eventutils.CollectEvents(recorder.Events)).To(BeEmpty())
						}
						expectResetRequested := func(phase infrastructurev1beta1.InPlaceUpgradePhase) {
							host := hostNow()
							Expect(host.Spec.ResetRequested).To(BeTrue())
							Expect(host.Spec.InPlaceUpgrade).To(BeNil())
							Expect(host.Spec.ByoMachineRef).To(Equal(hostBefore.Spec.ByoMachineRef))
							Expect(host.Spec.BootstrapSecret).To(Equal(hostBefore.Spec.BootstrapSecret))
							// The result of the upgrade stays in the status.
							Expect(host.Status.InPlaceUpgrade).To(Equal(hostBefore.Status.InPlaceUpgrade))
							Expect(host.Status.InPlaceUpgrade.Phase).To(Equal(phase))
						}

						BeforeEach(func() {
							requestInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "abc123", 0)
						})

						It("should remove the request of a succeeded upgrade in the patch that requests the reset, then release the host", func() {
							hostBefore = reportInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "abc123", 0, infrastructurev1beta1.InPlaceUpgradeSucceeded)

							Expect(reconcileOnce()).To(BeTrue())
							expectResetRequested(infrastructurev1beta1.InPlaceUpgradeSucceeded)
							// One write of the ByoHost for both changes.
							Expect(hostNow().Generation).To(Equal(hostBefore.Generation + 1))
							updatedByoMachine := &infrastructurev1beta1.ByoMachine{}
							Expect(k8sClientUncached.Get(ctx, byoMachineLookupKey, updatedByoMachine)).Should(Succeed())
							Expect(v1beta1conditions.Get(updatedByoMachine, infrastructurev1beta1.BYOHostReady).Reason).To(Equal(infrastructurev1beta1.WaitingForHostResetReason))
							Expect(eventutils.CollectEvents(recorder.Events)).Should(ConsistOf([]string{
								fmt.Sprintf("Normal ByoHostInPlaceUpgradeEnded Request of succeeded in-place upgrade abc123 to v1.37.2 (attempt 0) removed for the reset by ByoMachine %s; status.inPlaceUpgrade keeps its result", byoMachine.Name),
								fmt.Sprintf("Normal ByoHostResetRequested Requested the reset of ByoHost %s", byoHost.Name),
								fmt.Sprintf("Normal ByoHostResetRequested Reset requested by ByoMachine %s", byoMachine.Name),
							}))

							// A retry before the agent has reset the host changes nothing.
							requested := hostNow()
							WaitForObjectToBeUpdatedInCache(requested, func(object client.Object) bool {
								return object.(*infrastructurev1beta1.ByoHost).Spec.ResetRequested
							})
							Expect(reconcileOnce()).To(BeTrue())
							Expect(hostNow().ResourceVersion).To(Equal(requested.ResourceVersion))
							Expect(eventutils.CollectEvents(recorder.Events)).To(BeEmpty())

							reportHostReset(k8sClientUncached, requested, byoMachine.UID)
							reconcileOnce()
							released := hostNow()
							Expect(released.Spec).To(Equal(infrastructurev1beta1.ByoHostSpec{}))
							Expect(released.Status.InPlaceUpgrade).To(Equal(hostBefore.Status.InPlaceUpgrade))
							err := k8sClientUncached.Get(ctx, byoMachineLookupKey, &infrastructurev1beta1.ByoMachine{})
							Expect(apierrors.IsNotFound(err)).To(BeTrue())
						})

						It("should not request the reset while the agent runs the upgrade, and request it when the upgrade has ended", func() {
							hostBefore = reportInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "abc123", 0, infrastructurev1beta1.InPlaceUpgradeRunning)

							Expect(reconcileOnce()).To(BeTrue())
							expectWaitingForUpgrade()
							// Again, with the same result (a restart of the controller).
							Expect(reconcileOnce()).To(BeTrue())
							expectWaitingForUpgrade()

							hostBefore = reportInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "abc123", 0, infrastructurev1beta1.InPlaceUpgradeSucceeded)
							Expect(reconcileOnce()).To(BeTrue())
							expectResetRequested(infrastructurev1beta1.InPlaceUpgradeSucceeded)
						})

						It("should not request the reset after a failed upgrade, whose command may still run on the host", func() {
							hostBefore = reportInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "abc123", 0, infrastructurev1beta1.InPlaceUpgradeFailed)

							Expect(reconcileOnce()).To(BeTrue())
							expectWaitingForUpgrade()
							// The failure stays as it was reported.
							Expect(hostNow().Status.InPlaceUpgrade.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeFailed))
							Expect(hostNow().Status.InPlaceUpgrade.Message).To(Equal("step failed"))
							updatedByoMachine := &infrastructurev1beta1.ByoMachine{}
							Expect(k8sClientUncached.Get(ctx, byoMachineLookupKey, updatedByoMachine)).Should(Succeed())
							Expect(v1beta1conditions.Get(updatedByoMachine, infrastructurev1beta1.BYOHostReady).Message).To(ContainSubstring("has failed"))
						})

						It("should use the ordinary reset when an administrator has removed the failed request in a recovery", func() {
							reportInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "abc123", 0, infrastructurev1beta1.InPlaceUpgradeFailed)
							host := hostNow()
							host.Spec.InPlaceUpgrade = nil
							Expect(k8sClientUncached.Update(ctx, host)).Should(Succeed())
							WaitForObjectToBeUpdatedInCache(host, func(object client.Object) bool {
								return object.(*infrastructurev1beta1.ByoHost).Spec.InPlaceUpgrade == nil
							})

							Expect(reconcileOnce()).To(BeTrue())
							reset := hostNow()
							Expect(reset.Spec.ResetRequested).To(BeTrue())
							Expect(reset.Status.InPlaceUpgrade.Phase).To(Equal(infrastructurev1beta1.InPlaceUpgradeFailed))
							Expect(eventutils.CollectEvents(recorder.Events)).Should(ConsistOf([]string{
								fmt.Sprintf("Normal ByoHostResetRequested Requested the reset of ByoHost %s", byoHost.Name),
								fmt.Sprintf("Normal ByoHostResetRequested Reset requested by ByoMachine %s", byoMachine.Name),
							}))
						})

						It("should not request the reset for a request that the agent has not reported", func() {
							hostBefore = hostNow()
							Expect(hostBefore.Status.InPlaceUpgrade).To(BeNil())

							Expect(reconcileOnce()).To(BeTrue())
							expectWaitingForUpgrade()
						})

						It("should not request the reset when the status reports an earlier attempt", func() {
							reportInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "abc123", 0, infrastructurev1beta1.InPlaceUpgradeFailed)
							// An administrator resumes the failed upgrade: the agent has not accepted attempt 1 yet.
							requestInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "abc123", 1)
							hostBefore = hostNow()

							Expect(reconcileOnce()).To(BeTrue())
							expectWaitingForUpgrade()
						})

						It("should not request the reset when the status reports another upgrade", func() {
							hostBefore = reportInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "earlier", 0, infrastructurev1beta1.InPlaceUpgradeSucceeded)

							Expect(reconcileOnce()).To(BeTrue())
							expectWaitingForUpgrade()
						})

						It("should not overwrite a request that changes while the reset is requested", func() {
							reportInPlaceUpgrade(k8sClientUncached, byoHostLookupKey, "abc123", 0, infrastructurev1beta1.InPlaceUpgradeSucceeded)

							watchClient, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme.Scheme})
							Expect(err).NotTo(HaveOccurred())
							raced := false
							racingReconciler := &controllers.ByoMachineReconciler{
								ClusterCache: reconciler.ClusterCache, Recorder: recorder, RequeuePeriod: reconciler.RequeuePeriod,
								Client: interceptor.NewClient(watchClient, interceptor.Funcs{
									// The controller lists the ByoHosts with the index of the cache.
									List: func(ctx context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
										return reconciler.Client.List(ctx, list, opts...)
									},
									// The request changes (a new attempt) after the controller
									// has read the succeeded upgrade, and before its patch.
									Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
										if _, ok := obj.(*infrastructurev1beta1.ByoHost); ok && !raced {
											raced = true
											requestInPlaceUpgrade(c, byoHostLookupKey, "abc123", 1)
										}
										return c.Patch(ctx, obj, p, opts...)
									},
								}),
							}

							res, err := racingReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
							Expect(err).NotTo(HaveOccurred())
							Expect(raced).To(BeTrue())
							Expect(res.RequeueAfter).To(BeNumerically(">", 0))

							host := hostNow()
							Expect(host.Spec.ResetRequested).To(BeFalse())
							Expect(host.Spec.InPlaceUpgrade).NotTo(BeNil())
							Expect(host.Spec.InPlaceUpgrade.Attempt).To(Equal(int32(1)))
							Expect(eventutils.CollectEvents(recorder.Events)).To(BeEmpty())

							// The retry sees the new attempt and waits for it.
							hostBefore = host
							Expect(reconcileOnce()).To(BeTrue())
							expectWaitingForUpgrade()
						})
					})

					It("should not detach the host for a reset for an earlier ByoMachine", func() {
						reportHostReset(k8sClientUncached, byoHost, "uid-of-an-earlier-byomachine")

						_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
						Expect(err).NotTo(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						Expect(k8sClientUncached.Get(ctx, byoHostLookupKey, updatedByoHost)).Should(Succeed())
						Expect(updatedByoHost.Spec.ByoMachineRef).To(Equal(&infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}))
						Expect(updatedByoHost.Spec.ResetRequested).To(BeTrue())

						Expect(k8sClientUncached.Get(ctx, byoMachineLookupKey, &infrastructurev1beta1.ByoMachine{})).Should(Succeed())
					})

					It("should detach the host and delete the ByoMachine after the agent has reset the host", func() {
						ph, err := v1beta1patch.NewHelper(byoHost, k8sClientUncached)
						Expect(err).ShouldNot(HaveOccurred())
						byoHost.Spec.ResetRequested = true
						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{Kind: "Secret", Namespace: defaultNamespace, Name: "installation-secret"}
						Expect(ph.Patch(ctx, byoHost)).Should(Succeed())
						reportHostReset(k8sClientUncached, byoHost, byoMachine.UID)

						_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
						Expect(err).NotTo(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						Expect(k8sClientUncached.Get(ctx, byoHostLookupKey, updatedByoHost)).Should(Succeed())
						Expect(updatedByoHost.Spec).To(Equal(infrastructurev1beta1.ByoHostSpec{}))
						// The report of the agent is kept.
						Expect(updatedByoHost.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))

						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							fmt.Sprintf("Normal ByoHostReleaseSucceeded Released ByoHost %s", byoHost.Name),
							fmt.Sprintf("Normal ByoHostReleaseSucceeded ByoHost Released by %s", byoMachine.Name),
						}))

						err = k8sClientUncached.Get(ctx, byoMachineLookupKey, &infrastructurev1beta1.ByoMachine{})
						Expect(err).To(MatchError(fmt.Sprintf("byomachines.infrastructure.cluster.x-k8s.io %q not found", byoMachineLookupKey.Name)))
					})
				})

				Context("When installer config exists", func() {
					BeforeEach(func() {
						k8sInstallerConfig = builder.K8sInstallerConfig(defaultNamespace, "").
							WithName(byoMachine.Name).
							Build()
						Expect(k8sClientUncached.Create(ctx, k8sInstallerConfig)).Should(Succeed())
						WaitForObjectsToBePopulatedInCache(k8sInstallerConfig)

						ph, err := v1beta1patch.NewHelper(byoMachine, k8sClientUncached)
						Expect(err).ShouldNot(HaveOccurred())
						byoMachine.Spec.InstallerRef = &corev1.ObjectReference{
							Kind:       "K8sInstallerConfigTemplate",
							Namespace:  defaultNamespace,
							Name:       defaultK8sInstallerConfigTemplateName,
							APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
						}
						Expect(ph.Patch(ctx, byoMachine, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())

						WaitForObjectToBeUpdatedInCache(byoMachine, func(object client.Object) bool {
							return object.(*infrastructurev1beta1.ByoMachine).Spec.InstallerRef != nil
						})
					})

					It("should requeue the request if installer config is not ready", func() {
						res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
						Expect(err).NotTo(HaveOccurred())
						Expect(res.RequeueAfter).To(Equal(reconciler.RequeuePeriod))
					})

					It("should patch byohost if installer config is ready", func() {
						ph, err := v1beta1patch.NewHelper(k8sInstallerConfig, k8sClientUncached)
						Expect(err).ShouldNot(HaveOccurred())
						k8sInstallerConfig.Status = infrastructurev1beta1.K8sInstallerConfigStatus{
							Ready: true,
							InstallationSecret: &corev1.ObjectReference{
								Kind:       "Secret",
								Namespace:  defaultNamespace,
								Name:       "K8sInstallationSecret",
								APIVersion: "v1",
							},
						}
						Expect(ph.Patch(ctx, k8sInstallerConfig, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())

						WaitForObjectToBeUpdatedInCache(k8sInstallerConfig, func(object client.Object) bool {
							return object.(*infrastructurev1beta1.K8sInstallerConfig).Status.Ready == true
						})

						_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
						Expect(err).NotTo(HaveOccurred())

						patchedHost := &infrastructurev1beta1.ByoHost{}
						err = k8sClientUncached.Get(ctx, byoHostLookupKey, patchedHost)
						Expect(err).ShouldNot(HaveOccurred())

						Expect(k8sInstallerConfig.Status.InstallationSecret).To(Equal(patchedHost.Spec.InstallationSecret))
					})

					AfterEach(func() {
						Expect(k8sClientUncached.Delete(ctx, k8sInstallerConfig)).Should(Succeed())
					})
				})

			})

			It("should mark BYOHostReady as False when byomachine is paused", func() {
				ph, err := v1beta1patch.NewHelper(byoMachine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())

				pauseAnnotations := map[string]string{
					clusterv1.PausedAnnotation: "paused",
				}
				annotations.AddAnnotations(byoMachine, pauseAnnotations)

				Expect(ph.Patch(ctx, byoMachine, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())

				WaitForObjectToBeUpdatedInCache(byoMachine, func(object client.Object) bool {
					return annotations.HasPaused(object.(*infrastructurev1beta1.ByoMachine))
				})

				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				actualCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.ClusterOrResourcePausedReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))
			})

			It("should mark BYOHostReady as False when cluster is paused", func() {
				pausedCluster := builder.Cluster(defaultNamespace, "paused-cluster").
					WithPausedField(true).
					WithInfrastructureRef(byoCluster).
					Build()
				Expect(k8sClientUncached.Create(ctx, pausedCluster)).Should(Succeed())

				pausedMachine := builder.Machine(defaultNamespace, "paused-machine").
					WithClusterName(pausedCluster.Name).
					Build()
				Expect(k8sClientUncached.Create(ctx, pausedMachine)).Should(Succeed())

				pausedByoMachine := builder.ByoMachine(defaultNamespace, "paused-byo-machine").
					WithClusterLabel(pausedCluster.Name).
					WithOwnerMachine(pausedMachine).
					Build()
				Expect(k8sClientUncached.Create(ctx, pausedByoMachine)).Should(Succeed())

				WaitForObjectsToBePopulatedInCache(pausedCluster, pausedMachine, pausedByoMachine)

				pausedByoMachineLookupKey := types.NamespacedName{Name: pausedByoMachine.Name, Namespace: pausedByoMachine.Namespace}

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: pausedByoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, pausedByoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				actualCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.ClusterOrResourcePausedReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))

				Expect(k8sClientUncached.Delete(ctx, pausedCluster)).Should(Succeed())
				Expect(k8sClientUncached.Delete(ctx, pausedMachine)).Should(Succeed())
				Expect(k8sClientUncached.Delete(ctx, pausedByoMachine)).Should(Succeed())
			})

			It("should mark BYOHostReady as False when machine.Spec.Bootstrap.DataSecretName is not set", func() {
				ph, err := patch.NewHelper(machine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())

				machine.Spec.Bootstrap.DataSecretName = nil
				Expect(ph.Patch(ctx, machine, patch.WithStatusObservedGeneration{})).Should(Succeed())

				WaitForObjectToBeUpdatedInCache(machine, func(object client.Object) bool {
					return object.(*clusterv1.Machine).Spec.Bootstrap.DataSecretName == nil
				})

				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).To(BeNil())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ShouldNot(HaveOccurred())

				actualCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.WaitingForBootstrapDataSecretReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))
			})

			It("should mark BYOHostReady condition as False when the InstallationSecret is not available", func() {
				// making the node unavailable by deleting it so that the reason persists
				Expect(clientFake.Delete(ctx, node)).Should(Succeed())

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).Should(MatchError(fmt.Sprintf("nodes %q not found", byoHost.Name)))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ShouldNot(HaveOccurred())

				actualCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.InstallationSecretNotAvailableReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))
			})
		})

		Context("When no matching BYO Hosts are available", func() {
			BeforeEach(func() {
				byoHost = builder.ByoHost(defaultNamespace, "byohost-with-different-label").
					WithLabels(map[string]string{"CPUs": "2"}).
					Build()
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())

				byoMachine = builder.ByoMachine(defaultNamespace, "byomachine-with-label-selector").
					WithClusterLabel(defaultClusterName).
					WithOwnerMachine(machine).
					WithLabelSelector(map[string]string{"CPUs": "4"}).
					Build()
				Expect(k8sClientUncached.Create(ctx, byoMachine)).Should(Succeed())

				WaitForObjectsToBePopulatedInCache(byoHost, byoMachine)
				byoMachineLookupKey = types.NamespacedName{Name: byoMachine.Name, Namespace: byoMachine.Namespace}
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost)).ToNot(HaveOccurred())
			})

			It("should mark BYOHostReady as False when BYOHosts is available but label mismatch", func() {
				res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(reconciler.RequeuePeriod))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				actualCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BYOHostsUnavailableReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					"Warning ByoHostSelectionFailed No available ByoHost",
				}))
			})
		})

		Context("When all ByoHost are attached", func() {
			BeforeEach(func() {
				byoHost = builder.ByoHost(defaultNamespace, "byohost-attached-different-cluster").Build()
				byoHost.Spec.ByoMachineRef = &infrastructurev1beta1.ByoMachineReference{Name: "another-byomachine", UID: "uid-of-another-byomachine"}
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())

				WaitForObjectsToBePopulatedInCache(byoHost)
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost)).ToNot(HaveOccurred())
			})

			It("should mark BYOHostReady as False", func() {
				res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(reconciler.RequeuePeriod))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				actualCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*actualCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BYOHostsUnavailableReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					"Warning ByoHostSelectionFailed No available ByoHost",
				}))
			})
		})

		Context("When a free ByoHost is in another namespace", func() {
			var (
				anotherNamespace       *corev1.Namespace
				hostInAnotherNamespace *infrastructurev1beta1.ByoHost
			)

			BeforeEach(func() {
				anotherNamespace = builder.Namespace("byoh-another-namespace-").Build()
				Expect(k8sClientUncached.Create(ctx, anotherNamespace)).Should(Succeed())

				hostInAnotherNamespace = builder.ByoHost(anotherNamespace.Name, "host-in-another-namespace").Build()
				Expect(k8sClientUncached.Create(ctx, hostInAnotherNamespace)).Should(Succeed())
				DeferCleanup(func() {
					Expect(k8sClientUncached.Delete(ctx, hostInAnotherNamespace)).Should(Succeed())
				})
				WaitForObjectsToBePopulatedInCache(hostInAnotherNamespace)
				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, hostInAnotherNamespace.Name).Build())).Should(Succeed())
			})

			It("should not attach the ByoHost, and should attach one in the namespace of the ByoMachine", func() {
				res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(reconciler.RequeuePeriod))

				untouchedHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClientUncached.Get(ctx, client.ObjectKeyFromObject(hostInAnotherNamespace), untouchedHost)).Should(Succeed())
				Expect(untouchedHost.Spec.ByoMachineRef).To(BeNil())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				Expect(k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)).Should(Succeed())
				Expect(*v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.BYOHostReady,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BYOHostsUnavailableReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))

				hostInSameNamespace := builder.ByoHost(defaultNamespace, "host-in-same-namespace").Build()
				Expect(k8sClientUncached.Create(ctx, hostInSameNamespace)).Should(Succeed())
				DeferCleanup(func() {
					Expect(k8sClientUncached.Delete(ctx, hostInSameNamespace)).Should(Succeed())
				})
				WaitForObjectsToBePopulatedInCache(hostInSameNamespace)
				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, hostInSameNamespace.Name).Build())).Should(Succeed())

				res, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(BeZero())

				attachedHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClientUncached.Get(ctx, client.ObjectKeyFromObject(hostInSameNamespace), attachedHost)).Should(Succeed())
				Expect(attachedHost.Spec.ByoMachineRef).To(Equal(&infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}))

				Expect(k8sClientUncached.Get(ctx, client.ObjectKeyFromObject(hostInAnotherNamespace), untouchedHost)).Should(Succeed())
				Expect(untouchedHost.Spec.ByoMachineRef).To(BeNil())
			})

			It("should not take a ByoHost in another namespace as the attached one", func() {
				// The ByoHost refers to the ByoMachine, but is in another namespace.
				referringHost := builder.ByoHost(anotherNamespace.Name, "referring-host-in-another-namespace").Build()
				referringHost.Spec.ByoMachineRef = &infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}
				Expect(k8sClientUncached.Create(ctx, referringHost)).Should(Succeed())
				DeferCleanup(func() {
					Expect(k8sClientUncached.Delete(ctx, referringHost)).Should(Succeed())
				})
				WaitForObjectsToBePopulatedInCache(referringHost)

				attachedHost, err := reconciler.FetchAttachedByoHost(ctx, byoMachine)
				Expect(err).NotTo(HaveOccurred())
				Expect(attachedHost).To(BeNil())
			})
		})

		Context("When a ByoHost is attached to an earlier ByoMachine of the same name", func() {
			BeforeEach(func() {
				// An administrator has removed the finalizer of the earlier
				// ByoMachine, which has left the ByoHost attached to it.
				byoHost = builder.ByoHost(defaultNamespace, "host-of-an-earlier-byomachine").Build()
				byoHost.Spec.ByoMachineRef = &infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: "uid-of-an-earlier-byomachine"}
				byoHost.Spec.ResetRequested = true
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())
				DeferCleanup(func() {
					Expect(k8sClientUncached.Delete(ctx, byoHost)).Should(Succeed())
				})
				WaitForObjectsToBePopulatedInCache(byoHost)
				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, byoHost.Name).Build())).Should(Succeed())
			})

			It("should neither take the ByoHost as the attached one nor attach it", func() {
				attachedHost, err := reconciler.FetchAttachedByoHost(ctx, byoMachine)
				Expect(err).NotTo(HaveOccurred())
				Expect(attachedHost).To(BeNil())

				res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(reconciler.RequeuePeriod))

				untouchedHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClientUncached.Get(ctx, client.ObjectKeyFromObject(byoHost), untouchedHost)).Should(Succeed())
				Expect(untouchedHost.Spec.ByoMachineRef.UID).To(Equal(types.UID("uid-of-an-earlier-byomachine")))
				Expect(untouchedHost.Spec.ResetRequested).To(BeTrue())
			})
		})

		Context("When a free ByoHost has the fields of an earlier attachment", func() {
			BeforeEach(func() {
				// An administrator has detached the host by hand, and has
				// left the fields other than spec.byoMachineRef.
				byoHost = builder.ByoHost(defaultNamespace, "host-detached-by-hand").Build()
				byoHost.Spec.ResetRequested = true
				byoHost.Spec.InstallationSecret = &corev1.ObjectReference{Kind: "Secret", Namespace: defaultNamespace, Name: "installation-secret-of-an-earlier-byomachine"}
				Expect(k8sClientUncached.Create(ctx, byoHost)).Should(Succeed())
				DeferCleanup(func() {
					Expect(k8sClientUncached.Delete(ctx, byoHost)).Should(Succeed())
				})
				WaitForObjectsToBePopulatedInCache(byoHost)
				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, byoHost.Name).Build())).Should(Succeed())
			})

			It("should clear them when it attaches the ByoHost", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())

				attachedHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClientUncached.Get(ctx, client.ObjectKeyFromObject(byoHost), attachedHost)).Should(Succeed())
				Expect(attachedHost.Spec.ByoMachineRef).To(Equal(&infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}))
				Expect(attachedHost.Spec.ResetRequested).To(BeFalse())
				Expect(attachedHost.Spec.InstallationSecret).To(BeNil())
			})
		})

		Context("When another ByoMachine claims the ByoHost at the same time", func() {
			anotherByoMachineRef := &infrastructurev1beta1.ByoMachineReference{Name: "another-byomachine", UID: "uid-of-another-byomachine"}
			// Only the hosts of this test match the selector of the ByoMachine.
			raceTestLabels := map[string]string{"race-test": "true"}
			var (
				claimedHost      *infrastructurev1beta1.ByoHost
				racingReconciler *controllers.ByoMachineReconciler
			)

			BeforeEach(func() {
				byoMachine.Spec.Selector = &metav1.LabelSelector{MatchLabels: raceTestLabels}
				Expect(k8sClientUncached.Update(ctx, byoMachine)).Should(Succeed())
				WaitForObjectToBeUpdatedInCache(byoMachine, func(object client.Object) bool {
					return object.(*infrastructurev1beta1.ByoMachine).Spec.Selector != nil
				})

				claimedHost = builder.ByoHost(defaultNamespace, "host-claimed-by-another").WithLabels(raceTestLabels).Build()
				Expect(k8sClientUncached.Create(ctx, claimedHost)).Should(Succeed())
				DeferCleanup(func() {
					Expect(k8sClientUncached.Delete(ctx, claimedHost)).Should(Succeed())
				})
				WaitForObjectsToBePopulatedInCache(claimedHost)

				watchClient, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme.Scheme})
				Expect(err).NotTo(HaveOccurred())
				claimed := false
				racingClient := interceptor.NewClient(watchClient, interceptor.Funcs{
					// The controller lists the ByoHosts with the index of the cache.
					List: func(ctx context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						return reconciler.Client.List(ctx, list, opts...)
					},
					// Another ByoMachine claims the host after this ByoMachine
					// read it, and before this ByoMachine patches it.
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
						if host, ok := obj.(*infrastructurev1beta1.ByoHost); ok && !claimed {
							claimed = true
							another := &infrastructurev1beta1.ByoHost{}
							Expect(c.Get(ctx, client.ObjectKeyFromObject(host), another)).Should(Succeed())
							another.Spec.ByoMachineRef = anotherByoMachineRef
							Expect(c.Update(ctx, another)).Should(Succeed())
						}
						return c.Patch(ctx, obj, p, opts...)
					},
				})
				racingReconciler = &controllers.ByoMachineReconciler{
					Client:        racingClient,
					ClusterCache:  reconciler.ClusterCache,
					Recorder:      recorder,
					RequeuePeriod: reconciler.RequeuePeriod,
				}
			})

			It("should not take the ByoHost, and should attach another one", func() {
				res, err := racingReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())
				// Picks a host again soon, before the requeue period to wait for a free one.
				Expect(res.RequeueAfter).To(BeNumerically(">", 0))
				Expect(res.RequeueAfter).To(BeNumerically("<", reconciler.RequeuePeriod))

				updatedClaimedHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClientUncached.Get(ctx, client.ObjectKeyFromObject(claimedHost), updatedClaimedHost)).Should(Succeed())
				Expect(updatedClaimedHost.Spec.ByoMachineRef).To(Equal(anotherByoMachineRef))
				Expect(updatedClaimedHost.Spec.BootstrapSecret).To(BeNil())

				freeHost := builder.ByoHost(defaultNamespace, "host-free-after-conflict").WithLabels(raceTestLabels).Build()
				Expect(k8sClientUncached.Create(ctx, freeHost)).Should(Succeed())
				DeferCleanup(func() {
					Expect(k8sClientUncached.Delete(ctx, freeHost)).Should(Succeed())
				})
				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, freeHost.Name).Build())).Should(Succeed())
				WaitForObjectsToBePopulatedInCache(freeHost)
				WaitForObjectToBeUpdatedInCache(claimedHost, func(object client.Object) bool {
					return object.(*infrastructurev1beta1.ByoHost).Spec.ByoMachineRef != nil
				})

				_, err = racingReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).NotTo(HaveOccurred())

				attachedHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClientUncached.Get(ctx, client.ObjectKeyFromObject(freeHost), attachedHost)).Should(Succeed())
				Expect(attachedHost.Spec.ByoMachineRef).To(Equal(&infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}))

				Expect(k8sClientUncached.Get(ctx, client.ObjectKeyFromObject(claimedHost), updatedClaimedHost)).Should(Succeed())
				Expect(updatedClaimedHost.Spec.ByoMachineRef).To(Equal(anotherByoMachineRef))
			})
		})

		Context("When multiple BYO Host are available", func() {
			var (
				byoHost1 *infrastructurev1beta1.ByoHost
				byoHost2 *infrastructurev1beta1.ByoHost
			)

			BeforeEach(func() {
				byoHost1 = builder.ByoHost(defaultNamespace, defaultByoHostName).Build()
				Expect(k8sClientUncached.Create(ctx, byoHost1)).Should(Succeed())
				byoHost2 = builder.ByoHost(defaultNamespace, defaultByoHostName).Build()
				Expect(k8sClientUncached.Create(ctx, byoHost2)).Should(Succeed())

				WaitForObjectsToBePopulatedInCache(byoHost1, byoHost2)

				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, byoHost1.Name).Build())).Should(Succeed())
				Expect(clientFake.Create(ctx, builder.Node(defaultNamespace, byoHost2.Name).Build())).Should(Succeed())
			})

			It("claims one of the available host", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())

				Expect(createdByoMachine.Status.Ready).To(BeTrue())

				readyCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*readyCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:   infrastructurev1beta1.BYOHostReady,
					Status: corev1.ConditionTrue,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(len(events)).Should(Equal(3))

				node1 := corev1.Node{}
				err = clientFake.Get(ctx, types.NamespacedName{Name: byoHost1.Name, Namespace: defaultNamespace}, &node1)
				Expect(err).NotTo(HaveOccurred())

				node2 := corev1.Node{}
				err = clientFake.Get(ctx, types.NamespacedName{Name: byoHost2.Name, Namespace: defaultNamespace}, &node2)
				Expect(err).NotTo(HaveOccurred())

				var nodeTagged bool
				if strings.Contains(node1.Spec.ProviderID, controllers.ProviderIDPrefix) || strings.Contains(node2.Spec.ProviderID, controllers.ProviderIDPrefix) {
					nodeTagged = true
				}
				Expect(nodeTagged).To(Equal(true))
			})

			It("does not claims the attached host", func() {
				anotherByoMachine := &infrastructurev1beta1.ByoMachine{
					ObjectMeta: metav1.ObjectMeta{Name: "another-byomachine", Namespace: defaultNamespace, UID: "uid-of-another-byomachine"},
				}
				attachByoHostToByoMachine(k8sClientUncached, byoHost2, anotherByoMachine)

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ToNot(HaveOccurred())

				createdByoHost := &infrastructurev1beta1.ByoHost{}
				err = k8sClientUncached.Get(ctx, types.NamespacedName{Name: byoHost1.Name, Namespace: defaultNamespace}, createdByoHost)
				Expect(err).ToNot(HaveOccurred())
				Expect(createdByoHost.Spec.ByoMachineRef).To(Equal(&infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}))

				createdByoMachine := &infrastructurev1beta1.ByoMachine{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
				Expect(err).ToNot(HaveOccurred())
				Expect(createdByoMachine.Status.Ready).To(BeTrue())

				readyCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
				Expect(*readyCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:   infrastructurev1beta1.BYOHostReady,
					Status: corev1.ConditionTrue,
				}))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(len(events)).Should(Equal(3))

				node := corev1.Node{}
				err = clientFake.Get(ctx, types.NamespacedName{Name: byoHost1.Name, Namespace: defaultNamespace}, &node)
				Expect(err).NotTo(HaveOccurred())

				Expect(node.Spec.ProviderID).To(ContainSubstring(controllers.ProviderIDPrefix))
			})

			AfterEach(func() {
				Expect(k8sClientUncached.Delete(ctx, byoHost1)).Should(Succeed())
				Expect(k8sClientUncached.Delete(ctx, byoHost2)).Should(Succeed())
			})
		})

		Context("When installer config template exists", func() {
			It("should create installer config from the template", func() {
				ph, err := v1beta1patch.NewHelper(byoMachine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())
				byoMachine.Spec.InstallerRef = &corev1.ObjectReference{
					Kind:       "K8sInstallerConfigTemplate",
					Namespace:  k8sInstallerConfigTemplate.Namespace,
					Name:       k8sInstallerConfigTemplate.Name,
					UID:        k8sInstallerConfigTemplate.UID,
					APIVersion: infrastructurev1beta1.GroupVersion.String(),
				}
				Expect(ph.Patch(ctx, byoMachine, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())

				WaitForObjectToBeUpdatedInCache(byoMachine, func(object client.Object) bool {
					return object.(*infrastructurev1beta1.ByoMachine).Spec.InstallerRef != nil
				})

				res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).ShouldNot(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(reconciler.RequeuePeriod))

				createdK8sInstallerConfig := &infrastructurev1beta1.K8sInstallerConfig{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdK8sInstallerConfig)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(k8sInstallerConfigTemplate.Spec.Template.Spec).To(Equal(createdK8sInstallerConfig.Spec))
				Expect(createdK8sInstallerConfig.GetAnnotations()[infrastructurev1beta1.K8sVersionAnnotation]).To(Equal(machine.Spec.Version))
			})
		})

		Context("When installer config template resource does not exists", func() {
			BeforeEach(func() {
				// delete k8sinstallerconfigtemplate resource
				Expect(k8sClientUncached.Delete(ctx, k8sInstallerConfigTemplate)).Should(Succeed())

				ph, err := v1beta1patch.NewHelper(byoMachine, k8sClientUncached)
				Expect(err).ShouldNot(HaveOccurred())
				byoMachine.Spec.InstallerRef = &corev1.ObjectReference{
					Kind:       "K8sInstallerConfigTemplate",
					Namespace:  defaultNamespace,
					Name:       defaultK8sInstallerConfigTemplateName,
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				}
				Expect(ph.Patch(ctx, byoMachine, v1beta1patch.WithStatusObservedGeneration{})).Should(Succeed())
				WaitForObjectToBeUpdatedInCache(byoMachine, func(object client.Object) bool {
					return object.(*infrastructurev1beta1.ByoMachine).Spec.InstallerRef != nil
				})
			})

			It("should fail create installer config from the template", func() {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
				Expect(err).Should(MatchError(fmt.Sprintf("k8sinstallerconfigtemplates.infrastructure.cluster.x-k8s.io %q not found", defaultK8sInstallerConfigTemplateName)))

				createdK8sInstallerConfig := &infrastructurev1beta1.K8sInstallerConfig{}
				err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdK8sInstallerConfig)
				Expect(err).Should(MatchError(fmt.Sprintf("k8sinstallerconfigs.infrastructure.cluster.x-k8s.io %q not found", byoMachineLookupKey.Name)))
			})
		})
	})

	Context("When cluster infrastructure is not ready", func() {
		BeforeEach(func() {
			ph, err := patch.NewHelper(capiCluster, k8sClientUncached)
			Expect(err).ShouldNot(HaveOccurred())
			capiCluster.Status.Initialization.InfrastructureProvisioned = ptr.To(false)
			err = ph.Patch(ctx, capiCluster, patch.WithStatusObservedGeneration{})
			Expect(err).ShouldNot(HaveOccurred())

			WaitForObjectToBeUpdatedInCache(capiCluster, func(object client.Object) bool {
				return !ptr.Deref(object.(*clusterv1.Cluster).Status.Initialization.InfrastructureProvisioned, false)
			})
		})

		It("should mark BYOHostReady as False", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: byoMachineLookupKey})
			Expect(err).To(BeNil())

			createdByoMachine := &infrastructurev1beta1.ByoMachine{}
			err = k8sClientUncached.Get(ctx, byoMachineLookupKey, createdByoMachine)
			Expect(err).ShouldNot(HaveOccurred())

			actualCondition := v1beta1conditions.Get(createdByoMachine, infrastructurev1beta1.BYOHostReady)
			Expect(*actualCondition).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
				Type:     infrastructurev1beta1.BYOHostReady,
				Status:   corev1.ConditionFalse,
				Reason:   infrastructurev1beta1.WaitingForClusterInfrastructureReason,
				Severity: clusterv1beta1.ConditionSeverityInfo,
			}))

			// assert events
			events := eventutils.CollectEvents(recorder.Events)
			Expect(len(events)).Should(Equal(0))
		})
	})
})

// attachByoHostToByoMachine attaches the ByoHost to the ByoMachine, as the
// controller does, and waits until the cache of the controller has it.
func attachByoHostToByoMachine(c client.Client, host *infrastructurev1beta1.ByoHost, byoMachine *infrastructurev1beta1.ByoMachine) {
	ph, err := v1beta1patch.NewHelper(host, c)
	Expect(err).ShouldNot(HaveOccurred())
	host.Spec.ByoMachineRef = &infrastructurev1beta1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}
	Expect(ph.Patch(ctx, host)).Should(Succeed())
	WaitForObjectToBeUpdatedInCache(host, func(object client.Object) bool {
		ref := object.(*infrastructurev1beta1.ByoHost).Spec.ByoMachineRef
		return ref != nil && ref.UID == byoMachine.UID
	})
}

// reportHostReset reports in the status of the ByoHost that the agent has reset
// the host for the ByoMachine of the UID, as the agent does, and waits until
// the cache of the controller has it.
// requestInPlaceUpgrade writes an in-place upgrade into the spec of the
// attached ByoHost, as the in-place extension does, and waits for the cache.
func requestInPlaceUpgrade(c client.Client, key types.NamespacedName, id string, attempt int32) {
	host := &infrastructurev1beta1.ByoHost{}
	Expect(c.Get(ctx, key, host)).Should(Succeed())
	Expect(host.Spec.ByoMachineRef).NotTo(BeNil())
	host.Spec.InPlaceUpgrade = &infrastructurev1beta1.InPlaceUpgradeRequest{
		ID: id, Attempt: attempt,
		ClusterName: "cluster", ClusterUID: "uid-of-cluster", MachineName: "machine", MachineUID: "uid-of-machine",
		ByoMachineUID: host.Spec.ByoMachineRef.UID, NodeName: host.Name, Role: infrastructurev1beta1.InPlaceUpgradeWorker,
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
	Expect(c.Update(ctx, host)).Should(Succeed())
	WaitForObjectToBeUpdatedInCache(host, func(object client.Object) bool {
		req := object.(*infrastructurev1beta1.ByoHost).Spec.InPlaceUpgrade
		return req != nil && req.ID == id && req.Attempt == attempt
	})
}

// reportInPlaceUpgrade writes the status of an in-place upgrade, as the agent
// does, waits for the cache and returns the ByoHost as it is then.
func reportInPlaceUpgrade(c client.Client, key types.NamespacedName, id string, attempt int32, phase infrastructurev1beta1.InPlaceUpgradePhase) *infrastructurev1beta1.ByoHost {
	host := &infrastructurev1beta1.ByoHost{}
	Expect(c.Get(ctx, key, host)).Should(Succeed())
	host.Status.InPlaceUpgrade = &infrastructurev1beta1.InPlaceUpgradeStatus{ID: id, Attempt: attempt, Phase: phase}
	switch phase {
	case infrastructurev1beta1.InPlaceUpgradeSucceeded:
		host.Status.InPlaceUpgrade.ObservedKubeletVersion, host.Status.InPlaceUpgrade.ObservedNodeVersion = "v1.37.2", "v1.37.2"
	case infrastructurev1beta1.InPlaceUpgradeFailed:
		host.Status.InPlaceUpgrade.Message = "step failed"
	case infrastructurev1beta1.InPlaceUpgradeRunning:
	}
	Expect(c.Status().Update(ctx, host)).Should(Succeed())
	WaitForObjectToBeUpdatedInCache(host, func(object client.Object) bool {
		status := object.(*infrastructurev1beta1.ByoHost).Status.InPlaceUpgrade
		return status != nil && status.ID == id && status.Attempt == attempt && status.Phase == phase
	})
	Expect(c.Get(ctx, key, host)).Should(Succeed())
	return host
}

func reportHostReset(c client.Client, host *infrastructurev1beta1.ByoHost, uid types.UID) {
	ph, err := v1beta1patch.NewHelper(host, c)
	Expect(err).ShouldNot(HaveOccurred())
	host.Status.LastReset = &infrastructurev1beta1.LastReset{ByoMachineUID: uid, Time: metav1.Now()}
	Expect(ph.Patch(ctx, host)).Should(Succeed())
	WaitForObjectToBeUpdatedInCache(host, func(object client.Object) bool {
		lastReset := object.(*infrastructurev1beta1.ByoHost).Status.LastReset
		return lastReset != nil && lastReset.ByoMachineUID == uid
	})
}
