// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package resettest_test runs the ByoMachine reconciler against an API server
// that enforces the real ByoHost validating webhook: the deletion of a
// ByoMachine whose host carries an in-place upgrade. The suite of the
// controllers runs without webhooks and the suite of the webhooks without the
// reconciler; the defect that this guards against lived between the two (the
// reconciler wrote what the webhook refuses).
package resettest_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	controllers "github.com/openkubes/ok-baremetal-byoh/controllers/infrastructure"
	"github.com/openkubes/ok-baremetal-byoh/test/builder"
	"github.com/openkubes/ok-baremetal-byoh/test/utils/crds"
)

const (
	namespace   = "default"
	clusterName = "cluster"
	webhookPath = "/validate-infrastructure-cluster-x-k8s-io-v1beta1-byohost"
	resetDenied = "a host that is reset is not upgraded"
)

// suite is the API server with the webhook, and the clients of the test.
type suite struct {
	ctx        context.Context
	admin      client.Client // uncached, as an administrator or the extension
	cached     client.Client // the client of the reconciler
	reconciler *controllers.ByoMachineReconciler
	watch      client.WithWatch // uncached, for a reconciler with intercepted writes
}

// byoHostWebhook is the registration of the ByoHost validator, as
// config/webhook/manifests.yaml has it (fail closed).
func byoHostWebhook() *admissionregistrationv1.ValidatingWebhookConfiguration {
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "byohost-validator"},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name:                    "vbyohost.kb.io",
			AdmissionReviewVersions: []string{"v1"},
			SideEffects:             ptr.To(admissionregistrationv1.SideEffectClassNone),
			FailurePolicy:           ptr.To(admissionregistrationv1.Fail),
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{Name: "webhook-service", Namespace: "system", Path: ptr.To(webhookPath)},
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update, admissionregistrationv1.Delete},
				Rule: admissionregistrationv1.Rule{
					APIGroups: []string{infrav1.GroupVersion.Group}, APIVersions: []string{infrav1.GroupVersion.Version},
					Resources: []string{"byohosts", "byohosts/status"},
				},
			}},
		}},
	}
}

func start(t *testing.T) *suite {
	t.Helper()
	g := NewWithT(t)
	capiCore, err := crds.ClusterAPICoreDir()
	g.Expect(err).NotTo(HaveOccurred())
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases"), capiCore},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{ValidatingWebhooks: []*admissionregistrationv1.ValidatingWebhookConfiguration{byoHostWebhook()}},
	}
	cfg, err := env.Start()
	g.Expect(err).NotTo(HaveOccurred())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = env.Stop()
	})

	scheme := runtime.NewScheme()
	g.Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	g.Expect(infrav1.AddToScheme(scheme)).To(Succeed())
	g.Expect(clusterv1.AddToScheme(scheme)).To(Succeed())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host: env.WebhookInstallOptions.LocalServingHost, Port: env.WebhookInstallOptions.LocalServingPort, CertDir: env.WebhookInstallOptions.LocalServingCertDir,
		}),
	})
	g.Expect(err).NotTo(HaveOccurred())
	mgr.GetWebhookServer().Register(webhookPath, &webhook.Admission{Handler: infrav1.NewByoHostValidator(scheme)})

	s := &suite{ctx: ctx, cached: mgr.GetClient()}
	s.admin, err = client.New(cfg, client.Options{Scheme: scheme})
	g.Expect(err).NotTo(HaveOccurred())
	s.watch, err = client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	g.Expect(err).NotTo(HaveOccurred())
	s.reconciler = &controllers.ByoMachineReconciler{Client: mgr.GetClient(), Recorder: events.NewFakeRecorder(1024), RequeuePeriod: time.Second}
	// Registers the index of the ByoHosts. The manager itself is not started:
	// the test calls Reconcile, so that every step is deterministic.
	g.Expect(s.reconciler.SetupWithManager(ctx, mgr)).To(Succeed())
	go func() { _ = mgr.GetCache().Start(ctx) }()
	go func() { _ = mgr.GetWebhookServer().Start(ctx) }()
	g.Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue())

	byoCluster := builder.ByoCluster(namespace, clusterName).Build()
	g.Expect(s.admin.Create(ctx, byoCluster)).To(Succeed())
	g.Expect(s.admin.Create(ctx, builder.Cluster(namespace, clusterName).WithInfrastructureRef(byoCluster).Build())).To(Succeed())
	return s
}

// fixture is a ByoMachine in deletion with its attached ByoHost.
type fixture struct {
	*suite
	g       *WithT
	machine types.NamespacedName
	host    types.NamespacedName
	uid     types.UID
}

