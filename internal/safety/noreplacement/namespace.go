// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"encoding/json"
	"fmt"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// NamespaceAdmissionHandler closes the namespace-label bypass (REVIEW Q1):
// the guard webhooks route by the label ProtectedNamespaceLabel, so removing
// it would silently switch the guard off. For a namespace in the trusted
// registry the label cannot be removed or changed and the namespace cannot be
// deleted; decommission removes the registry entry first (runbook). The label
// cannot be set on a namespace outside the registry either, as the guard would
// then fail closed for every lifecycle request in it.
//
// The webhook selects namespaces by the label with an objectSelector, which
// the API server evaluates against the old and the new object: removing the
// label is routed here, other namespaces are not.
type NamespaceAdmissionHandler struct {
	registrations map[string]NamespaceRegistration
}

// NewNamespaceAdmissionHandler returns the namespace guard.
func NewNamespaceAdmissionHandler(regs map[string]NamespaceRegistration) *NamespaceAdmissionHandler {
	return &NamespaceAdmissionHandler{registrations: copyRegs(regs)}
}

// EvaluateNamespaceChange decides a namespace CREATE, UPDATE or DELETE.
func EvaluateNamespaceChange(op admissionv1.Operation, registered bool, oldLabel, newLabel string) Decision {
	switch op {
	case admissionv1.Delete:
		if registered {
			return deny("a namespace in the No-Replacement registry is not deleted; decommission it first")
		}
		return allow()
	case admissionv1.Create, admissionv1.Update:
		if registered && oldLabel == labelValueTrue && newLabel != labelValueTrue {
			return deny(fmt.Sprintf("the label %s of a namespace in the No-Replacement registry cannot be removed or changed; decommission it first", ProtectedNamespaceLabel))
		}
		if !registered && newLabel != "" && newLabel != oldLabel {
			return deny(fmt.Sprintf("the label %s is set only on namespaces in the No-Replacement registry", ProtectedNamespaceLabel))
		}
		return allow()
	}
	return deny("unsupported operation")
}

// Handle implements admission.Handler.
//
//nolint:gocritic // admission.Handler requires Request by value.
func (h *NamespaceAdmissionHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Resource.Group != "" || req.Resource.Resource != "namespaces" || req.SubResource != "" {
		return admission.Denied("unsupported admission resource")
	}
	label := func(raw []byte) (string, error) {
		if len(raw) == 0 {
			return "", nil
		}
		ns := &corev1.Namespace{}
		if err := json.Unmarshal(raw, ns); err != nil {
			return "", err
		}
		return ns.Labels[ProtectedNamespaceLabel], nil
	}
	oldLabel, err := label(req.OldObject.Raw)
	if err != nil {
		return admission.Denied(fmt.Sprintf("invalid original Namespace: %v", err))
	}
	newLabel, err := label(req.Object.Raw)
	if err != nil {
		return admission.Denied(fmt.Sprintf("invalid updated Namespace: %v", err))
	}
	registration, registered := h.registrations[req.Name]
	registered = registered && registration.Protected
	decision := EvaluateNamespaceChange(req.Operation, registered, oldLabel, newLabel)
	if !decision.Allowed {
		logf.FromContext(ctx).Info("DENY", "namespace", req.Name, "op", req.Operation, "user", req.UserInfo.Username, "reason", decision.Reason)
		return admission.Denied(decision.Reason)
	}
	return admission.Allowed("No-Replacement namespace policy satisfied")
}
