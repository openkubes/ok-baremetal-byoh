// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	b64 "encoding/base64"
	"time"

	infrastructurev1beta1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	"github.com/openkubes/ok-baremetal-byoh/common/bootstraptoken"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	v1beta1patch "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// BootstrapKubeconfigReconciler reconciles a BootstrapKubeconfig object
type BootstrapKubeconfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	// ttl is the time to live for the generated bootstrap token
	ttl                     = time.Minute * 30
	bootstrapTokenFinalizer = "infrastructure.cluster.x-k8s.io/bootstrap-token-cleanup"
)

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BootstrapKubeconfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received")

	// Fetch the BootstrapKubeconfig instance
	bootstrapKubeconfig := &infrastructurev1beta1.BootstrapKubeconfig{}
	err := r.Client.Get(ctx, req.NamespacedName, bootstrapKubeconfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !bootstrapKubeconfig.DeletionTimestamp.IsZero() {
		// Always attempt revocation, even on older objects with no finalizer.
		// For new requests the finalizer is added before issuing any token.
		if revokeErr := revokeBootstrapTokenSecret(ctx, r.Client, bootstrapKubeconfig); revokeErr != nil {
			return ctrl.Result{}, revokeErr
		}
		if controllerutil.ContainsFinalizer(bootstrapKubeconfig, bootstrapTokenFinalizer) {
			original := bootstrapKubeconfig.DeepCopy()
			controllerutil.RemoveFinalizer(bootstrapKubeconfig, bootstrapTokenFinalizer)
			return ctrl.Result{}, r.Client.Patch(ctx, bootstrapKubeconfig, client.MergeFrom(original))
		}
		return ctrl.Result{}, nil
	}

	// Persist the finalizer BEFORE creating any token Secret. A crash after
	// issuance must not leave a deletable request and a live credential.
	if !controllerutil.ContainsFinalizer(bootstrapKubeconfig, bootstrapTokenFinalizer) {
		original := bootstrapKubeconfig.DeepCopy()
		controllerutil.AddFinalizer(bootstrapKubeconfig, bootstrapTokenFinalizer)
		return ctrl.Result{}, r.Client.Patch(ctx, bootstrapKubeconfig, client.MergeFrom(original))
	}

	// There already is bootstrap-kubeconfig data associated with this object.
	if bootstrapKubeconfig.Status.BootstrapKubeconfigData != nil {
		return ctrl.Result{}, nil
	}

	// An earlier reconciliation may already have created the Secret but failed
	// to persist the status. Reuse that exact token instead of issuing another.
	tokenStr, err := ensureBootstrapTokenSecret(ctx, r.Client, bootstrapKubeconfig, ttl)
	if err != nil {
		return ctrl.Result{}, err
	}

	bootstrapKubeconfigData, err := bootstraptoken.GenerateBootstrapKubeconfigFromBootstrapToken(tokenStr, bootstrapKubeconfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	caData := bootstrapKubeconfigData.Clusters[infrastructurev1beta1.DefaultClusterName].CertificateAuthorityData
	decodedCAData, err := b64.StdEncoding.DecodeString(string(caData))
	if err != nil {
		return ctrl.Result{}, err
	}

	bootstrapKubeconfigData.Clusters[infrastructurev1beta1.DefaultClusterName].CertificateAuthorityData = decodedCAData
	runtimeEncodedBootstrapKubeConfig, err := runtime.Encode(clientcmdlatest.Codec, bootstrapKubeconfigData)
	if err != nil {
		return ctrl.Result{}, err
	}

	helper, err := v1beta1patch.NewHelper(bootstrapKubeconfig, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	bootstrapKubeconfigDataStr := string(runtimeEncodedBootstrapKubeConfig)
	bootstrapKubeconfig.Status.BootstrapKubeconfigData = &bootstrapKubeconfigDataStr

	return ctrl.Result{}, helper.Patch(ctx, bootstrapKubeconfig)
}

// SetupWithManager sets up the controller with the Manager.
func (r *BootstrapKubeconfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.BootstrapKubeconfig{}).
		Complete(r)
}
