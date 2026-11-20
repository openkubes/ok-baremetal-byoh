// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement_test

import (
	"context"
	"encoding/json"
	"testing"

	noreplacement "github.com/openkubes/ok-baremetal-byoh/internal/safety/noreplacement"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

//nolint:funlen // Exhaustive admission matrix: CREATE/UPDATE/DELETE across protected, unverified and unknown scope in one fail-closed security contract.
func TestMachineAdmissionHandler(t *testing.T) {
	const namespace = "byoh-lab"

	handler := noreplacement.NewMachineAdmissionHandler(
		map[string]noreplacement.NamespaceRegistration{
			namespace: {
				Protected:   true,
				ClusterName: "ok-byoh",
				ClusterUID:  "cluster-uid-123",
			},
		},
		func(
			_ context.Context,
			_, _ string,
		) (string, error) {

			return "cluster-uid-123", nil
		},
	)

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "controlplane-0",
			Namespace: namespace,
			UID:       types.UID("machine-uid-123"),
			Labels: map[string]string{
				"cluster.x-k8s.io/cluster-name": "ok-byoh",
			},
		},
	}

	raw := func(m *clusterv1.Machine) runtime.RawExtension {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return runtime.RawExtension{Raw: data}
	}

	makeRequest := func(
		operation admissionv1.Operation,
	) admission.Request {

		return admission.Request{
			AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: operation,
				Namespace: namespace,
				Name:      machine.Name,
				Resource: metav1.GroupVersionResource{
					Group:    "cluster.x-k8s.io",
					Version:  "v1beta2",
					Resource: "machines",
				},
				Object:    raw(machine),
				OldObject: raw(machine),
			},
		}
	}

	t.Run("deny CREATE without a protection label", func(t *testing.T) {
		req := makeRequest(admissionv1.Create)
		req.Object.Raw = []byte(`{"metadata":{"name":"replacement"}}`)

		if handler.Handle(context.Background(), req).Allowed {
			t.Fatal("replacement CREATE unexpectedly allowed")
		}
	})

	t.Run("deny DELETE", func(t *testing.T) {
		req := makeRequest(admissionv1.Delete)

		if handler.Handle(context.Background(), req).Allowed {
			t.Fatal("protected Machine DELETE unexpectedly allowed")
		}
	})

	t.Run("allow harmless UPDATE", func(t *testing.T) {
		req := makeRequest(admissionv1.Update)
		updated := machine.DeepCopy()
		updated.Annotations = map[string]string{
			"example.org/note": "safe",
		}
		req.Object = raw(updated)

		if resp := handler.Handle(context.Background(), req); !resp.Allowed {
			t.Fatalf("harmless UPDATE denied: %v", resp.Result)
		}
	})

	t.Run("deny Machine version UPDATE", func(t *testing.T) {
		req := makeRequest(admissionv1.Update)
		updated := machine.DeepCopy()
		updated.Spec.Version = "v1.34.0"
		req.Object = raw(updated)

		if handler.Handle(context.Background(), req).Allowed {
			t.Fatal("Machine version UPDATE unexpectedly allowed")
		}
	})

	t.Run("deny status subresource until reviewed", func(t *testing.T) {
		req := makeRequest(admissionv1.Update)
		req.SubResource = "status"

		if handler.Handle(context.Background(), req).Allowed {
			t.Fatal("unreviewed status UPDATE unexpectedly allowed")
		}
	})

	t.Run("deny unregistered namespace", func(t *testing.T) {
		req := makeRequest(admissionv1.Create)
		req.Namespace = "unregistered"

		if handler.Handle(context.Background(), req).Allowed {
			t.Fatal("unregistered namespace unexpectedly allowed")
		}
	})

	t.Run("deny unsupported resource", func(t *testing.T) {
		req := makeRequest(admissionv1.Create)
		req.Resource.Resource = "machinedeployments"

		if handler.Handle(context.Background(), req).Allowed {
			t.Fatal("unsupported resource unexpectedly allowed")
		}
	})

	t.Run("deny malformed UPDATE", func(t *testing.T) {
		req := makeRequest(admissionv1.Update)
		req.Object.Raw = []byte("{")

		if handler.Handle(context.Background(), req).Allowed {
			t.Fatal("malformed UPDATE unexpectedly allowed")
		}
	})

	t.Run("deny mismatched Machine identity", func(t *testing.T) {
		req := makeRequest(admissionv1.Update)
		req.Name = "other-machine"

		if handler.Handle(context.Background(), req).Allowed {
			t.Fatal("mismatched Machine identity unexpectedly allowed")
		}
	})

	t.Run("deny unknown API version", func(t *testing.T) {
		req := makeRequest(admissionv1.Create)
		req.Resource.Version = "v1beta1"

		if handler.Handle(context.Background(), req).Allowed {
			t.Fatal("unsupported API version unexpectedly allowed")
		}
	})
}
