// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"encoding/json"
	"fmt"

	admissionv1 "k8s.io/api/admission/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// MachineAdmissionHandler connects verified cluster scope to the
// No-Replacement policy. It is not registered by this package.
//
// Registration, configuration trust, API availability and
// permission boundaries must be verified before deployment.
type MachineAdmissionHandler struct {
	registrations map[string]NamespaceRegistration
	lookup        ClusterLookup
}

// NewMachineAdmissionHandler takes an immutable snapshot of the
// supplied registrations. Callers must supply trusted configuration.
func NewMachineAdmissionHandler(
	registrations map[string]NamespaceRegistration,
	lookup ClusterLookup,
) *MachineAdmissionHandler {

	copied := make(map[string]NamespaceRegistration, len(registrations))
	for namespace, registration := range registrations {
		copied[namespace] = registration
	}

	return &MachineAdmissionHandler{
		registrations: copied,
		lookup:        lookup,
	}
}

// Handle evaluates admission for CAPI core/v1beta2 Machines.
// Unknown resources, subresources and scope are denied.
//
//nolint:gocritic // admission.Handler requires Request by value.
func (h *MachineAdmissionHandler) Handle(
	ctx context.Context,
	req admission.Request,
) admission.Response {

	return h.handleMachine(ctx, &req)
}

func validMachineResource(req *admission.Request) bool {
	return req.Resource.Group == "cluster.x-k8s.io" &&
		req.Resource.Version == "v1beta2" &&
		req.Resource.Resource == "machines"
}

func (h *MachineAdmissionHandler) handleMachine(
	ctx context.Context,
	req *admission.Request,
) admission.Response {

	if !validMachineResource(req) {
		return admission.Denied("unsupported admission resource")
	}

	if req.SubResource != "" {
		return admission.Denied(
			"Machine subresource requires separate safety policy",
		)
	}

	scope := ResolveRegisteredScope(
		ctx,
		req.Namespace,
		h.registrations,
		h.lookup,
	)

	var decision Decision

	switch req.Operation {
	case admissionv1.Create:
		decision = EvaluateMachineLifecycle(scope, Create)

	case admissionv1.Delete:
		decision = EvaluateMachineLifecycle(scope, Delete)

	case admissionv1.Update:
		if !scope.Verified {
			return admission.Denied(
				"BYOH protection scope could not be verified",
			)
		}

		var oldMachine, newMachine clusterv1.Machine

		if err := json.Unmarshal(req.OldObject.Raw, &oldMachine); err != nil {
			return admission.Denied(
				fmt.Sprintf("invalid original Machine: %v", err),
			)
		}

		if err := json.Unmarshal(req.Object.Raw, &newMachine); err != nil {
			return admission.Denied(
				fmt.Sprintf("invalid updated Machine: %v", err),
			)
		}

		if oldMachine.Namespace != req.Namespace ||
			newMachine.Namespace != req.Namespace ||
			oldMachine.Name != req.Name ||
			newMachine.Name != req.Name {
			return admission.Denied(
				"Machine identity does not match admission request",
			)
		}

		decision = EvaluateMachineUpdate(
			scope,
			&oldMachine,
			&newMachine,
		)

	default:
		return admission.Denied(
			"unsupported Machine admission operation",
		)
	}

	if !decision.Allowed {
		return admission.Denied(decision.Reason)
	}

	return admission.Allowed("No-Replacement policy satisfied")
}
