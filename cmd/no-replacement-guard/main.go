// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	noreplacement "github.com/openkubes/ok-baremetal-byoh/internal/safety/noreplacement"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

func run() error {
	// The standalone server must never start with protection off.
	if os.Getenv(noreplacement.GuardEnabledEnv) != "true" {
		return fmt.Errorf(
			"%s must be explicitly true",
			noreplacement.GuardEnabledEnv,
		)
	}

	// controller-runtime suppresses all logs (incl. admission DENY decisions) without a logger.
	ctrl.SetLogger(klog.Background())

	scheme := runtime.NewScheme()
	// core/v1 is required: the grant reader lists ConfigMaps and scope resolution reads
	// Namespaces through the API reader; without it every grant lookup fails (fail-closed).
	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register core/v1 scheme: %w", err)
	}
	if err := clusterv1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register CAPI scheme: %w", err)
	}

	mgr, err := ctrl.NewManager(
		ctrl.GetConfigOrDie(),
		ctrl.Options{
			Scheme: scheme,
			Metrics: server.Options{
				BindAddress: "0",
			},
			HealthProbeBindAddress: ":9440",
			WebhookServer: webhook.NewServer(
				webhook.Options{
					Port:    9443,
					CertDir: "/tls",
				},
			),
		},
	)
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	guards, err := noreplacement.SetupInPlaceFromEnvironment(
		mgr.GetAPIReader(),
	)
	if err != nil {
		return fmt.Errorf("configure guard: %w", err)
	}

	if guards == nil {
		return fmt.Errorf("guard handlers are unavailable")
	}

	ws := mgr.GetWebhookServer()
	ws.Register(noreplacement.MachineAdmissionPath, &webhook.Admission{Handler: guards.Machine})
	ws.Register(noreplacement.MachineSetAdmissionPath, &webhook.Admission{Handler: guards.MachineSet})
	ws.Register(noreplacement.ByoMachineAdmissionPath, &webhook.Admission{Handler: guards.ByoMachine})
	ws.Register(noreplacement.GrantConfigMapAdmissionPath, &webhook.Admission{Handler: guards.Grant})

	// Readiness = the webhook server is actually serving, so a rollout only completes
	// once admission can be answered (the probe also registers the lazy server runnable).
	if err := mgr.AddReadyzCheck("webhook", ws.StartedChecker()); err != nil {
		return fmt.Errorf("add readyz check: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", ws.StartedChecker()); err != nil {
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
