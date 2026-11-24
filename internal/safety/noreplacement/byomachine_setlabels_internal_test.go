// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The label re-alignment of a ByoMachine after the in-place move of its Machine, as seen on the ha1 lab: the
// MachineSet controller of the new MachineSet applies its two labels to the InfraMachine (syncMachines). The guard
// denied it ("ByoMachine change is not a recognized in-place transition") and Cluster API then blocks the reconcile
// of the new MachineSet.

const (
	moveNS        = "ha1"
	moveCluster   = "c-uid"
	moveMDUID     = "md-uid"
	moveSource    = "md-0-frlf2"
	moveTarget    = "md-0-stdd2"
	moveSourceUID = "ms-source-uid"
	moveTargetUID = "ms-target-uid"
	moveMachine   = "md-0-frlf2-4725m"
	moveSrcHash   = "795260781-frlf2"
	moveTgtHash   = "3719851804-stdd2"
)

func moveMachineSet(name, uid, hash, mdUID string) *clusterv1.MachineSet {
	return &clusterv1.MachineSet{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: moveNS, UID: types.UID(uid),
		Labels: map[string]string{machineTemplateHashLabel: hash},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: clusterv1.GroupVersion.String(), Kind: "MachineDeployment",
			Name: "md-0", UID: types.UID(mdUID), Controller: ptr.To(true)}},
	}}
}

// movedMachine is the Machine after the granted move: controlled by the target MachineSet, with its labels.
func movedMachine() *clusterv1.Machine {
	return &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{
		Name: moveMachine, Namespace: moveNS, UID: "machine-uid",
		Labels: map[string]string{"cluster.x-k8s.io/cluster-name": "ha1", machineSetNameLabel: moveTarget, machineTemplateHashLabel: moveTgtHash},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: clusterv1.GroupVersion.String(), Kind: "MachineSet",
			Name: moveTarget, UID: moveTargetUID, Controller: ptr.To(true)}},
	}, Spec: clusterv1.MachineSpec{InfrastructureRef: clusterv1.ContractVersionedObjectReference{
		APIGroup: "infrastructure.cluster.x-k8s.io", Kind: "ByoMachine", Name: moveMachine}}}
}

func deleting[T client.Object](o T) T {
	o.SetDeletionTimestamp(&metav1.Time{Time: grantNow})
	o.SetFinalizers([]string{"test/keep"})
	return o
}

func moveByoMachine(setName, hash string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1", "kind": "ByoMachine",
		"spec": map[string]any{"providerID": "byoh://ha1-w-1/tkleaq"},
	}}
	u.SetName(moveMachine)
	u.SetNamespace(moveNS)
	u.SetUID("byomachine-uid")
	u.SetLabels(map[string]string{"cluster.x-k8s.io/cluster-name": "ha1", machineSetNameLabel: setName, machineTemplateHashLabel: hash})
	u.SetAnnotations(map[string]string{updateInProgressAnnotation: "", "cluster.x-k8s.io/cloned-from-name": "md-0"})
	u.SetFinalizers([]string{"byomachine.infrastructure.cluster.x-k8s.io"})
	u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: clusterv1.GroupVersion.String(), Kind: "Machine",
		Name: moveMachine, UID: "machine-uid", Controller: ptr.To(true)}})
	return u
}

func moveGrant(kind TargetKind, targetUID string, notAfter time.Time, claim, transitions string) *corev1.ConfigMap {
	d := map[string]string{
		"grantID": "g", "clusterUID": moveCluster, "targetKind": string(kind), "targetName": "t", "targetUID": targetUID,
		"fromVersion": "v1.36.6", "toVersion": "v1.37.2", "transitions": transitions,
		"payloadDigest": "sha256:p", "notAfter": notAfter.Format(time.RFC3339), "permittedUseCount": "1",
		"requestDigest": "sha256:r", "authorizationDigest": "sha256:a",
	}
	if claim != "" {
		d[GrantClaimKey] = claim
	}
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "okx-grant-" + string(kind) + "-" + targetUID, Namespace: moveNS,
		Labels: map[string]string{GrantLabel: "true"}}, Data: d}
}

