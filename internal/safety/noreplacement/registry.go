package noreplacement

import (
	"context"
	"errors"
)

// NamespaceRegistration is supplied by a trusted configuration
// source, independently of the incoming admission request.
//
// Protected registrations bind namespace and CAPI Cluster UID.
// Unprotected registrations must be explicitly configured.
//
// The initial design requires dedicated protected namespaces.
type NamespaceRegistration struct {
	Protected   bool
	ClusterName string
	ClusterUID  string
}

// ClusterLookup reads the UID of the actual CAPI Cluster.
// The admission adapter will implement this using an uncached
// Kubernetes API reader.
type ClusterLookup func(
	context.Context,
	string,
	string,
) (string, error)

// ResolveRegisteredScope verifies the trusted registration
// against the current CAPI Cluster identity.
//
// An unregistered namespace, lookup failure, empty UID, or
// identity mismatch remains unverified and fails closed.
//
// Registrations must be kept immutable for the lifetime of
// an admission evaluation and protected from untrusted writes.
func ResolveRegisteredScope(
	ctx context.Context,
	namespace string,
	registrations map[string]NamespaceRegistration,
	lookup ClusterLookup,
) Scope {
	if namespace == "" {
		return Scope{}
	}

	registration, exists := registrations[namespace]
	if !exists {
		return Scope{}
	}

	if !registration.Protected {
		// Explicitly unprotected namespaces must not carry
		// contradictory protected-cluster identity fields.
		if registration.ClusterName != "" ||
			registration.ClusterUID != "" {
			return Scope{}
		}
		return Scope{Verified: true}
	}

	if registration.ClusterName == "" ||
		registration.ClusterUID == "" ||
		lookup == nil {
		return Scope{}
	}

	actualUID, err := lookup(
		ctx,
		namespace,
		registration.ClusterName,
	)
	if err != nil || actualUID == "" {
		return Scope{}
	}

	if actualUID != registration.ClusterUID {
		return Scope{}
	}

	return Scope{
		Verified:  true,
		Protected: true,
	}
}

// ErrClusterUnavailable is useful for test doubles.
// Production adapters return the actual API error.
var ErrClusterUnavailable = errors.New(
	"CAPI Cluster unavailable",
)
