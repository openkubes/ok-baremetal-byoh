// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

// ClusterIdentity represents a cluster discovered from a trusted
// Kubernetes API inventory and its independently verified policy.
//
// Protected must not come from an incoming Machine's metadata.
type ClusterIdentity struct {
	Name      string
	UID       string
	Protected bool
}

// NamespaceInventory is a complete, trusted snapshot of CAPI
// Cluster identities and their protection state in one namespace.
//
// Verified must be true only when:
// - the inventory is complete;
// - each protected policy is bound to the exact Cluster UID;
// - all protection-state lookups succeeded.
//
// A missing or stale inventory must not be marked Verified.
type NamespaceInventory struct {
	Verified bool
	Clusters []ClusterIdentity
}

// ResolveScope uses namespace-wide protection to prevent new
// Machines from bypassing admission through forged or missing
// cluster-name labels or owner references.
//
// This deliberately requires namespace isolation for protected
// BYOH clusters. It does not inspect untrusted Machine metadata.
//
// The caller is responsible for validating the namespace and
// authenticating the inventory source.
func ResolveScope(inventory NamespaceInventory) Scope {
	if !inventory.Verified {
		return Scope{}
	}

	seenNames := make(map[string]string)
	seenUIDs := make(map[string]bool)
	protected := false

	for _, cluster := range inventory.Clusters {
		if cluster.Name == "" || cluster.UID == "" {
			return Scope{}
		}

		if uid, exists := seenNames[cluster.Name]; exists {
			// Duplicate entries make the snapshot ambiguous.
			_ = uid
			return Scope{}
		}

		if seenUIDs[cluster.UID] {
			return Scope{}
		}

		seenNames[cluster.Name] = cluster.UID
		seenUIDs[cluster.UID] = true

		if cluster.Protected {
			protected = true
		}
	}

	return Scope{
		Verified:  true,
		Protected: protected,
	}
}
