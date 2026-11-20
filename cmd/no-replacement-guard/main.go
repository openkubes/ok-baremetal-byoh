// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	noreplacement "github.com/openkubes/ok-baremetal-byoh/internal/safety/noreplacement"

	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const admissionPath = "/validate-cluster-x-k8s-io-v1beta2-machine-no-replacement"

func run() error {
	// The standalone server must never start with protection off.
	if os.Getenv(noreplacement.GuardEnabledEnv) != "true" {
		return fmt.Errorf(
			"%s must be explicitly true",
			noreplacement.GuardEnabledEnv,
		)
	}

	scheme := runtime.NewScheme()
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
			HealthProbeBindAddress: "0",
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

	handler, err := noreplacement.SetupFromEnvironment(
		mgr.GetAPIReader(),
	)
	if err != nil {
		return fmt.Errorf("configure guard: %w", err)
	}

	if handler == nil {
		return fmt.Errorf("guard handler is unavailable")
	}

	mgr.GetWebhookServer().Register(
		admissionPath,
		&webhook.Admission{Handler: handler},
	)

	return mgr.Start(ctrl.SetupSignalHandler())
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
