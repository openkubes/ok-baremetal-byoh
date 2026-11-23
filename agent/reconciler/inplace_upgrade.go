// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"time"

	"github.com/openkubes/ok-baremetal-byoh/agent/inplaceupgrade"
	infrastructurev1beta1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1beta1conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// acceptedRequeue runs an accepted upgrade right after its Running status is
// stored.
const acceptedRequeue = time.Second

// reconcileInPlaceUpgrade runs the in-place upgrade in spec.inPlaceUpgrade.
//
// It first reports the attempt as Running, so that the upgrade has a durable
// owner before the host changes, and runs it in the next reconcile. A Running
// attempt runs again after a restart of the agent and resumes from its
// checkpoint. Succeeded and Failed are final for the attempt: the agent never
// retries a failed upgrade by itself, an administrator raises
// spec.inPlaceUpgrade.attempt after a forward recovery.
func (r *HostReconciler) reconcileInPlaceUpgrade(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) (ctrl.Result, error) {
	req := byoHost.Spec.InPlaceUpgrade
	logger := ctrl.LoggerFrom(ctx).WithValues("ByoHost", byoHost.Name, "upgrade", req.ID, "attempt", req.Attempt, "toVersion", req.ToVersion)
	status := byoHost.Status.InPlaceUpgrade
	if status.Matches(req) && status.Phase != infrastructurev1beta1.InPlaceUpgradeRunning {
		logger.Info("In-place upgrade attempt already finished", "phase", status.Phase)
		return ctrl.Result{}, nil
	}
	if !status.Matches(req) {
		now := metav1.Now()
		byoHost.Status.InPlaceUpgrade = &infrastructurev1beta1.InPlaceUpgradeStatus{
			ID: req.ID, Attempt: req.Attempt, Phase: infrastructurev1beta1.InPlaceUpgradeRunning, StartedAt: &now,
		}
		if err := r.checkInPlaceUpgrade(byoHost); err != nil {
			r.finishInPlaceUpgrade(byoHost, inplaceupgrade.Result{}, &inplaceupgrade.StepError{Step: inplaceupgrade.StepPreflight, Err: err})
			return ctrl.Result{}, nil
		}
		logger.Info("In-place upgrade accepted")
		return ctrl.Result{RequeueAfter: acceptedRequeue}, nil
	}
	if err := r.checkInPlaceUpgrade(byoHost); err != nil {
		r.finishInPlaceUpgrade(byoHost, inplaceupgrade.Result{}, &inplaceupgrade.StepError{Step: inplaceupgrade.StepPreflight, Err: err})
		return ctrl.Result{}, nil
	}

	logger.Info("Running in-place upgrade", "fromStep", status.Step)
	result, err := r.Upgrader.Execute(ctx, req, func(step inplaceupgrade.Step) {
		r.reportInPlaceUpgradeStep(ctx, byoHost, step)
	})
	r.finishInPlaceUpgrade(byoHost, result, err)
	if err != nil {
		logger.Error(err, "In-place upgrade failed")
	} else {
		logger.Info("In-place upgrade succeeded", "kubelet", result.KubeletVersion, "node", result.NodeVersion)
	}
	return ctrl.Result{}, nil
}

// checkInPlaceUpgrade checks that the agent can upgrade the host for the
// request: the host is bootstrapped, and the request is for its attachment.
func (r *HostReconciler) checkInPlaceUpgrade(byoHost *infrastructurev1beta1.ByoHost) error {
	switch {
	case r.Upgrader == nil:
		return errors.New("this agent does not run in-place upgrades")
	case byoHost.Spec.InPlaceUpgrade.ByoMachineUID != byoHost.Spec.ByoMachineRef.UID:
		return errors.New("the upgrade is for another ByoMachine than spec.byoMachineRef")
	case !v1beta1conditions.IsTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded):
		return errors.New("the node of the host is not bootstrapped")
	}
	return nil
}

// reportInPlaceUpgradeStep patches the step of a running upgrade into the
// status right away. A failed report only delays the progress in the status.
func (r *HostReconciler) reportInPlaceUpgradeStep(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost, step inplaceupgrade.Step) {
	before := byoHost.DeepCopy()
	byoHost.Status.InPlaceUpgrade.Step = string(step)
	if err := r.Client.Status().Patch(ctx, byoHost, client.MergeFrom(before)); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to report the in-place upgrade step", "step", step)
		byoHost.ResourceVersion = before.ResourceVersion
	}
}

// finishInPlaceUpgrade reports the end of the attempt. It reports Succeeded
// only without an error, with the versions that the executor observed.
func (r *HostReconciler) finishInPlaceUpgrade(byoHost *infrastructurev1beta1.ByoHost, result inplaceupgrade.Result, err error) {
	status := byoHost.Status.InPlaceUpgrade
	now := metav1.Now()
	status.FinishedAt = &now
	status.ObservedKubeletVersion = result.KubeletVersion
	status.ObservedNodeVersion = result.NodeVersion
	if err != nil {
		status.Phase = infrastructurev1beta1.InPlaceUpgradeFailed
		status.Message = err.Error()
		var stepErr *inplaceupgrade.StepError
		if errors.As(err, &stepErr) {
			status.Step = string(stepErr.Step)
		}
		r.Recorder.Eventf(byoHost, nil, corev1.EventTypeWarning, "InPlaceUpgradeFailed", "InPlaceUpgrade", "in-place upgrade %s to %s failed: %v", status.ID, byoHost.Spec.InPlaceUpgrade.ToVersion, err)
		return
	}
	status.Phase = infrastructurev1beta1.InPlaceUpgradeSucceeded
	status.Step = string(inplaceupgrade.StepVerify)
	status.Message = ""
	r.Recorder.Eventf(byoHost, nil, corev1.EventTypeNormal, "InPlaceUpgradeSucceeded", "InPlaceUpgrade", "in-place upgrade %s to %s verified", status.ID, byoHost.Spec.InPlaceUpgrade.ToVersion)
}