const mdTransitions = "MD-MOVE,MD-FIN-M,MD-TRIG,MD-C1,MD-C2,MD-FIN-IM,MD-ACK"

func moveWorld(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func guardMove(c client.Client, oldU, newU *unstructured.Unstructured, dryRun bool) Decision {
	return GuardByoMachineUpdate(context.Background(), KubernetesGrantReader{Reader: c}, TargetResolver{Reader: c},
		protectedScope(), moveCluster, oldU, newU, dryRun, grantNow)
}

//nolint:funlen // one table: the allowed re-alignment and every binding that must hold for it.
func TestByoMachineSetLabelsFollowTheMove(t *testing.T) {
	future, past := grantNow.Add(time.Hour), grantNow.Add(-time.Minute)
	source := func() *clusterv1.MachineSet { return moveMachineSet(moveSource, moveSourceUID, moveSrcHash, moveMDUID) }
	target := func() *clusterv1.MachineSet { return moveMachineSet(moveTarget, moveTargetUID, moveTgtHash, moveMDUID) }
	grant := func() *corev1.ConfigMap {
		return moveGrant(TargetMachineDeployment, moveMDUID, future, "claim", mdTransitions)
	}
	before := func() *unstructured.Unstructured { return moveByoMachine(moveSource, moveSrcHash) }
	after := func() *unstructured.Unstructured { return moveByoMachine(moveTarget, moveTgtHash) }

	for _, tc := range []struct {
		name   string
		world  []client.Object
		oldU   *unstructured.Unstructured
		newU   func() *unstructured.Unstructured
		allow  bool
		reason string
	}{
		{"the re-alignment after the granted move", []client.Object{source(), target(), movedMachine(), grant()}, before(), after, true, ""},

		// the grant
		{"no grant", []client.Object{source(), target(), movedMachine()}, before(), after, false, "requires an authorized upgrade grant"},
		{"an expired grant", []client.Object{source(), target(), movedMachine(), moveGrant(TargetMachineDeployment, moveMDUID, past, "claim", mdTransitions)}, before(), after, false, "requires an authorized upgrade grant"},
		{"an unclaimed grant", []client.Object{source(), target(), movedMachine(), moveGrant(TargetMachineDeployment, moveMDUID, future, "", mdTransitions)}, before(), after, false, "has not been claimed"},
		{"a grant of another MachineDeployment", []client.Object{source(), target(), movedMachine(), moveGrant(TargetMachineDeployment, "other-md-uid", future, "claim", mdTransitions)}, before(), after, false, "requires an authorized upgrade grant"},
		{"a grant without the InfraMachine transition", []client.Object{source(), target(), movedMachine(), moveGrant(TargetMachineDeployment, moveMDUID, future, "claim", "MD-MOVE,MD-FIN-M")}, before(), after, false, `transition "MD-FIN-IM" is not authorized`},
		{"two valid grants of the MachineDeployment (ambiguous)", []client.Object{source(), target(), movedMachine(), grant(), func() *corev1.ConfigMap {
			g := grant()
			g.Name += "-2"
			return g
		}()}, before(), after, false, "requires an authorized upgrade grant"},

		// the labels
		{"another label changes too", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured {
			u := after()
			l := u.GetLabels()
			l["cluster.x-k8s.io/cluster-name"] = "other"
			u.SetLabels(l)
			return u
		}, false, "not a recognized in-place transition"},
		{"a label is added", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured {
			u := after()
			l := u.GetLabels()
			l["extra"] = "x"
			u.SetLabels(l)
			return u
		}, false, "not a recognized in-place transition"},
		{"only the hash changes", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured { return moveByoMachine(moveSource, moveTgtHash) }, false, "not a recognized in-place transition"},
		{"the old labels are missing", []client.Object{source(), target(), movedMachine(), grant()}, func() *unstructured.Unstructured {
			u := before()
			u.SetLabels(nil)
			return u
		}(), after, false, "not a recognized in-place transition"},
		{"an empty hash", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured { return moveByoMachine(moveTarget, "") }, false, "not a recognized in-place transition"},
		{"only the set-name changes", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured { return moveByoMachine(moveTarget, moveSrcHash) }, false, "not a recognized in-place transition"},
		{"the set-name is removed", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured {
			u := after()
			l := u.GetLabels()
			delete(l, machineSetNameLabel)
			u.SetLabels(l)
			return u
		}, false, "not a recognized in-place transition"},
		{"a set-name that is not the MachineSet of the Machine", []client.Object{source(), target(), movedMachine(), grant(), moveMachineSet("md-0-other", "ms-other-uid", "1-other", moveMDUID)}, before(), func() *unstructured.Unstructured { return moveByoMachine("md-0-other", "1-other") }, false, "not a recognized in-place transition"},
		{"a hash that is not the one of the Machine", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured { return moveByoMachine(moveTarget, "forged") }, false, "not a recognized in-place transition"},
		{"Machine and ByoMachine agree on a hash that the target MachineSet does not have", []client.Object{source(), target(), func() *clusterv1.Machine {
			m := movedMachine()
			m.Labels[machineTemplateHashLabel] = "forged"
			return m
		}(), grant()}, before(), func() *unstructured.Unstructured { return moveByoMachine(moveTarget, "forged") }, false, "not a recognized in-place transition"},
		{"an annotation is removed too", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured {
			u := after()
			a := u.GetAnnotations()
			delete(a, "cluster.x-k8s.io/cloned-from-name")
			u.SetAnnotations(a)
			return u
		}, false, "not a recognized in-place transition"},
		{"an annotation changes too", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured {
			u := after()
			a := u.GetAnnotations()
			a["x"] = "y"
			u.SetAnnotations(a)
			return u
		}, false, "not a recognized in-place transition"},

		// identity, spec, owner references, finalizers
		{"the spec changes too", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured {
			u := after()
			_ = unstructured.SetNestedField(u.Object, "byoh://other/host", "spec", "providerID")
			return u
		}, false, "not a recognized in-place transition"},
		{"a finalizer is removed too", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured {
			u := after()
			u.SetFinalizers(nil)
			return u
		}, false, "not a recognized in-place transition"},
		{"an owner reference is added too", []client.Object{source(), target(), movedMachine(), grant()}, before(), func() *unstructured.Unstructured {
			u := after()
			u.SetOwnerReferences(append(u.GetOwnerReferences(), metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "x", UID: "x"}))
			return u
		}, false, "not a recognized in-place transition"},
		{"the controller reference is repointed to another moved Machine", []client.Object{source(), target(), movedMachine(), func() *clusterv1.Machine {
			m := movedMachine()
			m.Name, m.UID = "md-0-frlf2-other", "other-machine-uid"
			return m
		}(), grant()}, before(), func() *unstructured.Unstructured {
			u := after()
			refs := u.GetOwnerReferences()
			refs[0].Name, refs[0].UID = "md-0-frlf2-other", "other-machine-uid"
			u.SetOwnerReferences(refs)
			return u
		}, false, "not a recognized in-place transition"},

		// the move: Machine, target and source MachineSet, MachineDeployment
		{"the Machine was not moved (still in the source MachineSet)", []client.Object{source(), target(), func() *clusterv1.Machine {
			m := movedMachine()
			m.OwnerReferences[0].Name, m.OwnerReferences[0].UID = moveSource, moveSourceUID
			m.Labels[machineSetNameLabel], m.Labels[machineTemplateHashLabel] = moveSource, moveSrcHash
			return m
		}(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"the Machine does not carry the labels of the target yet", []client.Object{source(), target(), func() *clusterv1.Machine {
			m := movedMachine()
			m.Labels[machineSetNameLabel], m.Labels[machineTemplateHashLabel] = moveSource, moveSrcHash
			return m
		}(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"a replaced Machine of the same name (another UID)", []client.Object{source(), target(), func() *clusterv1.Machine {
			m := movedMachine()
			m.UID = "replacement-uid"
			return m
		}(), grant()}, before(), after, false, "could not resolve upgrade target"},
		{"the owner Machine is gone", []client.Object{source(), target(), grant()}, before(), after, false, "could not resolve upgrade target"},
		{"the Machine is being deleted", []client.Object{source(), target(), func() *clusterv1.Machine {
			m := movedMachine()
			m.DeletionTimestamp = &metav1.Time{Time: grantNow}
			m.Finalizers = []string{"machine.cluster.x-k8s.io"}
			return m
		}(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"the Machine has another InfraMachine", []client.Object{source(), target(), func() *clusterv1.Machine {
			m := movedMachine()
			m.Spec.InfrastructureRef.Name = "another-byomachine"
			return m
		}(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"the owner reference of the Machine is not a controller reference", []client.Object{source(), target(), func() *clusterv1.Machine {
			m := movedMachine()
			m.OwnerReferences[0].Controller = nil
			return m
		}(), grant()}, before(), after, false, ""},
		{"the target MachineSet is being deleted", []client.Object{source(), deleting(target()), movedMachine(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"the source MachineSet is being deleted", []client.Object{deleting(source()), target(), movedMachine(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"the source MachineSet has no MachineDeployment", []client.Object{func() *clusterv1.MachineSet {
			ms := source()
			ms.OwnerReferences = nil
			return ms
		}(), target(), movedMachine(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"a recreated target MachineSet (another UID)", []client.Object{source(), moveMachineSet(moveTarget, "recreated-uid", moveTgtHash, moveMDUID), movedMachine(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"the source MachineSet is gone", []client.Object{target(), movedMachine(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"the source MachineSet belongs to another MachineDeployment", []client.Object{moveMachineSet(moveSource, moveSourceUID, moveSrcHash, "other-md-uid"), target(), movedMachine(), grant()}, before(), after, false, "not a recognized in-place transition"},
		{"the target MachineSet belongs to another MachineDeployment", []client.Object{source(), moveMachineSet(moveTarget, moveTargetUID, moveTgtHash, "other-md-uid"), movedMachine(), grant(), moveGrant(TargetMachineDeployment, "other-md-uid", future, "claim", mdTransitions)}, before(), after, false, "not a recognized in-place transition"},
		{"a stale request (source label is not the old MachineSet)", []client.Object{source(), target(), movedMachine(), grant()}, moveByoMachine("md-0-gone", "0-gone"), after, false, "not a recognized in-place transition"},
		{"back to the source MachineSet", []client.Object{source(), target(), movedMachine(), grant()}, after(), before, false, "not a recognized in-place transition"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := guardMove(moveWorld(t, tc.world...), tc.oldU, tc.newU(), false)
			if d.Allowed != tc.allow {
				t.Fatalf("allowed=%v want %v (reason %q)", d.Allowed, tc.allow, d.Reason)
			}
			if !tc.allow && !strings.Contains(d.Reason, tc.reason) {
				t.Fatalf("reason %q does not contain %q", d.Reason, tc.reason)
			}
		})
	}
}

// The shape seen before Cluster API marks the InfraMachine: no update-in-progress annotation yet (syncMachines runs
// before triggerInPlaceUpdate). A dry-run of the re-alignment persists nothing and needs no claim, as every dry-run.
func TestByoMachineSetLabelsWithoutToggleAndDryRun(t *testing.T) {
	future := grantNow.Add(time.Hour)
	plain := func(setName, hash string) *unstructured.Unstructured {
		u := moveByoMachine(setName, hash)
		u.SetAnnotations(map[string]string{"cluster.x-k8s.io/cloned-from-name": "md-0"})
		return u
	}
	world := []client.Object{
		moveMachineSet(moveSource, moveSourceUID, moveSrcHash, moveMDUID), moveMachineSet(moveTarget, moveTargetUID, moveTgtHash, moveMDUID),
		movedMachine(), moveGrant(TargetMachineDeployment, moveMDUID, future, "claim", mdTransitions),
	}
	if d := guardMove(moveWorld(t, world...), plain(moveSource, moveSrcHash), plain(moveTarget, moveTgtHash), false); !d.Allowed {
		t.Fatalf("without the toggle: %s", d.Reason)
	}
	if d := guardMove(moveWorld(t, world...), plain(moveSource, moveSrcHash), plain(moveTarget, moveTgtHash), true); !d.Allowed {
		t.Fatalf("dry-run: %s", d.Reason)
	}
	// a persisted re-alignment to forged values stays denied, with and without the toggle on both sides
	if d := guardMove(moveWorld(t, world...), plain(moveSource, moveSrcHash), plain("md-0-forged", "forged"), false); d.Allowed {
		t.Fatal("forged values were allowed")
	}
}

// A control-plane Machine has no MachineSet: its ByoMachine never gets this exception, also not under a KCP grant.
func TestByoMachineSetLabelsNotForControlPlane(t *testing.T) {
	future := grantNow.Add(time.Hour)
	m := movedMachine()
	m.OwnerReferences[0] = metav1.OwnerReference{APIVersion: "controlplane.cluster.x-k8s.io/v1beta2", Kind: "KubeadmControlPlane", Name: "kcp", UID: "kcp-uid", Controller: ptr.To(true)}
	c := moveWorld(t, m, moveGrant(TargetKubeadmControlPlane, "kcp-uid", future, "claim", "KCP-1,KCP-2,KCP-3,KCP-C1,KCP-C2,KCP-IM,KCP-IM-C"))
	if d := guardMove(c, moveByoMachine(moveSource, moveSrcHash), moveByoMachine(moveTarget, moveTgtHash), false); d.Allowed || !strings.Contains(d.Reason, "not a recognized in-place transition") {
		t.Fatalf("allowed=%v reason %q", d.Allowed, d.Reason)
	}
	// the pure decision: even with the flag set, a KCP grant does not authorize it
	q := GrantQuery{ClusterUID: "cluster-uid-1", TargetKind: TargetKubeadmControlPlane, TargetUID: "kcp-uid-1", Now: time.Now()}
	if d := EvaluateByoMachineInPlace(protectedScope(), ByoMachineChange{SetLabelsFollowMove: true}, claimedKCPGrant(t), &q); d.Allowed {
		t.Fatal("a KCP grant authorized the MachineSet label re-alignment")
	}
}

// The existing shapes keep their behavior: the toggle is still needed for them, and the flag adds nothing to a
// change of the spec, the owner references or the finalizers.
func TestByoMachineSetLabelsDoNotWidenOtherShapes(t *testing.T) {
	md := mdGrantWithAck(t)
	q := msAckQuery()
	for name, c := range map[string]ByoMachineChange{
		"spec":       {SetLabelsFollowMove: true, SpecChanged: true},
		"owner refs": {SetLabelsFollowMove: true, OwnerRefsChanged: true},
		"finalizers": {SetLabelsFollowMove: true, FinalizersChanged: true},
		"identity":   {SetLabelsFollowMove: true, IdentityChanged: true},
		"no flag":    {MetadataChanged: true},
	} {
		if d := EvaluateByoMachineInPlace(protectedScope(), c, md, &q); d.Allowed {
			t.Fatalf("%s: allowed", name)
		}
	}
	if d := EvaluateByoMachineInPlace(protectedScope(), ByoMachineChange{SetLabelsFollowMove: true, MetadataChanged: true}, md, &q); !d.Allowed {
		t.Fatalf("the re-alignment under the MD grant: %s", d.Reason)
	}
	if d := EvaluateByoMachineInPlace(protectedScope(), ByoMachineChange{SetLabelsFollowMove: true, MetadataChanged: true}, nil, &q); d.Allowed {
		t.Fatal("allowed without a grant")
	}
}