// deleting creates a Machine, its ByoMachine and a ByoHost attached to it, and
// deletes the ByoMachine, which its finalizer keeps.
func (s *suite) deleting(t *testing.T, name string) *fixture {
	t.Helper()
	g := NewWithT(t)
	g.SetDefaultEventuallyTimeout(20 * time.Second)
	machine := builder.Machine(namespace, name).WithClusterName(clusterName).WithClusterVersion("v1.37.2").WithBootstrapDataSecret("bootstrap").Build()
	g.Expect(s.admin.Create(s.ctx, machine)).To(Succeed())
	byoMachine := builder.ByoMachine(namespace, name).WithClusterLabel(clusterName).WithOwnerMachine(machine).Build()
	controllerutil.AddFinalizer(byoMachine, infrav1.MachineFinalizer)
	g.Expect(s.admin.Create(s.ctx, byoMachine)).To(Succeed())

	host := builder.ByoHost(namespace, "host-"+name).Build()
	host.Spec.ByoMachineRef = &infrav1.ByoMachineReference{Name: byoMachine.Name, UID: byoMachine.UID}
	// The webhook server may still be starting for the first request.
	g.Eventually(func() error { return s.admin.Create(s.ctx, host) }).Should(Succeed())

	g.Expect(s.admin.Delete(s.ctx, byoMachine)).To(Succeed())
	f := &fixture{suite: s, g: g, machine: client.ObjectKeyFromObject(byoMachine), host: client.ObjectKeyFromObject(host), uid: byoMachine.UID}
	g.Eventually(func() bool {
		cachedMachine := &infrav1.ByoMachine{}
		return s.cached.Get(s.ctx, f.machine, cachedMachine) == nil && !cachedMachine.DeletionTimestamp.IsZero()
	}).Should(BeTrue())
	f.synced()
	return f
}

// now returns the ByoHost as the API server has it.
func (f *fixture) now() *infrav1.ByoHost {
	host := &infrav1.ByoHost{}
	f.g.Expect(f.admin.Get(f.ctx, f.host, host)).To(Succeed())
	return host
}

// synced waits until the cache of the reconciler has the current ByoHost.
func (f *fixture) synced() *infrav1.ByoHost {
	host := f.now()
	f.g.Eventually(func() string {
		cachedHost := &infrav1.ByoHost{}
		_ = f.cached.Get(f.ctx, f.host, cachedHost)
		return cachedHost.ResourceVersion
	}).Should(Equal(host.ResourceVersion))
	return host
}

func (f *fixture) request(id string, attempt int32) *infrav1.InPlaceUpgradeRequest {
	return &infrav1.InPlaceUpgradeRequest{
		ID: id, Attempt: attempt,
		ClusterName: clusterName, ClusterUID: "uid-of-cluster", MachineName: f.machine.Name, MachineUID: "uid-of-machine",
		ByoMachineUID: f.uid, NodeName: f.host.Name, Role: infrav1.InPlaceUpgradeWorker,
		FromVersion: "v1.36.6", ToVersion: "v1.37.2",
		Packages: infrav1.InPlaceUpgradePackages{
			Repository:               "https://pkgs.k8s.io/core:/stable:/v1.37/deb/",
			RepositoryKeyFingerprint: "DE15B14486CD377B9E876E1A234654DA9A296436",
			Debs: []infrav1.PinnedDeb{
				{Name: "kubeadm", Version: "1.37.2-1.1", SHA256: "d1674bffcf80b36531375b35cf68d0dc23453a373c1b4537d78b6960732bc699"},
				{Name: "kubelet", Version: "1.37.2-1.1", SHA256: "f95d0b572a24563bb6e2a1a1f0e84a8a30a24e11c95068b2e81c294a0e1bee11"},
				{Name: "kubectl", Version: "1.37.2-1.1", SHA256: "e95d73aaf1da438cc8b287545b128fbe5e6a9f3723ecaa25eac1b42b8fe542ab"},
			},
		},
	}
}

// requested writes an upgrade request through the webhook, as the extension.
func (f *fixture) requested(id string, attempt int32) error {
	host := f.now()
	host.Spec.InPlaceUpgrade = f.request(id, attempt)
	return f.admin.Update(f.ctx, host)
}

// reported writes the status of the upgrade and waits for the cache.
func (f *fixture) reported(id string, attempt int32, phase infrav1.InPlaceUpgradePhase) *infrav1.ByoHost {
	host := f.now()
	host.Status.InPlaceUpgrade = &infrav1.InPlaceUpgradeStatus{ID: id, Attempt: attempt, Phase: phase}
	if phase == infrav1.InPlaceUpgradeSucceeded {
		host.Status.InPlaceUpgrade.ObservedKubeletVersion, host.Status.InPlaceUpgrade.ObservedNodeVersion = "v1.37.2", "v1.37.2"
	}
	f.g.Expect(f.admin.Status().Update(f.ctx, host)).To(Succeed())
	return f.synced()
}

