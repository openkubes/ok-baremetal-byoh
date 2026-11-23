// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package extension

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	"github.com/openkubes/ok-baremetal-byoh/internal/inplace"
)

// ApprovalAnnotation is the per-host hold point of an upgrade: the extension
// requests the upgrade of a host only when an administrator has set it on the
// ByoHost to the target version. Agents cannot set it (they write only the
// status of their ByoHost), so no host starts on its own and none follows the
// previous one without a decision.
const ApprovalAnnotation = "inplace.openkubes.org/approved-upgrade"

// NodeAccess drains and verifies the Nodes of a workload cluster.
type NodeAccess interface {
	// Drain cordons the Node and evicts its pods, respecting
	// PodDisruptionBudgets; DaemonSet and static pods stay.
	Drain(ctx context.Context, cluster client.ObjectKey, node string) error
	// Uncordon makes the Node schedulable again.
	Uncordon(ctx context.Context, cluster client.ObjectKey, node string) error
	// KubeletVersion returns status.nodeInfo.kubeletVersion of the Node.
	KubeletVersion(ctx context.Context, cluster client.ObjectKey, node string) (string, error)
}

type state int

const (
	stateFailed state = iota
	stateWaiting
	stateDone
)

func (s state) String() string {
	return [...]string{"failed", "waiting", "done"}[s]
}

// outcome is the answer of one UpdateMachine call.
type outcome struct {
	state      state
	retryAfter int32
	message    string
}

func failed(format string, args ...any) outcome {
	return outcome{state: stateFailed, message: fmt.Sprintf(format, args...)}
}

func waiting(retryAfter int32, format string, args ...any) outcome {
	return outcome{state: stateWaiting, retryAfter: retryAfter, message: fmt.Sprintf(format, args...)}
}

func done(format string, args ...any) outcome {
	return outcome{state: stateDone, message: fmt.Sprintf(format, args...)}
}

// target is the resolved, identity-checked object chain of an update.
type target struct {
	machine *clusterv1.Machine
	cluster *clusterv1.Cluster
	host    *infrav1.ByoHost
	node    string
	from    string
	to      string
}

// resolve reads the live Machine, its Cluster, its ByoMachine and the one
// ByoHost attached to it, and checks that they are bound to each other.
func (h *ExtensionHandlers) resolve(ctx context.Context, desired *clusterv1.Machine) (*target, *outcome) {
	t := &target{machine: &clusterv1.Machine{}, cluster: &clusterv1.Cluster{}, to: desired.Spec.Version}
	if err := h.Client.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, t.machine); err != nil {
		o := failed("reading Machine %s: %v", desired.Name, err)
		return nil, &o
	}
	if t.machine.UID != desired.UID || desired.UID == "" {
		o := failed("Machine %s has the UID %s, the update is for %s", desired.Name, t.machine.UID, desired.UID)
		return nil, &o
	}
	if t.machine.Spec.Version != t.to {
		o := waiting(5, "Machine %s has the version %s, the update is for %s", desired.Name, t.machine.Spec.Version, t.to)
		return nil, &o
	}
	if !t.machine.DeletionTimestamp.IsZero() {
		o := failed("Machine %s is being deleted", desired.Name)
		return nil, &o
	}
	if err := h.Client.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: t.machine.Spec.ClusterName}, t.cluster); err != nil {
		o := failed("reading Cluster %s: %v", t.machine.Spec.ClusterName, err)
		return nil, &o
	}
	ref := t.machine.Spec.InfrastructureRef
	if ref.Kind != byoMachineKind || ref.APIGroup != infrav1.GroupVersion.Group {
		o := failed("Machine %s has the infrastructure %s.%s, not a ByoMachine", desired.Name, ref.Kind, ref.APIGroup)
		return nil, &o
	}
	byoMachine := &infrav1.ByoMachine{}
	if err := h.Client.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: ref.Name}, byoMachine); err != nil {
		o := failed("reading ByoMachine %s: %v", ref.Name, err)
		return nil, &o
	}
	owned := false
	for _, owner := range byoMachine.OwnerReferences {
		if owner.Kind == "Machine" && owner.UID == t.machine.UID && owner.Controller != nil && *owner.Controller {
			owned = true
		}
	}
	if !owned {
		o := failed("ByoMachine %s is not controlled by Machine %s (%s)", byoMachine.Name, t.machine.Name, t.machine.UID)
		return nil, &o
	}
	hosts := &infrav1.ByoHostList{}
	if err := h.Client.List(ctx, hosts, client.InNamespace(desired.Namespace)); err != nil {
		o := failed("listing ByoHosts: %v", err)
		return nil, &o
	}
	for i := range hosts.Items {
		if r := hosts.Items[i].Spec.ByoMachineRef; r != nil && r.UID == byoMachine.UID {
			if t.host != nil {
				o := failed("ByoMachine %s is attached to more than one ByoHost", byoMachine.Name)
				return nil, &o
			}
			t.host = &hosts.Items[i]
		}
	}
	if t.host == nil {
		o := failed("no ByoHost is attached to ByoMachine %s (%s)", byoMachine.Name, byoMachine.UID)
		return nil, &o
	}
	t.node = t.machine.Status.NodeRef.Name
	if t.node == "" {
		o := failed("Machine %s has no Node", t.machine.Name)
		return nil, &o
	}
	if t.machine.Status.NodeInfo == nil || t.machine.Status.NodeInfo.KubeletVersion == "" {
		o := waiting(10, "Machine %s does not report the kubelet version of its Node yet", t.machine.Name)
		return nil, &o
	}
	t.from = t.machine.Status.NodeInfo.KubeletVersion
	return t, nil
}

