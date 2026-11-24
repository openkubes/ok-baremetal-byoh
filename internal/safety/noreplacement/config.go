// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ProtectedClusterRegistration is a deliberately positive-only
// protection declaration. There is no unprotect flag.
type ProtectedClusterRegistration struct {
	Namespace   string `json:"namespace"`
	ClusterName string `json:"clusterName"`
	ClusterUID  string `json:"clusterUID"`
}

type registryDocument struct {
	Registrations []ProtectedClusterRegistration `json:"registrations"`
}

// ParseProtectedRegistry validates a trusted registry document.
//
// The caller must load this configuration from a trusted source.
// This function does not authorize who may change that source.
//
// An empty registry protects nothing and verifies nothing: every guarded
// request in a namespace that carries the routing label then fails closed.
//
// The returned map is owned by the caller and must not be mutated
// after it becomes active in an admission handler.
func ParseProtectedRegistry(
	data []byte,
) (map[string]NamespaceRegistration, error) {

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var document registryDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode registry: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("registry contains trailing data")
	}

	// An explicitly empty list is a valid registry: a management cluster may
	// protect no cluster at all. A document without the list (or with null)
	// is not, so that a truncated or wrong file never passes as "empty".
	if document.Registrations == nil {
		return nil, fmt.Errorf("registry must declare its registrations (use [] for none)")
	}

	result := make(map[string]NamespaceRegistration)

	for _, entry := range document.Registrations {
		if errs := validation.IsDNS1123Label(entry.Namespace); len(errs) > 0 {
			return nil, fmt.Errorf(
				"invalid namespace %q: %s",
				entry.Namespace,
				strings.Join(errs, ", "),
			)
		}

		if errs := validation.IsDNS1123Subdomain(entry.ClusterName); len(errs) > 0 {
			return nil, fmt.Errorf(
				"invalid cluster name %q: %s",
				entry.ClusterName,
				strings.Join(errs, ", "),
			)
		}

		if entry.ClusterUID == "" ||
			strings.TrimSpace(entry.ClusterUID) != entry.ClusterUID ||
			strings.ContainsAny(entry.ClusterUID, " \t\r\n") {
			return nil, fmt.Errorf(
				"invalid cluster UID in namespace %q",
				entry.Namespace,
			)
		}

		if _, exists := result[entry.Namespace]; exists {
			return nil, fmt.Errorf(
				"duplicate namespace registration %q",
				entry.Namespace,
			)
		}

		result[entry.Namespace] = NamespaceRegistration{
			Protected:   true,
			ClusterName: entry.ClusterName,
			ClusterUID:  entry.ClusterUID,
		}
	}

	return result, nil
}
