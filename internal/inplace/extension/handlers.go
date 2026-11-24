// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package extension holds the CAPI-typed RuntimeSDK handlers for the OpenKubes
// BYOH in-place update extension. The pure decision logic lives in
// internal/inplace; this package is the glue to the CAPI runtime hooks, to the
// ByoHosts of the management cluster and to the Nodes of the workload cluster.
//
// UpdateMachine keeps no state of its own: the request in
// spec.inPlaceUpgrade of the ByoHost and the status that its agent verified
// are the state, so a repeated or concurrent call, or a restart of the
// extension, continues the same upgrade.
//
// createJSONPatch is adapted from cluster-api test/extension (Apache-2.0).
package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	pkgerrors "github.com/pkg/errors"
	"gomodules.xyz/jsonpatch/v2"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	"github.com/openkubes/ok-baremetal-byoh/internal/inplace"
)

// Infrastructure kinds of the Machines that the extension updates.
const (
	byoMachineKind    = "ByoMachine"
	dockerMachineKind = "DockerMachine"
)

// Defaults of the extension. The upgrade timeout covers the bounded steps of
// the agent (agent/inplaceupgrade.DefaultTimeouts) with a margin, so that the
// extension does not report a failure while the agent still works.
const (
	DefaultUpgradeTimeout = 2 * time.Hour
	DefaultRetryAfter     = 15
)

// ExtensionHandlers implements CanUpdateMachine, CanUpdateMachineSet and
// UpdateMachine.
type ExtensionHandlers struct {
	// Client reads Machines, Clusters, ByoMachines and ByoHosts and writes
	// spec.inPlaceUpgrade of ByoHosts. Use an uncached client.
	Client client.Client
	// Packages pins the target versions.
	Packages inplace.PackageLock
	// Nodes drains and verifies the Nodes of the workload clusters. Without
	// it the extension refuses ByoMachines.
	Nodes NodeAccess
	// UpgradeTimeout bounds an upgrade from the start that its agent reports.
	UpgradeTimeout time.Duration
	// Now is time.Now by default.
	Now func() time.Time
	// SimulateDockerMachines lets the CAPD lab test the control loop with
	// DockerMachines: the update of such a Machine only waits. It never
	// applies to ByoMachines.
	SimulateDockerMachines bool

	simulated     sync.Map // key -> time.Time (start of a simulated update)
	completeAfter time.Duration
}

// NewExtensionHandlers returns handlers for the given management client,
// package lock and Node access.
func NewExtensionHandlers(c client.Client, packages inplace.PackageLock, nodes NodeAccess) *ExtensionHandlers {
	return &ExtensionHandlers{
		Client:         c,
		Packages:       packages,
		Nodes:          nodes,
		UpgradeTimeout: DefaultUpgradeTimeout,
		completeAfter:  inplace.DefaultUpdateCompleteAfter,
	}
}

func (h *ExtensionHandlers) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func emptyJSONPatch() runtimehooksv1.Patch {
	return runtimehooksv1.Patch{PatchType: runtimehooksv1.JSONPatchType, Patch: []byte("[]")}
}

// supportedInfrastructure reports whether the extension can update a Machine
// of the infrastructure kind in place.
func (h *ExtensionHandlers) supportedInfrastructure(kind string) bool {
	switch kind {
	case byoMachineKind:
		return h.Nodes != nil && h.Client != nil
	case dockerMachineKind:
		return h.SimulateDockerMachines
	}
	return false
}

// canUpdateVersion reports why the extension does not take over a version
// change in place, or nil. It takes only a supported transition to a pinned
// version (a simulated DockerMachine needs no pin).
func (h *ExtensionHandlers) canUpdateVersion(infraKind, from, to string) error {
	if from == to {
		return nil
	}
	if !h.supportedInfrastructure(infraKind) {
		return fmt.Errorf("the extension does not update %s Machines in place", infraKind)
	}
	if err := inplace.SupportedTransition(from, to); err != nil {
		return err
	}
	if infraKind == byoMachineKind {
		if _, ok := h.Packages[to]; !ok {
			return fmt.Errorf("the packages of %s are not pinned", to)
		}
	}
	return nil
}