// updateByoMachine drives the in-place upgrade of the host of a ByoMachine.
func (h *ExtensionHandlers) updateByoMachine(ctx context.Context, desired *clusterv1.Machine) outcome {
	t, o := h.resolve(ctx, desired)
	if o != nil {
		return *o
	}
	id := inplace.UpgradeID(string(t.machine.UID), t.to)
	if req := t.host.Spec.InPlaceUpgrade; req != nil {
		if req.ID == id {
			return h.progress(ctx, t, req)
		}
		if s := t.host.Status.InPlaceUpgrade; s == nil || !s.Matches(req) || s.Phase == infrav1.InPlaceUpgradeRunning {
			return failed("ByoHost %s has the unfinished in-place upgrade %s", t.host.Name, req.ID)
		}
	}
	return h.request(ctx, t, id)
}

// request writes a new upgrade request after its checks and the drain.
func (h *ExtensionHandlers) request(ctx context.Context, t *target, id string) outcome {
	if t.from == t.to {
		return failed("Node %s already reports %s without an upgrade verified by its agent; check the host", t.node, t.to)
	}
	if err := inplace.SupportedTransition(t.from, t.to); err != nil {
		return failed("%v", err)
	}
	pinned, ok := h.Packages[t.to]
	if !ok {
		return failed("the packages of %s are not pinned", t.to)
	}
	machines := &clusterv1.MachineList{}
	if err := h.Client.List(ctx, machines, client.InNamespace(t.machine.Namespace), client.MatchingLabels{clusterv1.ClusterNameLabel: t.cluster.Name}); err != nil {
		return failed("listing the Machines of Cluster %s: %v", t.cluster.Name, err)
	}
	role, wait := upgradeRole(t, machines.Items)
	if wait != nil {
		return *wait
	}
	if t.host.Annotations[ApprovalAnnotation] != t.to {
		return waiting(30, "waiting for the approval of the upgrade of ByoHost %s to %s (annotation %s)", t.host.Name, t.to, ApprovalAnnotation)
	}
	if busy, err := h.otherUpgradeRunning(ctx, t); err != nil {
		return failed("%v", err)
	} else if busy != "" {
		return waiting(30, "ByoHost %s of Cluster %s runs an in-place upgrade; hosts are upgraded one at a time", busy, t.cluster.Name)
	}
	cluster := client.ObjectKeyFromObject(t.cluster)
	if err := h.Nodes.Drain(ctx, cluster, t.node); err != nil {
		return waiting(30, "draining Node %s: %v", t.node, err)
	}
	req := &infrav1.InPlaceUpgradeRequest{
		ID: id, ClusterName: t.cluster.Name, ClusterUID: t.cluster.UID,
		MachineName: t.machine.Name, MachineUID: t.machine.UID, ByoMachineUID: t.host.Spec.ByoMachineRef.UID,
		NodeName: t.node, Role: role, FromVersion: t.from, ToVersion: t.to,
		Packages: infrav1.InPlaceUpgradePackages{Repository: pinned.Repository, RepositoryKeyFingerprint: pinned.RepositoryKeyFingerprint},
	}
	for _, deb := range pinned.Debs {
		req.Packages.Debs = append(req.Packages.Debs, infrav1.PinnedDeb{Name: deb.Name, Version: deb.Version, SHA256: deb.SHA256})
	}
	before := t.host.DeepCopy()
	t.host.Spec.InPlaceUpgrade = req
	if err := h.Client.Patch(ctx, t.host, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) {
			return waiting(5, "ByoHost %s changed while the upgrade was requested", t.host.Name)
		}
		return failed("requesting the upgrade on ByoHost %s: %v", t.host.Name, err)
	}
	return waiting(DefaultRetryAfter, "in-place upgrade %s of Node %s from %s to %s requested as %s", id, t.node, t.from, t.to, role)
}

