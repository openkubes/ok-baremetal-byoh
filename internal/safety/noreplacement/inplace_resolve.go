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

// BUILD STATUS: compiles on go 1.26 (verified); behaviour verified in CAPD (PENDING).
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
	case "KubeadmControlPlane":
		return TargetKubeadmControlPlane, ref.UID, nil
	case "MachineSet":
		var ms clusterv1.MachineSet
		if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: ref.Name}, &ms); err != nil {
			return "", "", fmt.Errorf("resolve MachineSet %s: %w", ref.Name, err)
		}
		md := controllerRef(ms.OwnerReferences)
		if md.Kind == "MachineDeployment" {
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
	ref := controllerRef(u.GetOwnerReferences())
	if ref.Kind != "Machine" {
		return "", "", fmt.Errorf("ByoMachine %s has no controller Machine owner", u.GetName())
	}
	var m clusterv1.Machine
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: u.GetNamespace(), Name: ref.Name}, &m); err != nil {
		return "", "", fmt.Errorf("resolve owner Machine %s: %w", ref.Name, err)
	}
	return r.ForMachine(ctx, &m)
}
