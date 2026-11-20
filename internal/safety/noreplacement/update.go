package noreplacement

import (
	"k8s.io/apimachinery/pkg/api/equality"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	clusterNameLabel     = "cluster.x-k8s.io/cluster-name"
	protectionAnnotation = "safety.openkubes.org/in-place-only"
)

// EvaluateMachineUpdate is a conservative initial UPDATE policy.
//
// It is not suitable for production deployment until required
// CAPI spec mutations and explicit upgrade authorizations are
// modeled and tested.
//
// Status subresource operations must be handled independently
// by the admission adapter.
func EvaluateMachineUpdate(
	scope Scope,
	oldMachine *clusterv1.Machine,
	newMachine *clusterv1.Machine,
) Decision {
	if !scope.Verified {
		return deny("BYOH protection scope could not be verified")
	}

	if !scope.Protected {
		return allow()
	}

	if oldMachine == nil || newMachine == nil {
		return deny("Machine UPDATE objects are required")
	}

	if oldMachine.Name != newMachine.Name ||
		oldMachine.Namespace != newMachine.Namespace ||
		oldMachine.UID != newMachine.UID {
		return deny("Machine identity must remain unchanged")
	}

	if !equality.Semantic.DeepEqual(
		oldMachine.Spec,
		newMachine.Spec,
	) {
		return deny("Machine spec changes require lifecycle authorization")
	}

	if !equality.Semantic.DeepEqual(
		oldMachine.OwnerReferences,
		newMachine.OwnerReferences,
	) {
		return deny("Machine owner references must remain unchanged")
	}

	if oldMachine.Labels[clusterNameLabel] !=
		newMachine.Labels[clusterNameLabel] {
		return deny("Machine cluster identity label must remain unchanged")
	}

	if oldMachine.Annotations[protectionAnnotation] !=
		newMachine.Annotations[protectionAnnotation] {
		return deny("Machine protection annotation must remain unchanged")
	}

	// Metadata not explicitly protected above may change.
	// This does not authorize spec mutations.
	return allow()
}
