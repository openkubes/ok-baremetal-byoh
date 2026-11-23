// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import "strings"

// GrantObjectNamePrefix is the naming convention for grant CAS ConfigMaps.
// The guard recognizes a grant by this name prefix IN ADDITION to the
// GrantLabel, so that removing the label cannot make a grant object evade the
// guarded-ConfigMap admission (M3 security finding — selector-evasion).
const GrantObjectNamePrefix = "okx-grant-"

// IsGrantObjectName reports whether name follows the grant naming convention.
func IsGrantObjectName(name string) bool {
	return strings.HasPrefix(name, GrantObjectNamePrefix)
}

// IsGrantObject reports whether a ConfigMap must be treated as a grant CAS object
// and therefore admitted by the guarded-ConfigMap policy. True if it is labelled
// as a grant OR follows the grant naming convention — name-based recognition
// removes reliance on the (evadable) objectSelector label for the security
// decision.
func IsGrantObject(name string, labels map[string]string) bool {
	if IsGrantObjectName(name) {
		return true
	}
	return labels[GrantLabel] == "true"
}

// grantLabelRemoved reports whether the GrantLabel was present on old and is
// absent or no longer "true" on new.
func grantLabelRemoved(oldLabels, newLabels map[string]string) bool {
	return oldLabels[GrantLabel] == "true" && newLabels[GrantLabel] != "true"
}

// EvaluateGrantConfigMapGuarded is the M6 wrapper around EvaluateGrantConfigMap
// (M2) that also enforces, on UPDATE, that the GrantLabel cannot be removed from
// a grant object (closing the objectSelector-evasion path). CREATE/DELETE are
// delegated unchanged. actor identity is established from admission userInfo.
func EvaluateGrantConfigMapGuarded(
	op Operation,
	actor GrantActor,
	oldLabels, newLabels map[string]string,
	oldData, newData map[string]string,
) Decision {
	if op == Update && grantLabelRemoved(oldLabels, newLabels) {
		return deny("the grant label must not be removed from an upgrade grant")
	}
	return EvaluateGrantConfigMap(op, actor, oldData, newData)
}