func (f *fixture) reconcile(r *controllers.ByoMachineReconciler) (reconcile.Result, error) {
	return r.Reconcile(f.ctx, reconcile.Request{NamespacedName: f.machine})
}

// waits checks that a reconcile neither writes the ByoHost nor fails, and that
// the ByoMachine reports why it waits.
func (f *fixture) waits(before *infrav1.ByoHost, reason string) {
	res, err := f.reconcile(f.reconciler)
	f.g.Expect(err).NotTo(HaveOccurred())
	f.g.Expect(res.IsZero()).To(BeTrue())
	host := f.now()
	f.g.Expect(host.ResourceVersion).To(Equal(before.ResourceVersion), "the ByoHost was written")
	f.g.Expect(host.Spec.ResetRequested).To(BeFalse())
	byoMachine := &infrav1.ByoMachine{}
	f.g.Expect(f.admin.Get(f.ctx, f.machine, byoMachine)).To(Succeed())
	var found bool
	for _, c := range byoMachine.Status.Conditions {
		if c.Type == infrav1.BYOHostReady {
			found = true
			f.g.Expect(c.Reason).To(Equal(infrav1.WaitingForInPlaceUpgradeReason))
			f.g.Expect(c.Message).To(ContainSubstring(reason))
		}
	}
	f.g.Expect(found).To(BeTrue(), "no BYOHostReady condition")
}

// racing returns a reconciler whose first patch of a ByoHost is preceded by
// the given write: it happens after the reconciler has read the host.
func (f *fixture) racing(write func()) (reconciler *controllers.ByoMachineReconciler, done *bool) {
	raced := false
	return &controllers.ByoMachineReconciler{
		Recorder: f.reconciler.Recorder, RequeuePeriod: time.Second,
		Client: interceptor.NewClient(f.watch, interceptor.Funcs{
			// The reconciler lists the ByoHosts with the index of the cache.
			List: func(ctx context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return f.cached.List(ctx, list, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*infrav1.ByoHost); ok && !raced {
					raced = true
					write()
				}
				return c.Patch(ctx, obj, p, opts...)
			},
		}),
	}, &raced
}

