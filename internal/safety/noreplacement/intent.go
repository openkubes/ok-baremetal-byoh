// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// controlPlaneAPIGroup is the group of KubeadmControlPlanes.
const controlPlaneAPIGroup = "controlplane.cluster.x-k8s.io"

// Resources and fields of the intent guard.
const (
	machineDeploymentsResource = "machinedeployments"
	fieldTemplate              = "template"
	fieldVersion               = "version"
	fieldMaxSurge              = "maxSurge"
	fieldRollingUpdate         = "rollingUpdate"
	fieldRollout               = "rollout"
	fieldStrategy              = "strategy"
)

// intentKind describes where the intent of a KCP or a MachineDeployment lives.
type intentKind struct {
	group, resource string
	target          TargetKind
	versionPath     []string
	// versionTransition is the Machine transition that the grant of a
	// version change must authorize as well (M1 §3).
	versionTransition string
}

var (
	kcpIntent = intentKind{
		group: controlPlaneAPIGroup, resource: "kubeadmcontrolplanes", target: TargetKubeadmControlPlane,
		versionPath: []string{fieldSpec, fieldVersion}, versionTransition: TransitionKCPVersionBump,
	}
	mdIntent = intentKind{
		group: clusterAPIGroup, resource: machineDeploymentsResource, target: TargetMachineDeployment,
		versionPath: []string{fieldSpec, fieldTemplate, fieldSpec, fieldVersion}, versionTransition: TransitionMDFinalizeMachine,
	}
	maxSurgePath = []string{fieldSpec, fieldRollout, fieldStrategy, fieldRollingUpdate, fieldMaxSurge}
)

// Reasons of the intent guard.
const (
	reasonIntentLifecycle = "a protected KubeadmControlPlane or MachineDeployment is created and deleted only outside the in-place-only scope; decommission it first (docs/inplace-upgrade.md, Decommission)"
	reasonIntentReplicas  = "the replicas of a protected KubeadmControlPlane or MachineDeployment do not change: scaling creates or deletes Machines"
	reasonIntentSpec      = "unauthorized change of the protected %s spec: only spec.version under an upgrade grant and lowering maxSurge to 0 are allowed"
	reasonIntentSurge     = "maxSurge of a protected %s may only be lowered to 0: a surge creates a replacement Machine"
)

// EvaluateIntentUpdate decides an UPDATE of the spec of a protected KCP or
// MachineDeployment. It allows only a version change that a grant bound to
// the object authorizes, and lowering maxSurge to 0; every other spec change,
// such as the templates, the kubeadm configuration or the replicas, is denied.
// Metadata is left to the Machine gate and RBAC: the CAPI controllers write
// finalizers, owner references and labels.
func EvaluateIntentUpdate(ctx context.Context, scope Scope, kind *intentKind, oldObj, newObj *unstructured.Unstructured, clusterUID string, grants GrantReader, dryRun bool, now time.Time) Decision {
	if !scope.Verified {
		return deny(reasonScopeUnverified)
	}
	if !scope.Protected {
		return allow()
	}
	oldSpec, _, _ := unstructured.NestedMap(oldObj.Object, fieldSpec)
	newSpec, _, _ := unstructured.NestedMap(newObj.Object, fieldSpec)
	if !reflect.DeepEqual(oldSpec["replicas"], newSpec["replicas"]) {
		return deny(reasonIntentReplicas)
	}
	oldVersion, _, _ := unstructured.NestedString(oldObj.Object, kind.versionPath...)
	newVersion, _, _ := unstructured.NestedString(newObj.Object, kind.versionPath...)
	oldSurge, _, _ := unstructured.NestedFieldNoCopy(oldObj.Object, maxSurgePath...)
	newSurge, _, _ := unstructured.NestedFieldNoCopy(newObj.Object, maxSurgePath...)

	// Compare the rest of the spec without the two fields that may change.
	o, n := oldObj.DeepCopy(), newObj.DeepCopy()
	for _, obj := range []*unstructured.Unstructured{o, n} {
		unstructured.RemoveNestedField(obj.Object, kind.versionPath...)
		unstructured.RemoveNestedField(obj.Object, maxSurgePath...)
	}
	oRest, _, _ := unstructured.NestedMap(o.Object, fieldSpec)
	nRest, _, _ := unstructured.NestedMap(n.Object, fieldSpec)
	if !reflect.DeepEqual(pruneEmpty(oRest), pruneEmpty(nRest)) {
		return deny(fmt.Sprintf(reasonIntentSpec, kind.target))
	}
	if !reflect.DeepEqual(oldSurge, newSurge) && !isZeroSurge(newSurge) {
		return deny(fmt.Sprintf(reasonIntentSurge, kind.target))
	}
	if oldVersion == newVersion {
		return allow()
	}
	if grants == nil {
		return deny("in-place update requires an authorized upgrade grant")
	}
	grant, err := grants.GrantFor(ctx, newObj.GetNamespace(), clusterUID, kind.target, string(newObj.GetUID()), "", now)
	if err != nil {
		return deny(fmt.Sprintf("upgrade grant lookup failed: %v", err))
	}
	return VerifyGrant(scope, grant, GrantQuery{
		DryRun: dryRun, ClusterUID: clusterUID, TargetKind: kind.target, TargetUID: string(newObj.GetUID()),
		Transition: kind.versionTransition, VersionRelevant: true, FromVersion: oldVersion, ToVersion: newVersion, Now: now,
	})
}