// DoCanUpdateMachine declares that this extension can update ONLY the Machine
// Kubernetes version in-place (version-only, identity-preserving), for a
// supported infrastructure and a pinned, supported target. NO API writes.
//
// Declining leaves the remaining diff to CAPI, which then falls back to a
// rollout: on a protected cluster the No-Replacement guard denies the
// resulting Machine deletion and the rollout stops visibly.
func (h *ExtensionHandlers) DoCanUpdateMachine(ctx context.Context, req *runtimehooksv1.CanUpdateMachineRequest, resp *runtimehooksv1.CanUpdateMachineResponse) {
	modified := req.Current.Machine.DeepCopy()
	cur := inplace.SpecView{Version: modified.Spec.Version}
	des := inplace.SpecView{Version: req.Desired.Machine.Spec.Version}
	if err := h.canUpdateVersion(req.Current.Machine.Spec.InfrastructureRef.Kind, cur.Version, des.Version); err != nil {
		ctrl.LoggerFrom(ctx).Info("Declining in-place update", "Machine", req.Current.Machine.Name, "reason", err.Error())
		des = cur
	}
	accepted, _ := inplace.AcceptInPlace(cur, des)
	modified.Spec.Version = accepted.Version

	marshaledCurrent, err := json.Marshal(req.Current.Machine)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = pkgerrors.Wrap(err, "marshal current Machine").Error()
		return
	}
	machinePatch, err := createJSONPatch(marshaledCurrent, modified)
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
	if err := h.canUpdateVersion(req.Current.MachineSet.Spec.Template.Spec.InfrastructureRef.Kind, cur.Version, des.Version); err != nil {
		ctrl.LoggerFrom(ctx).Info("Declining in-place update", "MachineSet", req.Current.MachineSet.Name, "reason", err.Error())
		des = cur
	}
	accepted, _ := inplace.AcceptInPlace(cur, des)
	modified.Spec.Template.Spec.Version = accepted.Version

	marshaledCurrent, err := json.Marshal(req.Current.MachineSet)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = pkgerrors.Wrap(err, "marshal current MachineSet").Error()
		return
	}
	machineSetPatch, err := createJSONPatch(marshaledCurrent, modified)
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

// DoUpdateMachine performs the in-place update of a Machine. It answers in
// progress (RetryAfterSeconds>0) until the update is verified, success
// (RetryAfterSeconds=0) only then, and failure for anything that it cannot
// verify. CAPI keeps calling it while the update is in progress and never
// falls back to a replacement once it has started, so a failure stops the
// update of this Machine for an operator instead of replacing it.
func (h *ExtensionHandlers) DoUpdateMachine(ctx context.Context, req *runtimehooksv1.UpdateMachineRequest, resp *runtimehooksv1.UpdateMachineResponse) {
	machine := &req.Desired.Machine
	log := ctrl.LoggerFrom(ctx).WithValues("Machine", machine.Namespace+"/"+machine.Name, "version", machine.Spec.Version)
	var out outcome
	switch kind := machine.Spec.InfrastructureRef.Kind; {
	case kind == byoMachineKind && h.supportedInfrastructure(kind):
		out = h.updateByoMachine(ctx, machine)
	case kind == dockerMachineKind && h.SimulateDockerMachines:
		out = h.simulateUpdate(machine)
	default:
		out = failed("the extension does not update %s Machines in place", kind)
	}
	log.Info("UpdateMachine", "result", out.state, "message", out.message)
	resp.Message = out.message
	switch out.state {
	case stateDone:
		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.RetryAfterSeconds = 0
	case stateWaiting:
		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.RetryAfterSeconds = out.retryAfter
		if resp.RetryAfterSeconds <= 0 {
			resp.RetryAfterSeconds = DefaultRetryAfter
		}
	default:
		resp.Status = runtimehooksv1.ResponseStatusFailure
	}
}

// simulateUpdate is the CAPD lab update of a DockerMachine: it waits for
// completeAfter and does no host work, so it proves only the CAPI control
// loop (labs/byoh-inplace-spike/guarded/README.md).
func (h *ExtensionHandlers) simulateUpdate(machine *clusterv1.Machine) outcome {
	// not the UID: Cluster API does not send it in the request
	key := machine.Namespace + "/" + machine.Name + "/" + machine.Spec.Version
	now := h.now()
	started, loaded := h.simulated.LoadOrStore(key, now)
	if loaded && inplace.UpdateDone(started.(time.Time), now, h.completeAfter) {
		h.simulated.Delete(key)
		return done("simulated in-place update complete (CAPD DockerMachine, no host work performed)")
	}
	return waiting(retryShort, "simulated in-place update in progress (CAPD DockerMachine, no host work performed)")
}

// createJSONPatch builds an RFC 6902 JSON patch from a marshaled original and a
// modified object. Adapted from cluster-api test/extension (Apache-2.0).
func createJSONPatch(marshaledOriginal []byte, modified runtime.Object) ([]byte, error) {
	marshaledModified, err := json.Marshal(modified)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "marshal modified object")
	}
	patch, err := jsonpatch.CreatePatch(marshaledOriginal, marshaledModified)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "create patch")
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "marshal patch")
	}
	return patchBytes, nil
}
