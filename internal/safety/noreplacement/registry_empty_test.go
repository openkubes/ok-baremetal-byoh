// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	noreplacement "github.com/openkubes/ok-baremetal-byoh/internal/safety/noreplacement"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// The registries a management cluster passes through: no cluster, one, two,
// and back. "ha1" and "ha2" are namespaces with one Cluster each.
const (
	registryNone = `{"registrations": []}`
	registryHA1  = `{"registrations": [{"namespace":"ha1","clusterName":"ha1","clusterUID":"uid-ha1"}]}`
	registryBoth = `{"registrations": [
		{"namespace":"ha1","clusterName":"ha1","clusterUID":"uid-ha1"},
		{"namespace":"ha2","clusterName":"ha2","clusterUID":"uid-ha2"}]}`
	registryHA2 = `{"registrations": [{"namespace":"ha2","clusterName":"ha2","clusterUID":"uid-ha2"}]}`
)

// liveClusters answers the Cluster lookup like the API server would.
func liveClusters(_ context.Context, namespace, name string) (string, error) {
	uids := map[string]string{"ha1/ha1": "uid-ha1", "ha2/ha2": "uid-ha2"}
	if uid, ok := uids[namespace+"/"+name]; ok {
		return uid, nil
	}
	return "", errors.New("not found")
}

func mustParse(t *testing.T, document string) map[string]noreplacement.NamespaceRegistration {
	t.Helper()
	regs, err := noreplacement.ParseProtectedRegistry([]byte(document))
	if err != nil {
		t.Fatalf("registry rejected: %v", err)
	}
	if regs == nil {
		t.Fatal("a valid registry yields a map, also when it is empty")
	}
	return regs
}

func TestEmptyRegistryIsValidAndProtectsNothing(t *testing.T) {
	regs := mustParse(t, registryNone)
	if len(regs) != 0 {
		t.Fatalf("an empty registry has no registrations, got %d", len(regs))
	}
	for _, namespace := range []string{"ha1", "ha2", "default"} {
		scope := noreplacement.ResolveRegisteredScope(context.Background(), namespace, regs, liveClusters)
		if scope.Verified || scope.Protected {
			t.Fatalf("namespace %s: an empty registry must leave every scope unverified, got %+v", namespace, scope)
		}
	}
}

// machineRequest is the request the webhook receives for a namespace that
// carries the routing label (the webhook selects only such namespaces).
func machineRequest(t *testing.T, op admissionv1.Operation, namespace string) admission.Request {
	t.Helper()
	machine := &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{
		Name: "m-0", Namespace: namespace, UID: "machine-uid",
		Labels: map[string]string{clusterv1.ClusterNameLabel: namespace},
	}}
	data, err := json.Marshal(machine)
	if err != nil {
		t.Fatal(err)
	}
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: op, Namespace: namespace, Name: machine.Name,
		Resource: metav1.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta2", Resource: "machines"},
	}}
	if op != admissionv1.Create {
		req.OldObject = runtime.RawExtension{Raw: data}
	}
	if op != admissionv1.Delete {
		req.Object = runtime.RawExtension{Raw: data}
	}
	return req
}

func namespaceRequest(t *testing.T, op admissionv1.Operation, name, oldLabel, newLabel string) admission.Request {
	t.Helper()
	raw := func(label string) runtime.RawExtension {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if label != "" {
			ns.Labels = map[string]string{noreplacement.ProtectedNamespaceLabel: label}
		}
		data, err := json.Marshal(ns)
		if err != nil {
			t.Fatal(err)
		}
		return runtime.RawExtension{Raw: data}
	}
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Operation: op, Name: name,
		Resource: metav1.GroupVersionResource{Version: "v1", Resource: "namespaces"},
	}}
	if op != admissionv1.Create {
		req.OldObject = raw(oldLabel)
	}
	if op != admissionv1.Delete {
		req.Object = raw(newLabel)
	}
	return req
}