//nolint:funlen,gocyclo // One API server with the webhook for all scenarios of the reset.
func TestResetAfterInPlaceUpgradeBehindTheWebhook(t *testing.T) {
	s := start(t)

	t.Run("the webhook is enforced: a reset beside a request is refused", func(t *testing.T) {
		f := s.deleting(t, "enforced")
		f.g.Expect(f.requested("abc123", 0)).To(Succeed())
		host := f.now()
		host.Spec.ResetRequested = true
		err := f.admin.Update(f.ctx, host)
		f.g.Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%v", err)
		f.g.Expect(err.Error()).To(ContainSubstring(resetDenied))
	})

	t.Run("without an upgrade the ordinary reset is requested", func(t *testing.T) {
		f := s.deleting(t, "ordinary")
		_, err := f.reconcile(f.reconciler)
		f.g.Expect(err).NotTo(HaveOccurred())
		f.g.Expect(f.now().Spec.ResetRequested).To(BeTrue())
	})

	t.Run("after Succeeded the request leaves in the write that requests the reset, and the host is released", func(t *testing.T) {
		f := s.deleting(t, "succeeded")
		f.g.Expect(f.requested("abc123", 0)).To(Succeed())
		before := f.reported("abc123", 0, infrav1.InPlaceUpgradeSucceeded)

		res, err := f.reconcile(f.reconciler)
		f.g.Expect(err).NotTo(HaveOccurred())
		f.g.Expect(res.IsZero()).To(BeTrue())
		after := f.synced()
		f.g.Expect(after.Spec.ResetRequested).To(BeTrue())
		f.g.Expect(after.Spec.InPlaceUpgrade).To(BeNil())
		// One admitted write for both changes.
		f.g.Expect(after.Generation).To(Equal(before.Generation + 1))
		f.g.Expect(after.Status.InPlaceUpgrade).To(Equal(before.Status.InPlaceUpgrade))

		// A retry before the agent has reset the host writes nothing.
		_, err = f.reconcile(f.reconciler)
		f.g.Expect(err).NotTo(HaveOccurred())
		f.g.Expect(f.now().ResourceVersion).To(Equal(after.ResourceVersion))

		// No new request is admitted on the host that is reset.
		err = f.requested("def456", 0)
		f.g.Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%v", err)
		f.g.Expect(err.Error()).To(ContainSubstring(resetDenied))

		// The agent reports the reset; the host is detached and the ByoMachine goes.
		host := f.now()
		host.Status.LastReset = &infrav1.LastReset{ByoMachineUID: f.uid, Time: metav1.Now()}
		f.g.Expect(f.admin.Status().Update(f.ctx, host)).To(Succeed())
		f.synced()
		_, err = f.reconcile(f.reconciler)
		f.g.Expect(err).NotTo(HaveOccurred())
		released := f.now()
		f.g.Expect(released.Spec).To(Equal(infrav1.ByoHostSpec{}))
		f.g.Expect(released.Status.InPlaceUpgrade.Phase).To(Equal(infrav1.InPlaceUpgradeSucceeded))
		f.g.Eventually(func() bool {
			return apierrors.IsNotFound(f.admin.Get(f.ctx, f.machine, &infrav1.ByoMachine{}))
		}).Should(BeTrue())

		// A request on the free host is refused too.
		err = f.requested("def456", 0)
		f.g.Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%v", err)
	})

	t.Run("after Failed the reset is not requested", func(t *testing.T) {
		f := s.deleting(t, "failed")
		f.g.Expect(f.requested("abc123", 0)).To(Succeed())
		before := f.reported("abc123", 0, infrav1.InPlaceUpgradeFailed)
		f.waits(before, "has failed")
		f.waits(before, "has failed")
		f.g.Expect(f.now().Status.InPlaceUpgrade.Phase).To(Equal(infrav1.InPlaceUpgradeFailed))
	})

	t.Run("while Running the reset is not requested, and it is after Succeeded", func(t *testing.T) {
		f := s.deleting(t, "running")
		f.g.Expect(f.requested("abc123", 0)).To(Succeed())
		before := f.reported("abc123", 0, infrav1.InPlaceUpgradeRunning)
		f.waits(before, "is Running")

		f.reported("abc123", 0, infrav1.InPlaceUpgradeSucceeded)
		_, err := f.reconcile(f.reconciler)
		f.g.Expect(err).NotTo(HaveOccurred())
		f.g.Expect(f.now().Spec.ResetRequested).To(BeTrue())
		f.g.Expect(f.now().Spec.InPlaceUpgrade).To(BeNil())
	})

	t.Run("an unreported request, another attempt and another upgrade keep the reset from being requested", func(t *testing.T) {
		f := s.deleting(t, "unreported")
		f.g.Expect(f.requested("abc123", 0)).To(Succeed())
		f.waits(f.synced(), "has not reported it yet")

		// The status of attempt 0 beside the request of attempt 1.
		f.reported("abc123", 0, infrav1.InPlaceUpgradeSucceeded)
		f.g.Expect(f.requested("abc123", 1)).To(Succeed())
		f.waits(f.synced(), "has not reported it yet")

		// The status of another upgrade.
		before := f.reported("earlier", 1, infrav1.InPlaceUpgradeSucceeded)
		f.waits(before, "has not reported it yet")
	})

	t.Run("a request that changes between the read and the patch is not overwritten", func(t *testing.T) {
		f := s.deleting(t, "conflict")
		f.g.Expect(f.requested("abc123", 0)).To(Succeed())
		f.reported("abc123", 0, infrav1.InPlaceUpgradeSucceeded)

		racing, raced := f.racing(func() { f.g.Expect(f.requested("abc123", 1)).To(Succeed()) })
		res, err := f.reconcile(racing)
		f.g.Expect(err).NotTo(HaveOccurred())
		f.g.Expect(*raced).To(BeTrue())
		f.g.Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		host := f.synced()
		f.g.Expect(host.Spec.ResetRequested).To(BeFalse())
		f.g.Expect(host.Spec.InPlaceUpgrade.Attempt).To(Equal(int32(1)))
		// The retry sees the new attempt and waits for it.
		f.waits(host, "has not reported it yet")
	})

	t.Run("a request written between the read and the ordinary reset patch is not reset over", func(t *testing.T) {
		f := s.deleting(t, "interleaved")
		// The reconciler reads a host without a request and uses the ordinary
		// patch (no optimistic lock); the extension writes its request first.
		racing, raced := f.racing(func() { f.g.Expect(f.requested("abc123", 0)).To(Succeed()) })
		_, err := f.reconcile(racing)
		f.g.Expect(*raced).To(BeTrue())
		// The webhook refuses the reset beside the request that now exists.
		f.g.Expect(err).To(HaveOccurred())
		f.g.Expect(strings.Contains(err.Error(), resetDenied)).To(BeTrue(), "%v", err)
		host := f.synced()
		f.g.Expect(host.Spec.ResetRequested).To(BeFalse())
		f.g.Expect(host.Spec.InPlaceUpgrade).NotTo(BeNil())
		// The retry sees the request and waits for it.
		f.waits(host, "has not reported it yet")
	})
}
