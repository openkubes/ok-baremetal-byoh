// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"encoding/json"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// BUILD STATUS: PENDING. This file imports CAPI/apimachinery and is built and
// integration-tested on the Mac / in CI, not in the offline sandbox. It is the
// thin CAPI-typed EXTRACTOR that converts live objects into the pure *Change
// structs classified by the tested logic in transition.go / byomachine.go.

// Machine/InfraMachine in-place annotation keys (verified: cluster-api v1.14.3).
const (
	updateInProgressAnnotation = "in-place-updates.internal.cluster.x-k8s.io/update-in-progress"
	pendingAckMoveAnnotation   = "in-place-updates.internal.cluster.x-k8s.io/pending-acknowledge-move"
	pendingHooksAnnotation     = "runtime.cluster.x-k8s.io/pending-hooks"
	machineTemplateHashLabel   = "machine-template-hash"
	machineSetNameLabel        = "cluster.x-k8s.io/set-name"
	updateMachineHookName      = "UpdateMachine"
)

// Keys of an object and of its metadata that the change extraction compares.
const (
	fieldSpec        = "spec"
	fieldMetadata    = "metadata"
	fieldLabels      = "labels"
	fieldAnnotations = "annotations"
)

func annoDelta(oldAnns, newAnns map[string]string, key string) AnnoDelta {
	ov, oOK := oldAnns[key]
	nv, nOK := newAnns[key]
	switch {
	case !oOK && nOK:
		return AnnoAdded
	case oOK && !nOK:
		return AnnoRemoved
	case oOK && nOK && ov != nv:
		return AnnoChanged
	default:
		return AnnoUnchanged
	}
}

func listHas(csv, item string) bool {
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == item {
			return true
		}
	}
	return false
}

func pendingHookDelta(oldAnns, newAnns map[string]string, hook string) AnnoDelta {
	o := listHas(oldAnns[pendingHooksAnnotation], hook)
	n := listHas(newAnns[pendingHooksAnnotation], hook)
	switch {
	case !o && n:
		return AnnoAdded
	case o && !n:
		return AnnoRemoved
	default:
		return AnnoUnchanged
	}
}

func controllerRef(refs []metav1.OwnerReference) OwnerControllerRef {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			return OwnerControllerRef{Kind: r.Kind, Name: r.Name, UID: string(r.UID)}
		}
	}
	return OwnerControllerRef{}
}

// nonControllerRefsEqual reports whether the non-controller ownerRefs are equal.
func nonControllerRefsEqual(a, b []metav1.OwnerReference) bool {
	filter := func(refs []metav1.OwnerReference) []metav1.OwnerReference {
		out := []metav1.OwnerReference{}
		for _, r := range refs {
			if r.Controller == nil || !*r.Controller {
				out = append(out, r)
			}
		}
		return out
	}
	return equality.Semantic.DeepEqual(filter(a), filter(b))
}

// inPlaceReservedKeys are the reserved metadata keys that ARE expected to toggle
// during in-place and are therefore tracked individually (not counted as
// "other reserved metadata changed").
var inPlaceReservedKeys = map[string]struct{}{
	updateInProgressAnnotation: {}, pendingAckMoveAnnotation: {}, pendingHooksAnnotation: {},
	clusterNameLabel: {}, protectionAnnotation: {}, machineTemplateHashLabel: {},
	machineSetNameLabel: {}, // classified explicitly (SetNameLabelChanged)
}

func otherReservedChanged(oldMeta, newMeta map[string]string) bool {
	changed := func(a, b map[string]string) bool {
		for k, av := range a {
			if _, skip := inPlaceReservedKeys[k]; skip {
				continue
			}
			if !reservedMetadataKey(k) {
				continue
			}
			if bv, ok := b[k]; !ok || bv != av {
				return true
			}
		}
		return false
	}
	return changed(oldMeta, newMeta) || changed(newMeta, oldMeta)
}

