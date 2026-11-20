package noreplacement

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	clusterNameLabel     = "cluster.x-k8s.io/cluster-name"
	protectionAnnotation = "safety.openkubes.org/in-place-only"
)

// reservedMetadataKey identifies metadata owned by CAPI,
// infrastructure providers, or the OpenKubes safety boundary.
func reservedMetadataKey(key string) bool {
	domain, _, qualified := strings.Cut(key, "/")
	if !qualified {
		return false
	}

	return domain == "cluster.x-k8s.io" ||
		strings.HasSuffix(domain, ".cluster.x-k8s.io") ||
		domain == "safety.openkubes.org"
}

func reservedMetadataChanged(
	oldValues, newValues map[string]string,
) bool {
	for key, oldValue := range oldValues {
		if !reservedMetadataKey(key) {
			continue
		}
		newValue, exists := newValues[key]
		if !exists || newValue != oldValue {
			return true
		}
	}

	for key, newValue := range newValues {
		if !reservedMetadataKey(key) {
			continue
		}
		oldValue, exists := oldValues[key]
		if !exists || oldValue != newValue {
			return true
		}
	}

	return false
}

// EvaluateMachineUpdate is a conservative pre-deployment policy.
// Legitimate CAPI finalizer and metadata mutations need explicit
// exceptions before enabling this guard in a real cluster.
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

	if !equality.Semantic.DeepEqual(
		oldMachine.Finalizers,
		newMachine.Finalizers,
	) {
		return deny("Machine finalizers must remain unchanged")
	}

	oldCluster, oldClusterExists := oldMachine.Labels[clusterNameLabel]
	newCluster, newClusterExists := newMachine.Labels[clusterNameLabel]
	if oldClusterExists != newClusterExists ||
		oldCluster != newCluster {
		return deny("Machine cluster identity label must remain unchanged")
	}

	oldProtection, oldProtectionExists := oldMachine.Annotations[protectionAnnotation]
	newProtection, newProtectionExists := newMachine.Annotations[protectionAnnotation]
	if oldProtectionExists != newProtectionExists ||
		oldProtection != newProtection {
		return deny("Machine protection annotation must remain unchanged")
	}

	if reservedMetadataChanged(
		oldMachine.Labels,
		newMachine.Labels,
	) {
		return deny("Machine reserved labels must remain unchanged")
	}

	if reservedMetadataChanged(
		oldMachine.Annotations,
		newMachine.Annotations,
	) {
		return deny("Machine reserved annotations must remain unchanged")
	}

	return allow()
}