// isZeroSurge reports whether a maxSurge is 0 (an int or a string).
func isZeroSurge(v any) bool {
	switch s := v.(type) {
	case int64:
		return s == 0
	case float64:
		return s == 0
	case string:
		return s == "0" || s == "0%"
	}
	return false
}

// pruneEmpty drops the maps that removing a field left empty, so that an
// absent and an emptied parent compare equal.
func pruneEmpty(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			sub = pruneEmpty(sub)
			if len(sub) == 0 {
				continue
			}
			v = sub
		}
		out[k] = v
	}
	return out
}

// IntentAdmissionHandler guards the intent of protected KubeadmControlPlanes
// and MachineDeployments, including their scale subresource.
type IntentAdmissionHandler struct {
	registrations map[string]NamespaceRegistration
	lookup        ClusterLookup
	grants        GrantReader
}

// NewIntentAdmissionHandler returns the intent guard.
func NewIntentAdmissionHandler(regs map[string]NamespaceRegistration, lookup ClusterLookup, grants GrantReader) *IntentAdmissionHandler {
	return &IntentAdmissionHandler{registrations: copyRegs(regs), lookup: lookup, grants: grants}
}

// Handle implements admission.Handler.
//
//nolint:gocritic // admission.Handler requires Request by value.
func (h *IntentAdmissionHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	var kind *intentKind
	switch {
	case req.Resource.Group == kcpIntent.group && req.Resource.Resource == kcpIntent.resource && req.Resource.Version == capiVersion:
		kind = &kcpIntent
	case req.Resource.Group == mdIntent.group && req.Resource.Resource == mdIntent.resource && req.Resource.Version == capiVersion:
		kind = &mdIntent
	default:
		return admission.Denied("unsupported admission resource")
	}
	scope, clusterUID := scopeFor(ctx, req.Namespace, h.registrations, h.lookup)
	decision := h.decide(ctx, &req, kind, scope, clusterUID)
	log := logf.FromContext(ctx).WithValues("object", req.Namespace+"/"+req.Name, "resource", req.Resource.Resource,
		"subresource", req.SubResource, "op", req.Operation, "user", req.UserInfo.Username)
	if !decision.Allowed {
		log.Info("DENY", "reason", decision.Reason)
		return admission.Denied(decision.Reason)
	}
	return admission.Allowed("No-Replacement intent policy satisfied")
}

// evaluateScale denies a change of the replicas through the scale
// subresource.
func evaluateScale(req *admission.Request) Decision {
	var oldScale, newScale struct {
		Spec struct {
			Replicas *int32 `json:"replicas"`
		} `json:"spec"`
	}
	if json.Unmarshal(req.OldObject.Raw, &oldScale) != nil || json.Unmarshal(req.Object.Raw, &newScale) != nil {
		return deny("invalid Scale")
	}
	if !reflect.DeepEqual(oldScale.Spec.Replicas, newScale.Spec.Replicas) {
		return deny(reasonIntentReplicas)
	}
	return allow()
}

func (h *IntentAdmissionHandler) decide(ctx context.Context, req *admission.Request, kind *intentKind, scope Scope, clusterUID string) Decision {
	if !scope.Verified {
		return deny(reasonScopeUnverified)
	}
	if !scope.Protected {
		return allow()
	}
	switch {
	case req.Operation == admissionv1.Create || req.Operation == admissionv1.Delete:
		if req.SubResource != "" {
			return deny("unsupported subresource operation")
		}
		return deny(reasonIntentLifecycle)
	case req.Operation != admissionv1.Update:
		return deny("unsupported operation")
	case req.SubResource == "scale":
		return evaluateScale(req)
	case req.SubResource != "":
		// status is written by the CAPI controllers and not routed here.
		return deny("unsupported subresource")
	}
	oldObj, newObj := &unstructured.Unstructured{}, &unstructured.Unstructured{}
	if err := oldObj.UnmarshalJSON(req.OldObject.Raw); err != nil {
		return deny(fmt.Sprintf("invalid original object: %v", err))
	}
	if err := newObj.UnmarshalJSON(req.Object.Raw); err != nil {
		return deny(fmt.Sprintf("invalid updated object: %v", err))
	}
	if oldObj.GetUID() != newObj.GetUID() || newObj.GetNamespace() != req.Namespace || newObj.GetName() != req.Name {
		return deny("object identity does not match the admission request")
	}
	dryRun := req.DryRun != nil && *req.DryRun
	return EvaluateIntentUpdate(ctx, scope, kind, oldObj, newObj, clusterUID, h.grants, dryRun, time.Now())
}