// ExtractMachineChange builds the pure MachineChange from two Machine objects.
func ExtractMachineChange(oldM, newM *clusterv1.Machine) MachineChange {
	oldSpec := oldM.Spec.DeepCopy()
	newSpec := newM.Spec.DeepCopy()
	oldSpec.Version = ""
	newSpec.Version = ""

	return MachineChange{
		IdentityChanged: oldM.UID != newM.UID || oldM.Name != newM.Name || oldM.Namespace != newM.Namespace,

		VersionFrom:              oldM.Spec.Version,
		VersionTo:                newM.Spec.Version,
		SpecChangedBeyondVersion: !equality.Semantic.DeepEqual(oldSpec, newSpec),

		OwnerControllerFrom:              controllerRef(oldM.OwnerReferences),
		OwnerControllerTo:                controllerRef(newM.OwnerReferences),
		OwnerRefsChangedBeyondController: !nonControllerRefsEqual(oldM.OwnerReferences, newM.OwnerReferences),
		FinalizersChanged:                !equality.Semantic.DeepEqual(oldM.Finalizers, newM.Finalizers),

		UpdateInProgress:         annoDelta(oldM.Annotations, newM.Annotations, updateInProgressAnnotation),
		PendingAckMove:           annoDelta(oldM.Annotations, newM.Annotations, pendingAckMoveAnnotation),
		PendingHookUpdateMachine: pendingHookDelta(oldM.Annotations, newM.Annotations, updateMachineHookName),

		MDUniqueLabelChanged:         oldM.Labels[machineTemplateHashLabel] != newM.Labels[machineTemplateHashLabel],
		SetNameLabelChanged:          labelChanged(oldM.Labels, newM.Labels, machineSetNameLabel),
		SetNameMatchesController:     setNameMatchesController(newM),
		ClusterNameLabelChanged:      oldM.Labels[clusterNameLabel] != newM.Labels[clusterNameLabel],
		ProtectionAnnotationChanged:  oldM.Annotations[protectionAnnotation] != newM.Annotations[protectionAnnotation],
		OtherReservedMetadataChanged: otherReservedChanged(mergeMeta(oldM.Labels, oldM.Annotations), mergeMeta(newM.Labels, newM.Annotations)),
	}
}