// upgradeRole returns the role of the host. The first control-plane host runs
// kubeadm upgrade apply; a worker waits until every control-plane Machine of
// the cluster reports the target version, as a kubelet must not be newer than
// the API servers.
func upgradeRole(t *target, machines []clusterv1.Machine) (infrav1.InPlaceUpgradeRole, *outcome) {
	_, isControlPlane := t.machine.Labels[clusterv1.MachineControlPlaneLabel]
	controlPlanes, upgraded := 0, 0
	for i := range machines {
		m := &machines[i]
		if _, ok := m.Labels[clusterv1.MachineControlPlaneLabel]; !ok || m.UID == t.machine.UID {
			continue
		}
		controlPlanes++
		if m.Status.NodeInfo != nil && m.Status.NodeInfo.KubeletVersion == t.to {
			upgraded++
		}
	}
	if isControlPlane {
		if upgraded == 0 {
			return infrav1.InPlaceUpgradeControlPlaneFirst, nil
		}
		return infrav1.InPlaceUpgradeControlPlane, nil
	}
	if controlPlanes == 0 || upgraded != controlPlanes {
		o := waiting(30, "waiting for the control plane: %d of %d control-plane Machines report %s", upgraded, controlPlanes, t.to)
		return "", &o
	}
	return infrav1.InPlaceUpgradeWorker, nil
}

// otherUpgradeRunning returns a ByoHost of another Machine of the cluster that
// runs an in-place upgrade, or "".
func (h *ExtensionHandlers) otherUpgradeRunning(ctx context.Context, t *target) (string, error) {
	hosts := &infrav1.ByoHostList{}
	if err := h.Client.List(ctx, hosts, client.InNamespace(t.machine.Namespace)); err != nil {
		return "", fmt.Errorf("listing ByoHosts: %w", err)
	}
	for i := range hosts.Items {
		other := &hosts.Items[i]
		req := other.Spec.InPlaceUpgrade
		if other.UID == t.host.UID || req == nil || req.ClusterUID != t.cluster.UID {
			continue
		}
		if s := other.Status.InPlaceUpgrade; s == nil || !s.Matches(req) || s.Phase == infrav1.InPlaceUpgradeRunning {
			return other.Name, nil
		}
	}
	return "", nil
}

// progress reports the upgrade that the agent runs. Only an upgrade that the
// agent verified, and whose Node the extension reads with the target version,
// is done.
func (h *ExtensionHandlers) progress(ctx context.Context, t *target, req *infrav1.InPlaceUpgradeRequest) outcome {
	if req.MachineUID != t.machine.UID || req.ClusterUID != t.cluster.UID || req.ByoMachineUID != t.host.Spec.ByoMachineRef.UID || req.NodeName != t.node || req.ToVersion != t.to {
		return failed("the in-place upgrade %s on ByoHost %s is bound to another Machine, Cluster, ByoMachine, Node or version", req.ID, t.host.Name)
	}
	s := t.host.Status.InPlaceUpgrade
	if s == nil || !s.Matches(req) {
		return waiting(DefaultRetryAfter, "waiting for the agent of ByoHost %s to accept the in-place upgrade %s", t.host.Name, req.ID)
	}
	switch s.Phase {
	case infrav1.InPlaceUpgradeRunning:
		timeout := h.UpgradeTimeout
		if timeout == 0 {
			timeout = DefaultUpgradeTimeout
		}
		if s.StartedAt != nil && h.now().Sub(s.StartedAt.Time) > timeout {
			return failed("the in-place upgrade %s on ByoHost %s has run for more than %s (step %s); it is not verified, see the runbook", req.ID, t.host.Name, timeout, s.Step)
		}
		return waiting(DefaultRetryAfter, "the agent of ByoHost %s runs the in-place upgrade %s (step %s)", t.host.Name, req.ID, s.Step)
	case infrav1.InPlaceUpgradeFailed:
		return failed("the in-place upgrade %s failed on ByoHost %s at step %s: %s; recover forward and raise spec.inPlaceUpgrade.attempt (runbook)", req.ID, t.host.Name, s.Step, s.Message)
	case infrav1.InPlaceUpgradeSucceeded:
	default:
		return failed("ByoHost %s reports the unknown phase %q", t.host.Name, s.Phase)
	}
	if s.ObservedKubeletVersion != t.to || s.ObservedNodeVersion != t.to {
		return failed("ByoHost %s reports Succeeded with kubelet %q and Node %q, not %s", t.host.Name, s.ObservedKubeletVersion, s.ObservedNodeVersion, t.to)
	}
	cluster := client.ObjectKeyFromObject(t.cluster)
	version, err := h.Nodes.KubeletVersion(ctx, cluster, t.node)
	if err != nil {
		return waiting(10, "reading Node %s: %v", t.node, err)
	}
	if version != t.to {
		return failed("the agent of ByoHost %s verified %s, but Node %s reports %s", t.host.Name, t.to, t.node, version)
	}
	if err := h.Nodes.Uncordon(ctx, cluster, t.node); err != nil {
		return waiting(10, "uncordoning Node %s: %v", t.node, err)
	}
	return done("Node %s runs %s, verified by the agent of ByoHost %s and on the Node at %s", t.node, t.to, t.host.Name, h.now().UTC().Format(time.RFC3339))
}
