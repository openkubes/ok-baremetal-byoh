// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// workerMachine is a moved worker Machine (controller = ms-new) as CAPI v1.14.3
// leaves it after MD-MOVE: set-name still names the source MS and the controller
// ownerRef carries no blockOwnerDeletion (util.ReplaceOwnerRef).
func workerMachine() *clusterv1.Machine {
	return &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name: "w-1", Namespace: "default", UID: "m-uid",
			Labels: map[string]string{
				clusterNameLabel:         "c",
				machineSetNameLabel:      "ms-old",
				machineTemplateHashLabel: "h-new",
			},
			Annotations: map[string]string{updateInProgressAnnotation: "", pendingAckMoveAnnotation: ""},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(), Kind: "MachineSet",
				Name: "ms-new", UID: "ms-uid-new", Controller: ptr.To(true),
			}},
		},
		Spec: clusterv1.MachineSpec{ClusterName: "c", Version: "v1.33.0"},
	}
}

// Live CAPD finding: the target MS's syncMachines re-aligns set-name and re-adds
// blockOwnerDeletion on the SAME controller ref — classified as ShapeMoveAlign.
func TestMoveAlign_SyncMachinesMetadata(t *testing.T) {
	oldM := workerMachine()
	newM := oldM.DeepCopy()
	newM.Labels[machineSetNameLabel] = "ms-new"
	newM.OwnerReferences[0].BlockOwnerDeletion = ptr.To(true)
	c := ExtractMachineChange(oldM, newM)
	if got := ClassifyMachineTransition(c); got != ShapeMoveAlign {
		t.Fatalf("shape = %d, want ShapeMoveAlign", got)
	}
	if id := TransitionIDForShape(ShapeMoveAlign, TargetMachineDeployment); id != TransitionMDMove {
		t.Fatalf("MD id = %q, want MD-MOVE", id)
	}
	if id := TransitionIDForShape(ShapeMoveAlign, TargetKubeadmControlPlane); id != "" {
		t.Fatalf("KCP must not accept MoveAlign, got %q", id)
	}
}

// completeMoveMachine: spec.version + set-name re-alignment in one SSA -> MD-FIN-M.
func TestMoveAlign_CompleteMoveVersionBump(t *testing.T) {
	oldM := workerMachine()
	newM := oldM.DeepCopy()
	newM.Labels[machineSetNameLabel] = "ms-new"
	newM.Spec.Version = "v1.34.0"
	if got := ClassifyMachineTransition(ExtractMachineChange(oldM, newM)); got != ShapeVersionBump {
		t.Fatalf("shape = %d, want ShapeVersionBump", got)
	}
}

func TestMoveAlign_FailClosed(t *testing.T) {
	cases := map[string]func(m *clusterv1.Machine){
		"set-name points to a foreign MS": func(m *clusterv1.Machine) { m.Labels[machineSetNameLabel] = "ms-evil" },
		"set-name removed":                func(m *clusterv1.Machine) { delete(m.Labels, machineSetNameLabel) },
		"align + add pending hook": func(m *clusterv1.Machine) {
			m.Labels[machineSetNameLabel] = "ms-new"
			m.Annotations[pendingHooksAnnotation] = "UpdateMachine"
		},
		"align + extra spec change": func(m *clusterv1.Machine) { m.Labels[machineSetNameLabel] = "ms-new"; m.Spec.FailureDomain = "fd-x" },
		"align + controller moved again": func(m *clusterv1.Machine) {
			m.Labels[machineSetNameLabel] = "ms-x"
			m.OwnerReferences[0].Name = "ms-x"
			m.OwnerReferences[0].UID = "ms-uid-x"
		},
		"align + other reserved label": func(m *clusterv1.Machine) {
			m.Labels[machineSetNameLabel] = "ms-new"
			m.Labels["cluster.x-k8s.io/control-plane"] = ""
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			oldM := workerMachine()
			newM := oldM.DeepCopy()
			mut(newM)
			if got := ClassifyMachineTransition(ExtractMachineChange(oldM, newM)); got != ShapeNone {
				t.Fatalf("expected ShapeNone, got %d", got)
			}
		})
	}
}
