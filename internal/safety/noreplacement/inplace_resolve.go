// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BUILD STATUS: compiles on go 1.26 (verified); behavior verified in CAPD (PENDING).
//
// TargetResolver resolves the grant TARGET (kind + UID) of a worker object by
// walking the CAPI owner chain, read-only. The grant binds the KubeadmControlPlane
// or the MachineDeployment — never the intermediate MachineSet or Machine — so the
// resolver climbs to that owner. A resolution miss returns an error and the caller
// fails closed (no grant match -> deny).
type TargetResolver struct {
	Reader client.Reader
}

// ForMachine resolves a Machine to its grant target.
//   - KubeadmControlPlane owner  -> (KubeadmControlPlane, KCP UID)
//   - MachineSet owner           -> read the MS, climb to its MachineDeployment owner
//     -> (MachineDeployment, MD UID); a standalone MS (no MD) -> (MachineSet, MS UID)
func (r TargetResolver) ForMachine(ctx context.Context, m *clusterv1.Machine) (TargetKind, string, error) {
	ref := controllerRef(m.OwnerReferences)
	switch ref.Kind {
	case string(TargetKubeadmControlPlane):
		return TargetKubeadmControlPlane, ref.UID, nil
	case string(TargetMachineSet):
		var ms clusterv1.MachineSet
		if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: ref.Name}, &ms); err != nil {
			return "", "", fmt.Errorf("resolve MachineSet %s: %w", ref.Name, err)
		}
		md := controllerRef(ms.OwnerReferences)
		if md.Kind == string(TargetMachineDeployment) {
			return TargetMachineDeployment, md.UID, nil
		}
		// Standalone MachineSet (not owned by a MachineDeployment).
		return TargetMachineSet, string(ms.UID), nil
	default:
		return TargetKind(ref.Kind), ref.UID, nil
	}
}

// ForByoMachine resolves a ByoMachine (InfraMachine) to its grant target via its
// owning Machine.
func (r TargetResolver) ForByoMachine(ctx context.Context, u *unstructured.Unstructured) (TargetKind, string, error) {
	m, err := r.OwnerMachine(ctx, u)
	if err != nil {
		return "", "", err
	}
	return r.ForMachine(ctx, m)
}

// ByoMachineFollowsMove reports whether a ByoMachine UPDATE is the label re-alignment after the in-place move of
// its owner Machine, and nothing else. It is bound to that move by live objects, all of them required:
//
//   - the labels and annotations differ in exactly the two MachineSet labels (byoMachineSetLabelChange);
//   - the owner Machine (name and UID of the controller reference) exists, is not being deleted, and its
//     infrastructureRef names this ByoMachine;
//   - the Machine is controlled by the target MachineSet (name and UID), carries its set-name and
//     machine-template-hash labels, and the ByoMachine gets exactly those two values;
//   - the source MachineSet, named by the old set-name label, exists, is another MachineSet, and is controlled by
//     the same MachineDeployment (UID) as the target. Neither MachineSet is being deleted.
//
// The set-name label is taken as the MachineSet name, as on the Machine path (setNameMatchesController): a
// MachineSet whose name is not a valid label value (longer than 63 characters) never matches and stays denied.
//
// The Machine reaches the target MachineSet only through the granted move transition; the caller still requires
// the unexpired, claimed grant of that MachineDeployment. Every read error and every mismatch is false.
func (r TargetResolver) ByoMachineFollowsMove(ctx context.Context, oldU, newU *unstructured.Unstructured, machine *clusterv1.Machine) bool {
	source, target, targetHash, ok := byoMachineSetLabelChange(oldU, newU)
	if !ok || source == target || machine == nil || !machineBelongsTo(machine, target, targetHash) {
		return false
	}
	// the ByoMachine is the InfraMachine of that Machine, not only an object that names it as its owner
	if infra := machine.Spec.InfrastructureRef; infra.Kind != newU.GetKind() || infra.Name != newU.GetName() || infra.Name == "" {
		return false
	}
	targetMS, ok := r.machineSetOfDeployment(ctx, machine.Namespace, target)
	if !ok || string(targetMS.UID) != controllerRef(machine.OwnerReferences).UID || targetMS.Labels[machineTemplateHashLabel] != targetHash {
		return false
	}
	sourceMS, ok := r.machineSetOfDeployment(ctx, machine.Namespace, source)
	return ok && sourceMS.UID != targetMS.UID &&
		controllerRef(sourceMS.OwnerReferences).UID == controllerRef(targetMS.OwnerReferences).UID
}

// machineBelongsTo reports a live Machine that is controlled by the MachineSet of that name and carries its labels.
func machineBelongsTo(machine *clusterv1.Machine, machineSet, templateHash string) bool {
	ref := controllerRef(machine.OwnerReferences)
	return machine.DeletionTimestamp.IsZero() &&
		ref.Kind == string(TargetMachineSet) && ref.Name == machineSet && ref.UID != "" &&
		machine.Labels[machineSetNameLabel] == machineSet && machine.Labels[machineTemplateHashLabel] == templateHash
}

// machineSetOfDeployment reads a MachineSet that exists, is not being deleted and is controlled by a
// MachineDeployment.
func (r TargetResolver) machineSetOfDeployment(ctx context.Context, namespace, name string) (*clusterv1.MachineSet, bool) {
	ms := &clusterv1.MachineSet{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, ms); err != nil {
		return nil, false
	}
	md := controllerRef(ms.OwnerReferences)
	return ms, ms.DeletionTimestamp.IsZero() && md.Kind == string(TargetMachineDeployment) && md.UID != ""
}

// OwnerMachine reads the Machine that controls a ByoMachine; its UID must be the one of the owner reference.
func (r TargetResolver) OwnerMachine(ctx context.Context, u *unstructured.Unstructured) (*clusterv1.Machine, error) {
	ref := controllerRef(u.GetOwnerReferences())
	if ref.Kind != "Machine" {
		return nil, fmt.Errorf("ByoMachine %s has no controller Machine owner", u.GetName())
	}
	m := &clusterv1.Machine{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: u.GetNamespace(), Name: ref.Name}, m); err != nil {
		return nil, fmt.Errorf("resolve owner Machine %s: %w", ref.Name, err)
	}
	if string(m.UID) != ref.UID {
		return nil, fmt.Errorf("owner Machine %s has the UID %s, not %s", ref.Name, m.UID, ref.UID)
	}
	return m, nil
}
