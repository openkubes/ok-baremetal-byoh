// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
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
	// grants, when set, enables the grant-authorized in-place UPDATE exception.
	// When nil the handler keeps its strict deny-by-default behaviour.
	grants GrantReader
	// resolver climbs the owner chain to the grant target (KCP / MD).
	resolver TargetResolver
}

// WithGrantReader returns the handler with the read-only grant reader enabled.
func (h *MachineAdmissionHandler) WithGrantReader(r GrantReader) *MachineAdmissionHandler {
	h.grants = r
	return h
}

// WithResolver sets the owner-chain target resolver.
func (h *MachineAdmissionHandler) WithResolver(r TargetResolver) *MachineAdmissionHandler {
	h.resolver = r
	return h
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
	return req.Resource.Group == clusterAPIGroup &&
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
				reasonScopeUnverified,
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

		if h.grants != nil {
			dr := req.DryRun != nil && *req.DryRun
			decision = GuardMachineUpdate(
				ctx,
				h.grants,
				h.resolver,
				scope,
				h.registrations[req.Namespace].ClusterUID,
				&oldMachine,
				&newMachine,
				dr,
				time.Now(),
			)
		} else {
			decision = EvaluateMachineUpdate(
				scope,
				&oldMachine,
				&newMachine,
			)
		}

	default:
		return admission.Denied(
			"unsupported Machine admission operation",
		)
	}

	log := logf.FromContext(ctx).WithValues(
		"machine", req.Namespace+"/"+req.Name, "op", req.Operation,
		"user", req.UserInfo.Username, "dryRun", req.DryRun != nil && *req.DryRun)
	if !decision.Allowed {
		if req.Operation == admissionv1.Update {
			log = log.WithValues("diff", machineDiffSummary(req.OldObject.Raw, req.Object.Raw))
		}
		log.Info("DENY", "reason", decision.Reason)
		return admission.Denied(decision.Reason)
	}

	log.V(1).Info("ALLOW")
	return admission.Allowed("No-Replacement policy satisfied")
}
