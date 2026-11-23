// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0
//
// inplace-extension is the OpenKubes BYOH RuntimeSDK extension server. It serves
// the CanUpdateMachine, CanUpdateMachineSet and UpdateMachine hooks. CanUpdate*
// is a read-only decision; UpdateMachine requests the in-place upgrade of the
// ByoHost of a Machine (spec.inPlaceUpgrade), drains and uncordons its Node,
// and reports success only for an upgrade that the agent and the Node verify.
package main

import (
	"flag"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/klog/v2"
	runtimecatalog "sigs.k8s.io/cluster-api/api/runtime/catalog"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	"sigs.k8s.io/cluster-api/exp/runtime/server"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	"github.com/openkubes/ok-baremetal-byoh/internal/inplace"
	extension "github.com/openkubes/ok-baremetal-byoh/internal/inplace/extension"
)

const (
	webhookPort     = 9444
	webhookCertDir  = "/tls"
	healthProbeAddr = ":9440"
)

func run() error {
	packageLock := flag.String("package-lock", "/etc/openkubes/inplace/packages.json", "the pinned packages of the target versions")
	upgradeTimeout := flag.Duration("upgrade-timeout", extension.DefaultUpgradeTimeout, "the time after which a running upgrade fails")
	simulate := flag.Bool("simulate-docker-machines", false, "CAPD lab only: update DockerMachines by waiting, without host work")
	flag.Parse()

	// controller-runtime suppresses all log output until a logger is installed.
	// Without this the extension pod produces empty logs, which masks startup
	// state during integration debugging.
	ctrl.SetLogger(klog.Background())

	catalog := runtimecatalog.New()
	if err := runtimehooksv1.AddToCatalog(catalog); err != nil {
		return fmt.Errorf("add runtime hooks to catalog: %w", err)
	}

	srv, err := server.New(server.Options{
		Port:    webhookPort,
		CertDir: webhookCertDir,
		Catalog: catalog,
	})
	if err != nil {
		return fmt.Errorf("create runtime extension server: %w", err)
	}

	// An empty --package-lock pins nothing: every ByoMachine update is
	// declined (the CAPD lab simulates DockerMachines only).
	packages := inplace.PackageLock{}
	if *packageLock != "" {
		data, err := os.ReadFile(*packageLock)
		if err != nil {
			return fmt.Errorf("read the package lock: %w", err)
		}
		if packages, err = inplace.ParsePackageLock(data); err != nil {
			return err
		}
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, clusterv1.AddToScheme, infrav1.AddToScheme} {
		if err := add(scheme); err != nil {
			return err
		}
	}
	config := ctrl.GetConfigOrDie()
	// Every decision reads the live objects: no cache.
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	h := extension.NewExtensionHandlers(c, packages, extension.WorkloadNodes{Reader: c})
	h.UpgradeTimeout = *upgradeTimeout
	h.SimulateDockerMachines = *simulate

	for _, eh := range []server.ExtensionHandler{
		{Hook: runtimehooksv1.CanUpdateMachine, Name: "can-update-machine", HandlerFunc: h.DoCanUpdateMachine},
		{Hook: runtimehooksv1.CanUpdateMachineSet, Name: "can-update-machineset", HandlerFunc: h.DoCanUpdateMachineSet},
		{Hook: runtimehooksv1.UpdateMachine, Name: "update-machine", HandlerFunc: h.DoUpdateMachine},
	} {
		if err := srv.AddExtensionHandler(eh); err != nil {
			return fmt.Errorf("add %s handler: %w", eh.Name, err)
		}
	}

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		WebhookServer:          srv,
		HealthProbeBindAddress: healthProbeAddr,
		// This extension exposes no metrics; disabling the metrics server avoids
		// binding an unused (and, by default, auth-gated) port.
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	// controller-runtime only starts Options.WebhookServer after it has been added
	// to the manager's runnables, and that registration happens lazily the first
	// time GetWebhookServer() is called (controllerManager.webhookServerOnce ->
	// cm.Add(cm.webhookServer)). Without touching GetWebhookServer() the server is
	// constructed but never started, so nothing ever listens on the webhook port.
	// Registering the started checker both triggers that registration and gives a
	// truthful readiness signal (healthy only once the server is actually serving).
	if err := mgr.AddReadyzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
		return fmt.Errorf("add readyz check: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", mgr.GetWebhookServer().StartedChecker()); err != nil {
		return fmt.Errorf("add healthz check: %w", err)
	}

	return mgr.Start(ctrl.SetupSignalHandler())
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
