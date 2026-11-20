// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	GuardEnabledEnv = "BYOH_NO_REPLACEMENT_GUARD_ENABLED"
	RegistryPath    = "/etc/openkubes/no-replacement/registry.json"
)

// SetupFromEnvironment prepares an admission handler but does not
// register or expose a webhook.
//
// Guard enablement is intentionally explicit. A malformed flag,
// missing registry or missing API reader is a startup error.
func SetupFromEnvironment(
	reader client.Reader,
) (*MachineAdmissionHandler, error) {

	return setupFromEnvironment(reader, func() ([]byte, error) {
		return os.ReadFile(RegistryPath)
	})
}

func setupFromEnvironment(
	reader client.Reader,
	readRegistry func() ([]byte, error),
) (*MachineAdmissionHandler, error) {

	enabled := strings.TrimSpace(os.Getenv(GuardEnabledEnv))

	switch enabled {
	case "", "false":
		return nil, nil
	case "true":
		// Continue below.
	default:
		return nil, fmt.Errorf(
			"%s must be true or false",
			GuardEnabledEnv,
		)
	}

	if reader == nil {
		return nil, fmt.Errorf(
			"No-Replacement Guard requires Kubernetes API reader",
		)
	}

	data, err := readRegistry()
	if err != nil {
		return nil, fmt.Errorf(
			"read No-Replacement registry: %w",
			err,
		)
	}

	registrations, err := ParseProtectedRegistry(data)
	if err != nil {
		return nil, fmt.Errorf(
			"invalid No-Replacement registry: %w",
			err,
		)
	}

	return NewMachineAdmissionHandler(
		registrations,
		KubernetesClusterLookup(reader),
	), nil
}
