// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package extension holds the CAPI-typed RuntimeSDK handlers for the OpenKubes
// BYOH in-place update extension. The pure decision logic lives in
// internal/inplace; this package is the thin glue to the CAPI runtime hooks.
//
// BUILD STATUS: PENDING. This file imports CAPI/controller-runtime and is built
// and integration-tested on the Mac / in CI, not in the offline sandbox.
//
// createJSONPatch is adapted from cluster-api test/extension (Apache-2.0).
package extension

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	pkgerrors "github.com/pkg/errors"
	"gomodules.xyz/jsonpatch/v2"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	"github.com/openkubes/ok-baremetal-byoh/internal/inplace"
)

// ExtensionHandlers implements CanUpdateMachine, CanUpdateMachineSet and the
// Phase-0 CAPD UpdateMachine test handler. It holds no API client: CanUpdate*
// is read-only (pure computation on the request), and UpdateMachine only fakes
// progress — no kubeadm, no host signal, no drain (production executor is out
// of Phase 0 scope).
type ExtensionHandlers struct {
	state         sync.Map // key -> time.Time (CAPD fake-update start)
	completeAfter time.Duration
}

// NewExtensionHandlers returns handlers with the default simulated completion delay.
func NewExtensionHandlers() *ExtensionHandlers {
	return &ExtensionHandlers{completeAfter: inplace.DefaultUpdateCompleteAfter}
}

func emptyJSONPatch() runtimehooksv1.Patch {
	return runtimehooksv1.Patch{PatchType: runtimehooksv1.JSONPatchType, Patch: []byte("[]")}
}

// DoCanUpdateMachine declares that this extension can update ONLY the Machine
// Kubernetes version in-place (version-only, identity-preserving). NO API writes.
func (h *ExtensionHandlers) DoCanUpdateMachine(ctx context.Context, req *runtimehooksv1.CanUpdateMachineRequest, resp *runtimehooksv1.CanUpdateMachineResponse) {
	modified := req.Current.Machine.DeepCopy()
	cur := inplace.SpecView{Version: modified.Spec.Version}
	des := inplace.SpecView{Version: req.Desired.Machine.Spec.Version}
	accepted, _ := inplace.AcceptInPlace(cur, des)
	modified.Spec.Version = accepted.Version

	marshalledCurrent, err := json.Marshal(req.Current.Machine)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = pkgerrors.Wrap(err, "marshal current Machine").Error()
		return
	}
	machinePatch, err := createJSONPatch(marshalledCurrent, modified)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = err.Error()
		return
	}
	resp.MachinePatch = runtimehooksv1.Patch{PatchType: runtimehooksv1.JSONPatchType, Patch: machinePatch}
	// We change nothing on InfraMachine / BootstrapConfig in-place; if they differ,
	// CAPI will see a remaining diff and fall back to a rollout/replacement.
	resp.InfrastructureMachinePatch = emptyJSONPatch()
	resp.BootstrapConfigPatch = emptyJSONPatch()
	resp.Status = runtimehooksv1.ResponseStatusSuccess
}

// DoCanUpdateMachineSet mirrors DoCanUpdateMachine on the MachineSet template spec.
func (h *ExtensionHandlers) DoCanUpdateMachineSet(ctx context.Context, req *runtimehooksv1.CanUpdateMachineSetRequest, resp *runtimehooksv1.CanUpdateMachineSetResponse) {
	modified := req.Current.MachineSet.DeepCopy()
	cur := inplace.SpecView{Version: modified.Spec.Template.Spec.Version}
	des := inplace.SpecView{Version: req.Desired.MachineSet.Spec.Template.Spec.Version}
	accepted, _ := inplace.AcceptInPlace(cur, des)
	modified.Spec.Template.Spec.Version = accepted.Version

	marshalledCurrent, err := json.Marshal(req.Current.MachineSet)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = pkgerrors.Wrap(err, "marshal current MachineSet").Error()
		return
	}
	machineSetPatch, err := createJSONPatch(marshalledCurrent, modified)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = err.Error()
		return
	}
	resp.MachineSetPatch = runtimehooksv1.Patch{PatchType: runtimehooksv1.JSONPatchType, Patch: machineSetPatch}
	resp.InfrastructureMachineTemplatePatch = emptyJSONPatch()
	resp.BootstrapConfigTemplatePatch = emptyJSONPatch()
	resp.Status = runtimehooksv1.ResponseStatusSuccess
}

// DoUpdateMachine is the Phase-0 CAPD TEST handler. It fakes the in-place update:
// it reports in-progress (RetryAfterSeconds>0) until completeAfter has elapsed,
// then reports completion (RetryAfterSeconds=0). It performs NO real work.
// The production executor (kubeadm upgrade, host signal, drain) is OUT of scope.
func (h *ExtensionHandlers) DoUpdateMachine(ctx context.Context, req *runtimehooksv1.UpdateMachineRequest, resp *runtimehooksv1.UpdateMachineResponse) {
	_ = ctrl.LoggerFrom(ctx)
	key := req.Desired.Machine.Namespace + "/" + req.Desired.Machine.Name
	now := time.Now()

	if started, ok := h.state.Load(key); ok {
		if inplace.UpdateDone(started.(time.Time), now, h.completeAfter) {
			h.state.Delete(key)
			resp.Status = runtimehooksv1.ResponseStatusSuccess
			resp.RetryAfterSeconds = 0
			resp.Message = "in-place update complete (CAPD test handler — no host work performed)"
			return
		}
	} else {
		h.state.Store(key, now)
	}
	resp.Status = runtimehooksv1.ResponseStatusSuccess
	resp.RetryAfterSeconds = 5
	resp.Message = "in-place update in progress (CAPD test handler — no host work performed)"
}

// createJSONPatch builds an RFC 6902 JSON patch from a marshalled original and a
// modified object. Adapted from cluster-api test/extension (Apache-2.0).
func createJSONPatch(marshalledOriginal []byte, modified runtime.Object) ([]byte, error) {
	marshalledModified, err := json.Marshal(modified)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "marshal modified object")
	}
	patch, err := jsonpatch.CreatePatch(marshalledOriginal, marshalledModified)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "create patch")
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "marshal patch")
	}
	return patchBytes, nil
}
