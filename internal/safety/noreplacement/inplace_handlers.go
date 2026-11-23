// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// BUILD STATUS: PENDING. Imports CAPI/controller-runtime; built & integration-
// tested on the Mac / in CI. These adapters wire the admission requests to the
// read-only Guard* entry points. They are deliberately thin: all policy lives in
// the tested pure functions.
//
// Owner-chain target resolution (worker Machine -> MachineSet -> MachineDeployment,
// ByoMachine -> Machine -> KCP/MD) is implemented in inplace_resolve.go.

// GuardActors maps authenticated usernames to their grant role.
type GuardActors struct {
	Approvers  map[string]struct{}
	Initiators map[string]struct{}
}

func (a GuardActors) resolve(username string) GrantActor {
	if _, ok := a.Approvers[username]; ok {
		return ActorApprover
	}
	if _, ok := a.Initiators[username]; ok {
		return ActorInitiator
	}
	return ActorOther
}

func dryRun(req *admission.Request) bool {
	return req.DryRun != nil && *req.DryRun
}

// scopeFor resolves the protected scope + the trusted cluster UID for a namespace.
func scopeFor(ctx context.Context, ns string, regs map[string]NamespaceRegistration, lookup ClusterLookup) (Scope, string) {
	scope := ResolveRegisteredScope(ctx, ns, regs, lookup)
	return scope, regs[ns].ClusterUID
}

// MachineSetAdmissionHandler admits protected MachineSet UPDATE (allow-by-default,
// move-annotation gated). CREATE/DELETE pass (the Machine gate is authoritative).
type MachineSetAdmissionHandler struct {
	registrations map[string]NamespaceRegistration
	lookup        ClusterLookup
	grants        GrantReader
}

//nolint:gocritic // admission.Handler requires Request by value.
func (h *MachineSetAdmissionHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Update || req.SubResource != "" {
		return admission.Allowed("not a guarded MachineSet update")
	}
	scope, clusterUID := scopeFor(ctx, req.Namespace, h.registrations, h.lookup)
	if !scope.Verified {
		return admission.Denied(reasonScopeUnverified)
	}
	var oldMS, newMS clusterv1.MachineSet
	if err := json.Unmarshal(req.OldObject.Raw, &oldMS); err != nil {
		return admission.Denied(fmt.Sprintf("invalid original MachineSet: %v", err))
	}
	if err := json.Unmarshal(req.Object.Raw, &newMS); err != nil {
		return admission.Denied(fmt.Sprintf("invalid updated MachineSet: %v", err))
	}
	mdUID := controllerRef(newMS.OwnerReferences).UID
	d := GuardMachineSetUpdate(ctx, h.grants, scope, clusterUID, &oldMS, &newMS, dryRun(&req), time.Now(), mdUID)
	if !d.Allowed {
		return admission.Denied(d.Reason)
	}
	return admission.Allowed("No-Replacement MachineSet policy satisfied")
}

// ByoMachineAdmissionHandler admits protected ByoMachine (InfraMachine) UPDATE.
type ByoMachineAdmissionHandler struct {
	registrations map[string]NamespaceRegistration
	lookup        ClusterLookup
	grants        GrantReader
	resolver      TargetResolver
}

//nolint:gocritic // admission.Handler requires Request by value.
func (h *ByoMachineAdmissionHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	scope, clusterUID := scopeFor(ctx, req.Namespace, h.registrations, h.lookup)
	if !scope.Verified {
		return admission.Denied(reasonScopeUnverified)
	}
	switch req.Operation {
	case admissionv1.Create, admissionv1.Delete:
		// Deny-by-default: provisioning/detach of an InfraMachine is never in-place.
		if scope.Protected {
			return admission.Denied("ByoMachine CREATE/DELETE is forbidden during in-place-only lifecycle")
		}
		return admission.Allowed("unprotected")
	case admissionv1.Update:
		if req.SubResource != "" {
			return admission.Allowed("status subresource")
		}
		var oldU, newU unstructured.Unstructured
		if err := oldU.UnmarshalJSON(req.OldObject.Raw); err != nil {
			return admission.Denied(fmt.Sprintf("invalid original ByoMachine: %v", err))
		}
		if err := newU.UnmarshalJSON(req.Object.Raw); err != nil {
			return admission.Denied(fmt.Sprintf("invalid updated ByoMachine: %v", err))
		}
		d := GuardByoMachineUpdate(ctx, h.grants, h.resolver, scope, clusterUID, &oldU, &newU, dryRun(&req), time.Now())
		if !d.Allowed {
			return admission.Denied(d.Reason)
		}
		return admission.Allowed("No-Replacement ByoMachine policy satisfied")
	default:
		return admission.Denied("unsupported ByoMachine operation")
	}
}

// GrantConfigMapAdmissionHandler enforces role/field separation + single-use claim
// + label-removal protection on grant CAS ConfigMaps.
type GrantConfigMapAdmissionHandler struct {
	actors GuardActors
}

//nolint:gocritic // admission.Handler requires Request by value.
func (h *GrantConfigMapAdmissionHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	var oldCM, newCM *corev1.ConfigMap
	if len(req.OldObject.Raw) > 0 {
		oldCM = &corev1.ConfigMap{}
		if err := json.Unmarshal(req.OldObject.Raw, oldCM); err != nil {
			return admission.Denied(fmt.Sprintf("invalid original ConfigMap: %v", err))
		}
	}
	if len(req.Object.Raw) > 0 {
		newCM = &corev1.ConfigMap{}
		if err := json.Unmarshal(req.Object.Raw, newCM); err != nil {
			return admission.Denied(fmt.Sprintf("invalid updated ConfigMap: %v", err))
		}
	}
	// Only admit objects recognized as grants (label OR okx-grant-* name).
	name := req.Name
	var labels map[string]string
	if newCM != nil {
		labels = newCM.Labels
	} else if oldCM != nil {
		labels = oldCM.Labels
	}
	if !IsGrantObject(name, labels) {
		return admission.Allowed("not a grant object")
	}
	var op Operation
	switch req.Operation {
	case admissionv1.Create:
		op = Create
	case admissionv1.Update:
		op = Update
	case admissionv1.Delete:
		op = Delete
	default:
		return admission.Denied("unsupported grant ConfigMap operation")
	}
	d := GuardGrantConfigMap(op, h.actors.resolve(req.UserInfo.Username), oldCM, newCM)
	if !d.Allowed {
		return admission.Denied(d.Reason)
	}
	return admission.Allowed("grant ConfigMap policy satisfied")
}