// machineDiffSummary lists WHICH top-level spec fields, label keys, annotation
// keys, ownerReferences and finalizers differ between two raw Machines. Keys only,
// never values — it is a diagnostics aid for DENY logs, not a policy input.
func machineDiffSummary(oldRaw, newRaw []byte) map[string][]string {
	var o, n struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
		Spec     map[string]json.RawMessage `json:"spec"`
	}
	if json.Unmarshal(oldRaw, &o) != nil || json.Unmarshal(newRaw, &n) != nil {
		return map[string][]string{"error": {"unparseable"}}
	}
	rawKeys := func(a, b map[string]json.RawMessage, skip map[string]bool) []string {
		var out []string
		seen := map[string]bool{}
		for k := range a {
			seen[k] = true
		}
		for k := range b {
			seen[k] = true
		}
		for k := range seen {
			if !skip[k] && string(a[k]) != string(b[k]) {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
	strKeys := func(field string) []string {
		var a, b map[string]string
		_ = json.Unmarshal(o.Metadata[field], &a)
		_ = json.Unmarshal(n.Metadata[field], &b)
		var out []string
		for k, v := range a {
			if bv, ok := b[k]; !ok || bv != v {
				out = append(out, k)
			}
		}
		for k := range b {
			if _, ok := a[k]; !ok {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}
	meta := rawKeys(o.Metadata, n.Metadata, map[string]bool{
		fieldLabels: true, fieldAnnotations: true, "managedFields": true,
		"resourceVersion": true, "generation": true,
	})
	return map[string][]string{
		fieldSpec:        rawKeys(o.Spec, n.Spec, nil),
		fieldLabels:      strKeys(fieldLabels),
		fieldAnnotations: strKeys(fieldAnnotations),
		fieldMetadata:    meta,
	}
}

// labelChanged reports whether key was added, removed or changed.
func labelChanged(oldL, newL map[string]string, key string) bool {
	ov, oOK := oldL[key]
	nv, nOK := newL[key]
	return oOK != nOK || ov != nv
}

// setNameMatchesController reports whether the Machine's set-name label names
// exactly its controller MachineSet (the only value a re-alignment may set).
func setNameMatchesController(m *clusterv1.Machine) bool {
	ref := controllerRef(m.OwnerReferences)
	v, ok := m.Labels[machineSetNameLabel]
	return ok && ref.Kind == string(TargetMachineSet) && ref.Name != "" && v == ref.Name
}

func mergeMeta(labels, anns map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+len(anns))
	for k, v := range labels {
		out[k] = v
	}
	for k, v := range anns {
		out[k] = v
	}
	return out
}

// ExtractByoMachineChange builds the pure ByoMachineChange from two unstructured
// InfraMachine (ByoMachine) objects.
func ExtractByoMachineChange(oldU, newU *unstructured.Unstructured) ByoMachineChange {
	oldSpec, _, _ := unstructured.NestedMap(oldU.Object, "spec")
	newSpec, _, _ := unstructured.NestedMap(newU.Object, "spec")
	return ByoMachineChange{
		IdentityChanged:   oldU.GetUID() != newU.GetUID() || oldU.GetName() != newU.GetName() || oldU.GetNamespace() != newU.GetNamespace(),
		SpecChanged:       !equality.Semantic.DeepEqual(oldSpec, newSpec),
		OwnerRefsChanged:  !equality.Semantic.DeepEqual(oldU.GetOwnerReferences(), newU.GetOwnerReferences()),
		FinalizersChanged: !equality.Semantic.DeepEqual(oldU.GetFinalizers(), newU.GetFinalizers()),
		UpdateInProgress:  annoDelta(oldU.GetAnnotations(), newU.GetAnnotations(), updateInProgressAnnotation),
		MetadataChanged: !equality.Semantic.DeepEqual(oldU.GetLabels(), newU.GetLabels()) ||
			!equality.Semantic.DeepEqual(oldU.GetAnnotations(), newU.GetAnnotations()),
	}
}

// byoMachineSetLabelChange returns the old and the new MachineSet name when the labels and annotations of a
// ByoMachine differ in exactly the two MachineSet labels (cluster.x-k8s.io/set-name and machine-template-hash), both
// present and non-empty before and after. Any annotation change, any other label, an added or a removed label, or
// only one of the two makes it not ok.
func byoMachineSetLabelChange(oldU, newU *unstructured.Unstructured) (source, target, targetHash string, ok bool) {
	if !equality.Semantic.DeepEqual(oldU.GetAnnotations(), newU.GetAnnotations()) {
		return "", "", "", false
	}
	oldLabels, newLabels := oldU.GetLabels(), newU.GetLabels()
	for key := range mergeMeta(oldLabels, newLabels) {
		oldValue, hadOld := oldLabels[key]
		newValue, hasNew := newLabels[key]
		same := hadOld == hasNew && oldValue == newValue
		if key == machineSetNameLabel || key == machineTemplateHashLabel {
			if same || !hadOld || !hasNew || oldValue == "" || newValue == "" {
				return "", "", "", false
			}
			continue
		}
		if !same {
			return "", "", "", false
		}
	}
	source, target, targetHash = oldLabels[machineSetNameLabel], newLabels[machineSetNameLabel], newLabels[machineTemplateHashLabel]
	return source, target, targetHash, source != "" && target != "" && targetHash != ""
}

// MachineSetMoveTouched reports whether a MachineSet UPDATE touches either
// move-authorization annotation.
func MachineSetMoveTouched(oldMS, newMS *clusterv1.MachineSet) bool {
	return MoveAnnotationTouched(oldMS.Annotations, newMS.Annotations)
}
