// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openkubes/ok-baremetal-byoh/agent/cloudinit/cloudinitfakes"
	"github.com/openkubes/ok-baremetal-byoh/agent/reconciler"
	infrastructurev1beta1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	"github.com/openkubes/ok-baremetal-byoh/test/builder"
	eventutils "github.com/openkubes/ok-baremetal-byoh/test/utils/events"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	v1beta1conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions"
	v1beta1patch "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"

	controllerruntime "sigs.k8s.io/controller-runtime"
)

var _ = Describe("Byohost Agent Tests", func() {

	var (
		ctx                 = context.TODO()
		ns                  = "default"
		hostName            = "test-host"
		byoHost             *infrastructurev1beta1.ByoHost
		byoMachine          *infrastructurev1beta1.ByoMachine
		byoHostLookupKey    types.NamespacedName
		bootstrapSecret     *corev1.Secret
		installationSecret  *corev1.Secret
		recorder            *events.FakeRecorder
		uninstallScript     string
		uninstallScriptPath string
		upgradeUnits        *fakeUpgradeUnits
	)

	BeforeEach(func() {
		fakeCommandRunner = &cloudinitfakes.FakeICmdRunner{}
		fakeFileWriter = &cloudinitfakes.FakeIFileWriter{}
		fakeTemplateParser = &cloudinitfakes.FakeITemplateParser{}
		recorder = events.NewFakeRecorder(32)
		uninstallScriptPath = filepath.Join(GinkgoT().TempDir(), "uninstall.sh")
		// No command of an in-place upgrade runs on the host, unless a test says so.
		upgradeUnits = &fakeUpgradeUnits{}
		hostReconciler = &reconciler.HostReconciler{
			Client:              k8sClient,
			CmdRunner:           fakeCommandRunner,
			FileWriter:          fakeFileWriter,
			TemplateParser:      fakeTemplateParser,
			Recorder:            recorder,
			SkipK8sInstallation: false,
			PreResetCleanup:     func(context.Context) error { return nil },
			UninstallScriptPath: uninstallScriptPath,
			UpgradeUnits:        upgradeUnits,
		}
	})

	It("should do nothing if ByoHost is not found", func() {
		// An administrator has deleted the ByoHost.
		_, err := hostReconciler.Reconcile(ctx, controllerruntime.Request{
			NamespacedName: types.NamespacedName{
				Name:      "non-existent-host",
				Namespace: ns},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	Context("When ByoHost exists", func() {
		BeforeEach(func() {
			byoHost = builder.ByoHost(ns, hostName).Build()
			Expect(k8sClient.Create(ctx, byoHost)).NotTo(HaveOccurred(), "failed to create byohost")
			var err error
			patchHelper, err = v1beta1patch.NewHelper(byoHost, k8sClient)
			Expect(err).ShouldNot(HaveOccurred())

			byoHostLookupKey = types.NamespacedName{Name: byoHost.Name, Namespace: ns}
		})

		It("should set the Reason to WaitingForByoMachineRefReason if the ByoHost is not attached", func() {
			result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
				NamespacedName: byoHostLookupKey,
			})

			Expect(result).To(Equal(controllerruntime.Result{}))
			Expect(reconcilerErr).ToNot(HaveOccurred())

			updatedByoHost := &infrastructurev1beta1.ByoHost{}
			err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
			Expect(err).ToNot(HaveOccurred())
			k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
			Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
				Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
				Status:   corev1.ConditionFalse,
				Reason:   infrastructurev1beta1.WaitingForByoMachineRefReason,
				Severity: clusterv1beta1.ConditionSeverityInfo,
			}))
		})

		It("should mark a ByoHost that is no longer attached as free", func() {
			// The conditions of the earlier attachment.
			v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
			v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
			Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

			_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
				NamespacedName: byoHostLookupKey,
			})
			Expect(reconcilerErr).ToNot(HaveOccurred())
			Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(0))

			updatedByoHost := &infrastructurev1beta1.ByoHost{}
			Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
			Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
				Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
				Status:   corev1.ConditionFalse,
				Reason:   infrastructurev1beta1.WaitingForByoMachineRefReason,
				Severity: clusterv1beta1.ConditionSeverityInfo,
			}))
			Expect(v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)).To(BeNil())
		})

		Context("When the ByoHost is attached to a ByoMachine", func() {
			BeforeEach(func() {
				byoMachine = builder.ByoMachine(ns, "test-byomachine").Build()
				Expect(k8sClient.Create(ctx, byoMachine)).NotTo(HaveOccurred(), "failed to create byomachine")
				byoHost.Spec.ByoMachineRef = &infrastructurev1beta1.ByoMachineReference{
					Name: byoMachine.Name,
					UID:  byoMachine.UID,
				}
				Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
			})

			It("should set the Reason to BootstrapDataSecretUnavailableReason", func() {
				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).ToNot(HaveOccurred())

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
				Expect(err).ToNot(HaveOccurred())

				byoHostRegistrationSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
				Expect(*byoHostRegistrationSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.BootstrapDataSecretUnavailableReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))
			})

			It("should return an error if we fail to load the bootstrap secret", func() {
				byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{
					Kind:      "Secret",
					Namespace: "non-existent",
					Name:      "non-existent",
				}
				Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

				result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(result).To(Equal(controllerruntime.Result{}))
				Expect(reconcilerErr).To(MatchError("secrets \"non-existent\" not found"))

				// assert events
				events := eventutils.CollectEvents(recorder.Events)
				Expect(events).Should(ConsistOf([]string{
					fmt.Sprintf("Warning ReadBootstrapSecretFailed bootstrap secret %s not found", byoHost.Spec.BootstrapSecret.Name),
				}))
			})

			Context("When bootstrap secret is ready", func() {
				BeforeEach(func() {
					secretData := `write_files:
- path: fake/path
  content: blah
runCmd:
- echo 'run some command'`

					bootstrapSecret = builder.Secret(ns, "test-secret").
						WithData(secretData).
						Build()
					Expect(k8sClient.Create(ctx, bootstrapSecret)).NotTo(HaveOccurred())

					byoHost.Spec.BootstrapSecret = &corev1.ObjectReference{
						Kind:      "Secret",
						Namespace: bootstrapSecret.Namespace,
						Name:      bootstrapSecret.Name,
					}

					Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
				})

				It("should skip k8s installation if skip-installation is set", func() {
					hostReconciler.SkipK8sInstallation = true
					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).ToNot(HaveOccurred())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
					Expect(err).ToNot(HaveOccurred())

					k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
					Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
						Status: corev1.ConditionTrue,
					}))

					// assert events
					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).ShouldNot(ContainElement(
						"Normal k8sComponentInstalled Successfully Installed K8s components",
					))
				})

				It("should set the Reason to InstallationSecretUnavailableReason", func() {
					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).ToNot(HaveOccurred())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
					Expect(err).ToNot(HaveOccurred())

					byoHostRegistrationSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
					Expect(*byoHostRegistrationSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sComponentsInstallationSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sInstallationSecretUnavailableReason,
						Severity: clusterv1beta1.ConditionSeverityInfo,
					}))
				})

				It("should return an error if we fail to load the installation secret", func() {
					byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
						Kind:      "Secret",
						Namespace: "non-existent",
						Name:      "non-existent",
					}
					Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).To(MatchError("secrets \"non-existent\" not found"))

					// assert events
					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).Should(ConsistOf([]string{
						fmt.Sprintf("Warning ReadInstallationSecretFailed install and uninstall script %s not found", byoHost.Spec.InstallationSecret.Name),
					}))
				})

				Context("When installation secret is ready", func() {
					var installScript string

					BeforeEach(func() {
						// The scripts contain "{{" to check that they are run as they are.
						installScript = `echo "{{install}}"`
						uninstallScript = `echo "{{uninstall}}"`

						installationSecret = builder.Secret(ns, "test-secret3").
							WithKeyData("install", installScript).
							WithKeyData("uninstall", uninstallScript).
							Build()
						Expect(k8sClient.Create(ctx, installationSecret)).NotTo(HaveOccurred())

						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: installationSecret.Namespace,
							Name:      installationSecret.Name,
						}

						Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
					})

					It("should execute bootstrap secret only once ", func() {

						_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

						_, reconcilerErr = hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2)) // one cmd call is for install script
						Expect(fakeFileWriter.WriteToFileCallCount()).To(Equal(1))
					})

					It("should set K8sNodeBootstrapSucceeded to True if the boostrap execution succeeds", func() {

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2)) // one cmd call is for install script
						Expect(fakeFileWriter.WriteToFileCallCount()).To(Equal(1))

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
							Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status: corev1.ConditionTrue,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Normal InstallScriptExecutionSucceeded install script executed",
							"Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped",
						}))
					})

					It("should set K8sNodeBootstrapSucceeded to false with Reason CloudInitExecutionFailedReason if the bootstrap execution fails", func() {
						v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
						Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						fakeCommandRunner.RunCmdReturns(errors.New("I failed"))

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})

						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).To(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
							Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status:   corev1.ConditionFalse,
							Reason:   infrastructurev1beta1.CloudInitExecutionFailedReason,
							Severity: clusterv1beta1.ConditionSeverityError,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Warning BootstrapK8sNodeFailed k8s Node Bootstrap failed",
							// TODO: improve test to remove this event
							"Warning ResetK8sNodeFailed k8s Node Reset failed",
						}))
					})

					It("should return error if install script execution failed", func() {
						fakeCommandRunner.RunCmdReturns(errors.New("failed to execute install script"))
						invalidInstallationSecret := builder.Secret(ns, "invalid-test-secret").
							WithKeyData("install", "test").
							Build()
						Expect(k8sClient.Create(ctx, invalidInstallationSecret)).NotTo(HaveOccurred())
						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: invalidInstallationSecret.Namespace,
							Name:      invalidInstallationSecret.Name,
						}
						Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).To(HaveOccurred())

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Warning InstallScriptExecutionFailed install script execution failed",
						}))
					})

					It("should return error if installation secrent does not exists", func() {
						fakeCommandRunner.RunCmdReturns(errors.New("failed to execute install script"))
						byoHost.Spec.InstallationSecret = &corev1.ObjectReference{
							Kind:      "Secret",
							Namespace: "non-existent",
							Name:      "non-existent",
						}
						Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())

						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).To(HaveOccurred())

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Warning ReadInstallationSecretFailed install and uninstall script non-existent not found",
						}))

					})

					It("should execute the install script as it is", func() {
						_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).ToNot(HaveOccurred())

						_, installCommand := fakeCommandRunner.RunCmdArgsForCall(0)
						Expect(installCommand).To(Equal(installScript))
					})

					It("should keep the uninstall script in a file only readable by the agent, and leave the spec as it is", func() {
						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).NotTo(HaveOccurred())

						Expect(os.ReadFile(uninstallScriptPath)).To(Equal([]byte(uninstallScript)))
						info, err := os.Stat(uninstallScriptPath)
						Expect(err).NotTo(HaveOccurred())
						Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
						Expect(updatedByoHost.Spec).To(Equal(byoHost.Spec))
					})

					It("should not keep the uninstall script if the install script fails", func() {
						fakeCommandRunner.RunCmdReturns(errors.New("failed to execute install script"))

						_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(reconcilerErr).To(HaveOccurred())
						Expect(uninstallScriptPath).NotTo(BeAnExistingFile())
					})

					It("should set K8sComponentsInstallationSucceeded to true if Install succeeds", func() {
						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						K8sComponentsInstallationSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
						Expect(*K8sComponentsInstallationSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
							Type:   infrastructurev1beta1.K8sComponentsInstallationSucceeded,
							Status: corev1.ConditionTrue,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Normal InstallScriptExecutionSucceeded install script executed",
							"Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped",
						}))
					})

					It("should set K8sNodeBootstrapSucceeded to True if the boostrap execution succeeds", func() {
						result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
							NamespacedName: byoHostLookupKey,
						})
						Expect(result).To(Equal(controllerruntime.Result{}))
						Expect(reconcilerErr).ToNot(HaveOccurred())

						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
						Expect(fakeFileWriter.WriteToFileCallCount()).To(Equal(1))

						updatedByoHost := &infrastructurev1beta1.ByoHost{}
						err := k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)
						Expect(err).ToNot(HaveOccurred())

						k8sNodeBootstrapSucceeded := v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
						Expect(*k8sNodeBootstrapSucceeded).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
							Type:   infrastructurev1beta1.K8sNodeBootstrapSucceeded,
							Status: corev1.ConditionTrue,
						}))

						// assert events
						events := eventutils.CollectEvents(recorder.Events)
						Expect(events).Should(ConsistOf([]string{
							"Normal InstallScriptExecutionSucceeded install script executed",
							"Normal BootstrapK8sNodeSucceeded k8s Node Bootstraped",
						}))
					})
					AfterEach(func() {
						Expect(k8sClient.Delete(ctx, installationSecret)).NotTo(HaveOccurred())
					})
				})

				AfterEach(func() {
					Expect(k8sClient.Delete(ctx, bootstrapSecret)).NotTo(HaveOccurred())
					hostReconciler.SkipK8sInstallation = false
				})
			})

			AfterEach(func() {
				Expect(k8sClient.Delete(ctx, byoMachine)).NotTo(HaveOccurred())
			})
		})

		Context("When a reset of the host is requested", func() {
			BeforeEach(func() {
				// The script contains "{{" to check that it is run as it is.
				uninstallScript = `echo "{{uninstall success script}}"`
				byoMachine = builder.ByoMachine(ns, "test-byomachine").Build()
				Expect(k8sClient.Create(ctx, byoMachine)).NotTo(HaveOccurred(), "failed to create byomachine")
				byoHost.Spec.ByoMachineRef = &infrastructurev1beta1.ByoMachineReference{
					Name: byoMachine.Name,
					UID:  byoMachine.UID,
				}
				byoHost.Spec.ResetRequested = true
				v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)
				v1beta1conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
				Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				Expect(k8sClient.Delete(ctx, byoMachine)).NotTo(HaveOccurred())
			})

			Context("When the agent has installed the k8s components", func() {
				BeforeEach(func() {
					Expect(os.WriteFile(uninstallScriptPath, []byte(uninstallScript), 0o600)).To(Succeed())
				})

				It("should reset the node, uninstall the k8s components, and report the reset", func() {
					result, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(result).To(Equal(controllerruntime.Result{}))
					Expect(reconcilerErr).ToNot(HaveOccurred())

					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
					_, resetCommand := fakeCommandRunner.RunCmdArgsForCall(0)
					Expect(resetCommand).To(Equal(reconciler.KubeadmResetCommand))
					_, uninstallCommand := fakeCommandRunner.RunCmdArgsForCall(1)
					Expect(uninstallCommand).To(Equal(uninstallScript))
					Expect(uninstallScriptPath).NotTo(BeAnExistingFile())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
					// The agent leaves the spec to the controller.
					Expect(updatedByoHost.Spec).To(Equal(byoHost.Spec))
					Expect(updatedByoHost.Status.LastReset).NotTo(BeNil())
					Expect(updatedByoHost.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
					Expect(updatedByoHost.Status.LastReset.Time.IsZero()).To(BeFalse())

					Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sNodeAbsentReason,
						Severity: clusterv1beta1.ConditionSeverityInfo,
					}))
					Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sComponentsInstallationSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sNodeAbsentReason,
						Severity: clusterv1beta1.ConditionSeverityInfo,
					}))

					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).Should(ConsistOf([]string{
						"Normal ResetK8sNodeSucceeded k8s Node Reset completed",
						fmt.Sprintf("Normal ResetHostSucceeded Host reset for ByoMachine %s", byoMachine.Name),
					}))
				})

				It("should not reset the host again once it has reported the reset", func() {
					_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(reconcilerErr).ToNot(HaveOccurred())
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))

					_, reconcilerErr = hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(reconcilerErr).ToNot(HaveOccurred())
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
				})

				// The host is not reset while a command of an in-place upgrade
				// may still run on it, whatever the ByoHost says.
				Context("When a command of an in-place upgrade may still run on the host", func() {
					const busyUnit = "okx-inplace-abc123-0-kubeadm-upgrade.service (active/running)"
					var cleanupCalls int

					reconcileReset := func() error {
						_, err := hostReconciler.Reconcile(ctx, controllerruntime.Request{NamespacedName: byoHostLookupKey})
						return err
					}
					hostNow := func() *infrastructurev1beta1.ByoHost {
						host := &infrastructurev1beta1.ByoHost{}
						Expect(k8sClient.Get(ctx, byoHostLookupKey, host)).To(Succeed())
						return host
					}
					expectNothingDoneOnTheHost := func() {
						Expect(cleanupCalls).To(Equal(0), "the kubelet was stopped or sandboxes were removed")
						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(0), "a command ran on the host")
						Expect(uninstallScriptPath).To(BeAnExistingFile())
						Expect(uninstallScriptPath + ".node-reset").NotTo(BeAnExistingFile())
					}
					expectBlocked := func(reason, message string) {
						host := hostNow()
						Expect(host.Status.LastReset).To(BeNil())
						Expect(host.Spec).To(Equal(byoHost.Spec))
						condition := v1beta1conditions.Get(host, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
						Expect(condition.Status).To(Equal(corev1.ConditionFalse))
						Expect(condition.Reason).To(Equal(reason))
						Expect(condition.Severity).To(Equal(clusterv1beta1.ConditionSeverityWarning))
						Expect(condition.Message).To(ContainSubstring(message))
						// The node is still there: its condition is untouched.
						Expect(v1beta1conditions.IsTrue(host, infrastructurev1beta1.K8sNodeBootstrapSucceeded)).To(BeTrue())
					}

					BeforeEach(func() {
						cleanupCalls = 0
						hostReconciler.PreResetCleanup = func(context.Context) error {
							cleanupCalls++
							return nil
						}
					})

					It("should not touch the host and not report a reset while a unit is active", func() {
						upgradeUnits.busy = []string{busyUnit}

						Expect(reconcileReset()).To(MatchError(ContainSubstring("host reset blocked")))
						expectNothingDoneOnTheHost()
						expectBlocked(infrastructurev1beta1.HostResetBlockedByInPlaceUpgradeReason, busyUnit)
						Expect(eventutils.CollectEvents(recorder.Events)).To(ConsistOf(
							"Warning HostResetBlockedByInPlaceUpgrade a command of an in-place upgrade has not ended: " + busyUnit))
					})

					It("should record the blockade once, not on every retry", func() {
						upgradeUnits.busy = []string{busyUnit}
						Expect(reconcileReset()).To(HaveOccurred())
						Expect(eventutils.CollectEvents(recorder.Events)).To(HaveLen(1))

						Expect(reconcileReset()).To(HaveOccurred())
						Expect(reconcileReset()).To(HaveOccurred())
						Expect(eventutils.CollectEvents(recorder.Events)).To(BeEmpty())
						expectNothingDoneOnTheHost()

						// Another unit is a new fact, and is recorded.
						upgradeUnits.busy = []string{"okx-inplace-abc123-1-kubelet.service (queued systemd job)"}
						Expect(reconcileReset()).To(HaveOccurred())
						Expect(eventutils.CollectEvents(recorder.Events)).To(HaveLen(1))
						expectBlocked(infrastructurev1beta1.HostResetBlockedByInPlaceUpgradeReason, "queued systemd job")
					})

					It("should not touch the host when the state of the host cannot be established", func() {
						upgradeUnits.err = errors.New("systemctl list-units: Failed to connect to bus")

						Expect(reconcileReset()).To(MatchError(ContainSubstring("host reset blocked")))
						expectNothingDoneOnTheHost()
						expectBlocked(infrastructurev1beta1.HostExecutionStateUnknownReason, "Failed to connect to bus")
					})

					It("should not reset the host without a check of the units", func() {
						hostReconciler.UpgradeUnits = nil

						Expect(reconcileReset()).To(MatchError(ContainSubstring("host reset blocked")))
						expectNothingDoneOnTheHost()
						expectBlocked(infrastructurev1beta1.HostExecutionStateUnknownReason, "no check of the units")
					})

					It("should keep the blockade over a restart of the agent", func() {
						upgradeUnits.busy = []string{busyUnit}
						Expect(reconcileReset()).To(HaveOccurred())

						// A new agent process: a new reconciler, the same host.
						restarted := &reconciler.HostReconciler{
							Client: k8sClient, CmdRunner: fakeCommandRunner, FileWriter: fakeFileWriter, TemplateParser: fakeTemplateParser,
							Recorder: recorder, PreResetCleanup: hostReconciler.PreResetCleanup, UninstallScriptPath: uninstallScriptPath,
							UpgradeUnits: upgradeUnits,
						}
						_, err := restarted.Reconcile(ctx, controllerruntime.Request{NamespacedName: byoHostLookupKey})
						Expect(err).To(MatchError(ContainSubstring("host reset blocked")))
						expectNothingDoneOnTheHost()
						expectBlocked(infrastructurev1beta1.HostResetBlockedByInPlaceUpgradeReason, busyUnit)
					})

					It("should reset the host when the unit has ended, and report the reset then", func() {
						upgradeUnits.busy = []string{busyUnit}
						Expect(reconcileReset()).To(HaveOccurred())
						expectNothingDoneOnTheHost()
						eventutils.DrainEvents(recorder.Events)

						upgradeUnits.busy = nil
						Expect(reconcileReset()).To(Succeed())
						Expect(cleanupCalls).To(Equal(1))
						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
						host := hostNow()
						Expect(host.Status.LastReset).NotTo(BeNil())
						Expect(host.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
						Expect(v1beta1conditions.Get(host, infrastructurev1beta1.K8sComponentsInstallationSucceeded).Reason).To(Equal(infrastructurev1beta1.K8sNodeAbsentReason))
						Expect(eventutils.CollectEvents(recorder.Events)).To(ConsistOf(
							"Normal HostResetUnblocked no command of an in-place upgrade runs any more",
							"Normal ResetK8sNodeSucceeded k8s Node Reset completed",
							fmt.Sprintf("Normal ResetHostSucceeded Host reset for ByoMachine %s", byoMachine.Name),
						))
					})

					It("should not run the uninstall script, and not report a reset, when a unit is active after the node reset", func() {
						// Idle before the first change, busy before the uninstall script.
						upgradeUnits.answers = []unitsAnswer{{}, {busy: []string{busyUnit}}}

						Expect(reconcileReset()).To(MatchError(ContainSubstring("host reset blocked")))
						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1))
						_, command := fakeCommandRunner.RunCmdArgsForCall(0)
						Expect(command).To(Equal(reconciler.KubeadmResetCommand))
						Expect(uninstallScriptPath).To(BeAnExistingFile())
						Expect(hostNow().Status.LastReset).To(BeNil())
						Expect(v1beta1conditions.Get(hostNow(), infrastructurev1beta1.K8sComponentsInstallationSucceeded).Reason).To(Equal(infrastructurev1beta1.HostResetBlockedByInPlaceUpgradeReason))

						// The retry does not repeat kubeadm reset, and finishes when the unit has ended.
						upgradeUnits.answers = nil
						Expect(reconcileReset()).To(Succeed())
						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
						_, command = fakeCommandRunner.RunCmdArgsForCall(1)
						Expect(command).To(Equal(uninstallScript))
						Expect(hostNow().Status.LastReset).NotTo(BeNil())
					})

					It("should resume after a restart of the agent that was blocked before the uninstall script, without resetting the node again", func() {
						// kubeadm reset has run; a unit is active before the uninstall script.
						upgradeUnits.answers = []unitsAnswer{{}, {busy: []string{busyUnit}}}
						Expect(reconcileReset()).To(MatchError(ContainSubstring("host reset blocked")))
						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1))
						Expect(cleanupCalls).To(Equal(1))
						Expect(hostNow().Status.LastReset).To(BeNil())
						// The progress is on the host, bound to the attachment.
						checkpoint, err := os.ReadFile(uninstallScriptPath + ".node-reset")
						Expect(err).NotTo(HaveOccurred())
						Expect(string(checkpoint)).To(Equal(string(byoMachine.UID)))

						// A new agent process, with nothing in memory.
						restartedRunner := &cloudinitfakes.FakeICmdRunner{}
						restartedCleanups := 0
						restarted := &reconciler.HostReconciler{
							Client: k8sClient, CmdRunner: restartedRunner, FileWriter: fakeFileWriter, TemplateParser: fakeTemplateParser,
							Recorder: recorder, UninstallScriptPath: uninstallScriptPath, UpgradeUnits: upgradeUnits,
							PreResetCleanup: func(context.Context) error {
								restartedCleanups++
								return nil
							},
						}
						reconcileRestarted := func() error {
							_, err := restarted.Reconcile(ctx, controllerruntime.Request{NamespacedName: byoHostLookupKey})
							return err
						}

						// Still blocked: nothing runs, the reset is not reported.
						upgradeUnits.answers, upgradeUnits.busy = nil, []string{busyUnit}
						Expect(reconcileRestarted()).To(MatchError(ContainSubstring("host reset blocked")))
						Expect(restartedRunner.RunCmdCallCount()).To(Equal(0))
						Expect(restartedCleanups).To(Equal(0))
						Expect(hostNow().Status.LastReset).To(BeNil())
						Expect(uninstallScriptPath).To(BeAnExistingFile())

						// The unit has ended: only the uninstall script runs. The kubelet
						// is not stopped again and kubeadm reset is not repeated.
						upgradeUnits.busy = nil
						Expect(reconcileRestarted()).To(Succeed())
						Expect(restartedCleanups).To(Equal(0))
						Expect(restartedRunner.RunCmdCallCount()).To(Equal(1))
						_, command := restartedRunner.RunCmdArgsForCall(0)
						Expect(command).To(Equal(uninstallScript))
						Expect(uninstallScriptPath).NotTo(BeAnExistingFile())
						host := hostNow()
						Expect(host.Status.LastReset).NotTo(BeNil())
						Expect(host.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
					})

					It("should ask before the first change and again before the uninstall script", func() {
						Expect(reconcileReset()).To(Succeed())
						Expect(upgradeUnits.calls).To(Equal(2))
					})
				})

				It("runs pre-reset cleanup before kubeadm reset and uninstall", func() {
					cleanupCalled := false

					hostReconciler.PreResetCleanup = func(context.Context) error {
						// No reset or uninstall command may have run yet.
						Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(0))
						cleanupCalled = true
						return nil
					}

					_, err := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(err).NotTo(HaveOccurred())
					Expect(cleanupCalled).To(BeTrue())
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))

					_, first := fakeCommandRunner.RunCmdArgsForCall(0)
					_, second := fakeCommandRunner.RunCmdArgsForCall(1)

					Expect(first).To(Equal(reconciler.KubeadmResetCommand))
					Expect(second).To(Equal(uninstallScript))
				})

				It("must not run kubeadm reset if pre-reset cleanup fails", func() {
					hostReconciler.PreResetCleanup = func(context.Context) error {
						return errors.New("CNI DEL failed")
					}

					_, err := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(err).To(MatchError(ContainSubstring("CNI DEL failed")))
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(0))
					Expect(uninstallScriptPath).To(BeAnExistingFile())

					updated := &infrastructurev1beta1.ByoHost{}
					Expect(k8sClient.Get(ctx, byoHostLookupKey, updated)).To(Succeed())
					Expect(updated.Status.LastReset).To(BeNil())
				})

				It("should report the failure of kubeadm reset, and not report the reset", func() {
					fakeCommandRunner.RunCmdReturnsOnCall(0, errors.New("failed to reset"))

					_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(reconcilerErr).To(MatchError("failed to exec kubeadm reset: failed to reset"))
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1))
					Expect(uninstallScriptPath).To(BeAnExistingFile())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
					Expect(updatedByoHost.Status.LastReset).To(BeNil())
					Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sNodeResetFailedReason,
						Severity: clusterv1beta1.ConditionSeverityWarning,
						Message:  "failed to exec kubeadm reset: failed to reset",
					}))

					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).Should(ConsistOf([]string{
						"Warning ResetK8sNodeFailed k8s Node Reset failed",
					}))
				})

				It("should retry uninstall without repeating a successful kubeadm reset", func() {
					fakeCommandRunner.RunCmdReturnsOnCall(1, errors.New("failed to uninstall"))

					request := controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					}

					_, err := hostReconciler.Reconcile(ctx, request)
					Expect(err).To(MatchError("failed to uninstall"))
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))
					Expect(uninstallScriptPath).To(BeAnExistingFile())

					intermediate := &infrastructurev1beta1.ByoHost{}
					Expect(k8sClient.Get(ctx, byoHostLookupKey, intermediate)).To(Succeed())
					Expect(intermediate.Status.LastReset).To(BeNil())

					// The second reconcile resumes at uninstall, not kubeadm reset.
					_, err = hostReconciler.Reconcile(ctx, request)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(3))

					_, command := fakeCommandRunner.RunCmdArgsForCall(2)
					Expect(command).To(Equal(uninstallScript))
					Expect(uninstallScriptPath).NotTo(BeAnExistingFile())

					updated := &infrastructurev1beta1.ByoHost{}
					Expect(k8sClient.Get(ctx, byoHostLookupKey, updated)).To(Succeed())
					Expect(updated.Status.LastReset).NotTo(BeNil())
					Expect(updated.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
				})

				It("should not trust a checkpoint from another ByoMachine", func() {
					checkpointPath := uninstallScriptPath + ".node-reset"
					Expect(os.WriteFile(checkpointPath, []byte("old-machine-uid"), 0o600)).To(Succeed())

					_, err := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(2))

					_, command := fakeCommandRunner.RunCmdArgsForCall(0)
					Expect(command).To(Equal(reconciler.KubeadmResetCommand))
					checkpoint, err := os.ReadFile(checkpointPath)
					Expect(err).NotTo(HaveOccurred())
					Expect(string(checkpoint)).To(Equal(string(byoMachine.UID)))
				})

				It("should keep the UID checkpoint after an uninstall failure", func() {
					fakeCommandRunner.RunCmdReturnsOnCall(1, errors.New("failed to uninstall"))

					_, err := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(err).To(MatchError("failed to uninstall"))

					checkpoint, err := os.ReadFile(uninstallScriptPath + ".node-reset")
					Expect(err).NotTo(HaveOccurred())
					Expect(string(checkpoint)).To(Equal(string(byoMachine.UID)))
				})

				It("should report the failure of the uninstall script, keep the script, and not report the reset", func() {
					fakeCommandRunner.RunCmdReturnsOnCall(1, errors.New("failed to uninstall"))

					_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
						NamespacedName: byoHostLookupKey,
					})
					Expect(reconcilerErr).To(MatchError("failed to uninstall"))
					// The agent runs the script again when it tries again.
					Expect(uninstallScriptPath).To(BeAnExistingFile())

					updatedByoHost := &infrastructurev1beta1.ByoHost{}
					Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
					Expect(updatedByoHost.Status.LastReset).To(BeNil())
					Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
						Type:     infrastructurev1beta1.K8sComponentsInstallationSucceeded,
						Status:   corev1.ConditionFalse,
						Reason:   infrastructurev1beta1.K8sComponentsUninstallationFailedReason,
						Severity: clusterv1beta1.ConditionSeverityWarning,
						Message:  "failed to uninstall",
					}))

					events := eventutils.CollectEvents(recorder.Events)
					Expect(events).Should(ConsistOf([]string{
						"Normal ResetK8sNodeSucceeded k8s Node Reset completed",
						"Warning UninstallScriptExecutionFailed uninstall script execution failed",
					}))
				})
			})

			It("should skip the reset of the node and the uninstallation if the agent has not installed the k8s components", func() {
				_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(reconcilerErr).ToNot(HaveOccurred())
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(0))

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
				Expect(updatedByoHost.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
			})

			It("should reset the node and skip the uninstallation if skip-installation is set", func() {
				hostReconciler.SkipK8sInstallation = true

				_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(reconcilerErr).ToNot(HaveOccurred())
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1))
				_, resetCommand := fakeCommandRunner.RunCmdArgsForCall(0)
				Expect(resetCommand).To(Equal(reconciler.KubeadmResetCommand))

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
				Expect(updatedByoHost.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
				Expect(*v1beta1conditions.Get(updatedByoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)).To(v1beta1conditions.MatchCondition(clusterv1beta1.Condition{
					Type:     infrastructurev1beta1.K8sNodeBootstrapSucceeded,
					Status:   corev1.ConditionFalse,
					Reason:   infrastructurev1beta1.K8sNodeAbsentReason,
					Severity: clusterv1beta1.ConditionSeverityInfo,
				}))
			})

			It("should reset the host for the current attachment even if it has reported a reset for an earlier one", func() {
				byoHost.Status.LastReset = &infrastructurev1beta1.LastReset{
					ByoMachineUID: "uid-of-an-earlier-byomachine",
					Time:          metav1.Now(),
				}
				Expect(patchHelper.Patch(ctx, byoHost, v1beta1patch.WithStatusObservedGeneration{})).NotTo(HaveOccurred())
				hostReconciler.SkipK8sInstallation = true

				_, reconcilerErr := hostReconciler.Reconcile(ctx, controllerruntime.Request{
					NamespacedName: byoHostLookupKey,
				})
				Expect(reconcilerErr).ToNot(HaveOccurred())
				Expect(fakeCommandRunner.RunCmdCallCount()).To(Equal(1))

				updatedByoHost := &infrastructurev1beta1.ByoHost{}
				Expect(k8sClient.Get(ctx, byoHostLookupKey, updatedByoHost)).To(Succeed())
				Expect(updatedByoHost.Status.LastReset.ByoMachineUID).To(Equal(byoMachine.UID))
			})
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, byoHost)).NotTo(HaveOccurred())
			hostReconciler.SkipK8sInstallation = false
		})
	})
})

// unitsAnswer is one answer of the check of the units of in-place upgrades.
type unitsAnswer struct {
	busy []string
	err  error
}

// fakeUpgradeUnits answers the check of the units of in-place upgrades: with
// answers in turn while there are some, with busy and err otherwise.
type fakeUpgradeUnits struct {
	busy    []string
	err     error
	answers []unitsAnswer
	calls   int
}

func (f *fakeUpgradeUnits) Busy(context.Context) ([]string, error) {
	f.calls++
	if len(f.answers) > 0 {
		answer := f.answers[0]
		f.answers = f.answers[1:]
		return answer.busy, answer.err
	}
	return f.busy, f.err
}
