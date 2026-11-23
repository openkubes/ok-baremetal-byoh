// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BUILD STATUS: PENDING. Imports controller-runtime; built & integration-tested
// on the Mac / in CI. Constructors + environment wiring for the M6 handlers.

// Env vars configuring the grant-ConfigMap role separation (comma-separated
// authenticated usernames, typically ServiceAccount identities).
const (
	ApproverUsersEnv  = "BYOH_GRANT_APPROVER_USERS"
	InitiatorUsersEnv = "BYOH_GRANT_INITIATOR_USERS"
)

// NewMachineSetAdmissionHandler constructs the MachineSet handler.
func NewMachineSetAdmissionHandler(regs map[string]NamespaceRegistration, lookup ClusterLookup, grants GrantReader) *MachineSetAdmissionHandler {
	return &MachineSetAdmissionHandler{registrations: copyRegs(regs), lookup: lookup, grants: grants}
}

// NewByoMachineAdmissionHandler constructs the ByoMachine handler.
func NewByoMachineAdmissionHandler(regs map[string]NamespaceRegistration, lookup ClusterLookup, grants GrantReader, resolver TargetResolver) *ByoMachineAdmissionHandler {
	return &ByoMachineAdmissionHandler{registrations: copyRegs(regs), lookup: lookup, grants: grants, resolver: resolver}
}

// NewGrantConfigMapAdmissionHandler constructs the grant-ConfigMap handler.
func NewGrantConfigMapAdmissionHandler(actors GuardActors) *GrantConfigMapAdmissionHandler {
	return &GrantConfigMapAdmissionHandler{actors: actors}
}

func copyRegs(in map[string]NamespaceRegistration) map[string]NamespaceRegistration {
	out := make(map[string]NamespaceRegistration, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func parseUserSet(env string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, u := range strings.Split(os.Getenv(env), ",") {
		if u = strings.TrimSpace(u); u != "" {
			out[u] = struct{}{}
		}
	}
	return out
}

// InPlaceGuards bundles the four handlers the guard server registers.
type InPlaceGuards struct {
	Machine    *MachineAdmissionHandler
	MachineSet *MachineSetAdmissionHandler
	ByoMachine *ByoMachineAdmissionHandler
	Grant      *GrantConfigMapAdmissionHandler
}

// SetupInPlaceFromEnvironment builds all four handlers (Machine with the grant
// exception enabled) from the trusted registry and environment. It returns
// (nil, nil) when the guard is disabled, mirroring SetupFromEnvironment.
func SetupInPlaceFromEnvironment(reader client.Reader) (*InPlaceGuards, error) {
	machine, err := SetupFromEnvironment(reader)
	if err != nil || machine == nil {
		return nil, err
	}
	grantReader := KubernetesGrantReader{Reader: reader}
	resolver := TargetResolver{Reader: reader}
	machine = machine.WithGrantReader(grantReader).WithResolver(resolver)

	regs := machine.registrations
	lookup := machine.lookup
	actors := GuardActors{
		Approvers:  parseUserSet(ApproverUsersEnv),
		Initiators: parseUserSet(InitiatorUsersEnv),
	}
	if len(actors.Approvers) == 0 || len(actors.Initiators) == 0 {
		return nil, fmt.Errorf("%s and %s must both be set when the guard is enabled", ApproverUsersEnv, InitiatorUsersEnv)
	}

	return &InPlaceGuards{
		Machine:    machine,
		MachineSet: NewMachineSetAdmissionHandler(regs, lookup, grantReader),
		ByoMachine: NewByoMachineAdmissionHandler(regs, lookup, grantReader, resolver),
		Grant:      NewGrantConfigMapAdmissionHandler(actors),
	}, nil
}
