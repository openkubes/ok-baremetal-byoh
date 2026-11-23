// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

// Admission webhook paths. These are the single source of truth shared by the
// standalone guard server (cmd/no-replacement-guard) and the ValidatingWebhook
// Configuration manifests under config/webhook/no-replacement/. The YAML paths
// MUST match these exactly.
//
// ProtectedNamespaceLabel is the label a namespace MUST carry for the webhook
// to route its objects to the guard. Unlabeled namespaces are never intercepted
// (webhook namespaceSelector), which keeps unprotected clusters/namespaces
// unaffected. Protected namespaces must ALSO be present in the trusted registry
// (/etc/openkubes/no-replacement/registry.json) that binds them to a Cluster UID;
// the label is only the routing filter, the registry is the authority.
const (
	// MachineAdmissionPath guards CAPI core Machines (ACTIVE: handler exists).
	MachineAdmissionPath = "/validate-cluster-x-k8s-io-v1beta2-machine-no-replacement"

	// MachineSetAdmissionPath guards the MD-ACK transition on the approved
	// target MachineSet only (PENDING: handler lands in M6).
	MachineSetAdmissionPath = "/validate-cluster-x-k8s-io-v1beta2-machineset-no-replacement"

	// ByoMachineAdmissionPath guards the BYOH InfraMachine (PENDING: M6).
	ByoMachineAdmissionPath = "/validate-infrastructure-cluster-x-k8s-io-v1beta1-byomachine-no-replacement"

	// GrantConfigMapAdmissionPath enforces the guarded-ConfigMap field/role
	// separation on grant objects only (PENDING: handler lands in M6).
	GrantConfigMapAdmissionPath = "/validate-core-v1-configmap-upgrade-grant"

	// IntentAdmissionPath guards the intent of protected KubeadmControlPlanes
	// and MachineDeployments (spec and scale subresource).
	IntentAdmissionPath = "/validate-cluster-x-k8s-io-v1beta2-intent-no-replacement"

	// NamespaceAdmissionPath protects the routing label of the registered
	// namespaces (REVIEW Q1).
	NamespaceAdmissionPath = "/validate-core-v1-namespace-no-replacement"

	// ProtectedNamespaceLabel marks a namespace as in-place-only protected and
	// is the webhook namespaceSelector key. Value must be "true".
	ProtectedNamespaceLabel = "safety.openkubes.org/in-place-only"

	// labelValueTrue is the value of the protection and the grant labels.
	labelValueTrue = "true"
)
