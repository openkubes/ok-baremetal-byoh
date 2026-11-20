package noreplacement

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubernetesClusterLookup returns a ClusterLookup backed by
// Kubernetes API reads.
//
// Production callers MUST pass mgr.GetAPIReader(), not the
// potentially stale manager cache client.
//
// No watch, mutation, or implicit fallback is performed.
func KubernetesClusterLookup(
	reader client.Reader,
) ClusterLookup {
	return func(
		ctx context.Context,
		namespace string,
		name string,
	) (string, error) {
		if reader == nil {
			return "", errors.New(
				"Kubernetes Cluster reader is unavailable",
			)
		}

		if namespace == "" || name == "" {
			return "", errors.New(
				"Cluster namespace and name are required",
			)
		}

		cluster := &clusterv1.Cluster{}

		err := reader.Get(
			ctx,
			types.NamespacedName{
				Namespace: namespace,
				Name:      name,
			},
			cluster,
		)
		if err != nil {
			return "", err
		}

		if cluster.UID == "" {
			return "", errors.New(
				"CAPI Cluster has no UID",
			)
		}

		return string(cluster.UID), nil
	}
}