// TestRegistryTransitions walks the registry through 0 -> 1 -> 2 -> 1 -> 0
// registrations, as a guard restart with each document would see it, and
// checks what the Machine and the Namespace webhooks decide for a labeled
// namespace in every state. The invariant: a labeled namespace is never
// open. It is either protected (registered) or failing closed (not
// registered); only its label can be taken away once it is not registered.
//
//nolint:funlen // One table over all registry states keeps the fail-closed invariant readable.
func TestRegistryTransitions(t *testing.T) {
	for _, state := range []struct {
		name, document string
		registered     map[string]bool
	}{
		{"0: no cluster", registryNone, map[string]bool{"ha1": false, "ha2": false}},
		{"0->1: ha1 registered", registryHA1, map[string]bool{"ha1": true, "ha2": false}},
		{"1->2: ha2 added", registryBoth, map[string]bool{"ha1": true, "ha2": true}},
		{"2->1: ha1 removed, ha2 stays", registryHA2, map[string]bool{"ha1": false, "ha2": true}},
		{"1->0: last cluster removed", registryNone, map[string]bool{"ha1": false, "ha2": false}},
	} {
		t.Run(state.name, func(t *testing.T) {
			regs := mustParse(t, state.document)
			machines := noreplacement.NewMachineAdmissionHandler(regs, liveClusters)
			namespaces := noreplacement.NewNamespaceAdmissionHandler(regs)

			for namespace, registered := range state.registered {
				scope := noreplacement.ResolveRegisteredScope(context.Background(), namespace, regs, liveClusters)
				if scope.Verified != registered || scope.Protected != registered {
					t.Fatalf("%s: scope %+v, registered=%v", namespace, scope, registered)
				}

				// Machine lifecycle in a labeled namespace: replacement is
				// never admitted, whether the namespace is registered or not.
				for _, op := range []admissionv1.Operation{admissionv1.Create, admissionv1.Delete} {
					if machines.Handle(context.Background(), machineRequest(t, op, namespace)).Allowed {
						t.Fatalf("%s: Machine %s admitted (registered=%v)", namespace, op, registered)
					}
				}
				// An unregistered labeled namespace fails closed for every
				// operation; a registered one admits the harmless update.
				update := machines.Handle(context.Background(), machineRequest(t, admissionv1.Update, namespace))
				if update.Allowed != registered {
					t.Fatalf("%s: Machine UPDATE allowed=%v, registered=%v", namespace, update.Allowed, registered)
				}

				// Namespace policy: the label of a registered namespace is
				// kept and the namespace is not deleted; the label is set
				// only on a registered namespace.
				removeLabel := namespaces.Handle(context.Background(), namespaceRequest(t, admissionv1.Update, namespace, "true", ""))
				if removeLabel.Allowed == registered {
					t.Fatalf("%s: label removal allowed=%v, registered=%v", namespace, removeLabel.Allowed, registered)
				}
				deleteNS := namespaces.Handle(context.Background(), namespaceRequest(t, admissionv1.Delete, namespace, "true", ""))
				if deleteNS.Allowed == registered {
					t.Fatalf("%s: namespace DELETE allowed=%v, registered=%v", namespace, deleteNS.Allowed, registered)
				}
				setLabel := namespaces.Handle(context.Background(), namespaceRequest(t, admissionv1.Update, namespace, "", "true"))
				if setLabel.Allowed != registered {
					t.Fatalf("%s: setting the label allowed=%v, registered=%v", namespace, setLabel.Allowed, registered)
				}
			}
		})
	}
}

// A registration whose Cluster no longer exists (or has another UID) is not
// a reason to open the namespace: with one, two or no other registrations the
// scope stays unverified.
func TestRegistrationOfAnotherClusterDoesNotVerifyANamespace(t *testing.T) {
	regs := mustParse(t, `{"registrations": [
		{"namespace":"ha1","clusterName":"ha1","clusterUID":"uid-of-a-deleted-ha1"},
		{"namespace":"ha2","clusterName":"ha2","clusterUID":"uid-ha2"}]}`)
	if scope := noreplacement.ResolveRegisteredScope(context.Background(), "ha1", regs, liveClusters); scope.Verified {
		t.Fatalf("a Cluster with another UID verified the scope: %+v", scope)
	}
	if scope := noreplacement.ResolveRegisteredScope(context.Background(), "ha2", regs, liveClusters); !scope.Verified || !scope.Protected {
		t.Fatalf("the other registration lost its protection: %+v", scope)
	}
}
