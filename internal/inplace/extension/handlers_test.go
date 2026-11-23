// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package extension

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
	"github.com/openkubes/ok-baremetal-byoh/internal/inplace"
)

const (
	ns   = "ha1"
	from = "v1.36.6"
	toV  = "v1.37.2"
)

var lock = inplace.PackageLock{toV: {
	Repository:               "https://pkgs.k8s.io/core:/stable:/v1.37/deb/",
	RepositoryKeyFingerprint: "DE15B14486CD377B9E876E1A234654DA9A296436",
	Debs: []inplace.PinnedDeb{
		{Name: "kubeadm", Version: "1.37.2-1.1", SHA256: "d1674bffcf80b36531375b35cf68d0dc23453a373c1b4537d78b6960732bc699"},
		{Name: "kubelet", Version: "1.37.2-1.1", SHA256: "f95d0b572a24563bb6e2a1a1f0e84a8a30a24e11c95068b2e81c294a0e1bee11"},
		{Name: "kubectl", Version: "1.37.2-1.1", SHA256: "e95d73aaf1da438cc8b287545b128fbe5e6a9f3723ecaa25eac1b42b8fe542ab"},
	},
}}

// fakeNodes records the calls on the Nodes of the workload cluster.
type fakeNodes struct {
	drained, uncordoned []string
	versions            map[string]string
	drainErr, readErr   error
}

func (f *fakeNodes) Drain(_ context.Context, _ client.ObjectKey, node string) error {
	f.drained = append(f.drained, node)
	return f.drainErr
}

func (f *fakeNodes) Uncordon(_ context.Context, _ client.ObjectKey, node string) error {
	f.uncordoned = append(f.uncordoned, node)
	return nil
}

func (f *fakeNodes) KubeletVersion(_ context.Context, _ client.ObjectKey, node string) (string, error) {
	return f.versions[node], f.readErr
}

// world is a management cluster with the Cluster ha1, three control-plane
// Machines and two workers, each with its ByoMachine and attached ByoHost.
type world struct {
	t        *testing.T
	client   client.Client
	nodes    *fakeNodes
	h        *ExtensionHandlers
	now      time.Time
	patches  int
	conflict bool
}

func scheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, clusterv1.AddToScheme, infrav1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

var machineNames = []string{"cp-1", "cp-2", "cp-3", "w-1", "w-2"}

func isCP(name string) bool { return strings.HasPrefix(name, "cp-") }

func newWorld(t *testing.T) *world {
	w := &world{t: t, nodes: &fakeNodes{versions: map[string]string{}}, now: time.Date(2026, 11, 23, 22, 0, 0, 0, time.UTC)}
	objs := []client.Object{&clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "ha1", Namespace: ns, UID: "uid-cluster"}}}
	for _, name := range machineNames {
		labels := map[string]string{clusterv1.ClusterNameLabel: "ha1"}
		if isCP(name) {
			labels[clusterv1.MachineControlPlaneLabel] = ""
		}
		m := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-m-" + name), Labels: labels},
			Spec: clusterv1.MachineSpec{
				ClusterName: "ha1", Version: from,
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{APIGroup: infrav1.GroupVersion.Group, Kind: "ByoMachine", Name: "bm-" + name},
			},
			Status: clusterv1.MachineStatus{NodeRef: clusterv1.MachineNodeReference{Name: "node-" + name}, NodeInfo: &corev1.NodeSystemInfo{KubeletVersion: from}},
		}
		bm := &infrav1.ByoMachine{ObjectMeta: metav1.ObjectMeta{
			Name: "bm-" + name, Namespace: ns, UID: types.UID("uid-bm-" + name),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: clusterv1.GroupVersion.String(), Kind: "Machine", Name: name, UID: m.UID, Controller: ptr.To(true)}},
		}}
		host := &infrav1.ByoHost{
			ObjectMeta: metav1.ObjectMeta{Name: "host-" + name, Namespace: ns, UID: types.UID("uid-h-" + name), Annotations: map[string]string{ApprovalAnnotation: toV}},
			Spec:       infrav1.ByoHostSpec{ByoMachineRef: &infrav1.ByoMachineReference{Name: bm.Name, UID: bm.UID}},
		}
		w.nodes.versions["node-"+name] = from
		objs = append(objs, m, bm, host)
	}
	w.client = fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).
		WithStatusSubresource(&infrav1.ByoHost{}, &clusterv1.Machine{}).
		WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			w.patches++
			if w.conflict {
				return apierrors.NewConflict(schema.GroupResource{Resource: "byohosts"}, obj.GetName(), errors.New("changed"))
			}
			return c.Patch(ctx, obj, patch, opts...)
		}}).Build()
	w.h = NewExtensionHandlers(w.client, lock, w.nodes)
	w.h.Now = func() time.Time { return w.now }
	return w
}

func (w *world) machine(name string) *clusterv1.Machine {
	m := &clusterv1.Machine{}
	if err := w.client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, m); err != nil {
		w.t.Fatal(err)
	}
	return m
}

func (w *world) host(name string) *infrav1.ByoHost {
	h := &infrav1.ByoHost{}
	if err := w.client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "host-" + name}, h); err != nil {
		w.t.Fatal(err)
	}
	return h
}

// bump sets the toV version on a Machine, as CAPI does before the hook.
func (w *world) bump(name string) {
	m := w.machine(name)
	m.Spec.Version = toV
	if err := w.client.Update(context.Background(), m); err != nil {
		w.t.Fatal(err)
	}
}

// upgraded marks a Machine as reporting the toV version.
func (w *world) upgraded(name string) {
	m := w.machine(name)
	m.Spec.Version = toV
	if err := w.client.Update(context.Background(), m); err != nil {
		w.t.Fatal(err)
	}
	m.Status.NodeInfo.KubeletVersion = toV
	if err := w.client.Status().Update(context.Background(), m); err != nil {
		w.t.Fatal(err)
	}
	w.nodes.versions["node-"+name] = toV
}

func (w *world) report(name string, status infrav1.InPlaceUpgradeStatus) {
	h := w.host(name)
	status.ID = h.Spec.InPlaceUpgrade.ID
	status.Attempt = h.Spec.InPlaceUpgrade.Attempt
	h.Status.InPlaceUpgrade = &status
	if err := w.client.Status().Update(context.Background(), h); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) update(name string) *runtimehooksv1.UpdateMachineResponse {
	resp := &runtimehooksv1.UpdateMachineResponse{}
	w.h.DoUpdateMachine(context.Background(), &runtimehooksv1.UpdateMachineRequest{Desired: runtimehooksv1.UpdateMachineRequestObjects{Machine: *w.machine(name)}}, resp)
	return resp
}

func expect(t *testing.T, resp *runtimehooksv1.UpdateMachineResponse, want state, message string) {
	t.Helper()
	got := stateFailed
	switch {
	case resp.Status == runtimehooksv1.ResponseStatusSuccess && resp.RetryAfterSeconds == 0:
		got = stateDone
	case resp.Status == runtimehooksv1.ResponseStatusSuccess:
		got = stateWaiting
	}
	if got != want || !strings.Contains(resp.Message, message) {
		t.Fatalf("got %s %q, want %s with %q", got, resp.Message, want, message)
	}
}

func TestFirstControlPlaneUpgrade(t *testing.T) {
	w := newWorld(t)
	w.bump("cp-1")

	expect(t, w.update("cp-1"), stateWaiting, "requested as ControlPlaneFirst")
	req := w.host("cp-1").Spec.InPlaceUpgrade
	if req == nil || req.Role != infrav1.InPlaceUpgradeControlPlaneFirst || req.MachineUID != "uid-m-cp-1" || req.ByoMachineUID != "uid-bm-cp-1" ||
		req.ClusterUID != "uid-cluster" || req.NodeName != "node-cp-1" || req.FromVersion != from || req.ToVersion != toV || len(req.Packages.Debs) != 3 {
		t.Fatalf("request %+v", req)
	}
	if errs := infrav1.ValidateInPlaceUpgradeRequest(&w.host("cp-1").Spec); len(errs) > 0 {
		t.Fatalf("the request is not valid for the ByoHost webhook: %v", errs)
	}
	if len(w.nodes.drained) != 1 || w.nodes.drained[0] != "node-cp-1" {
		t.Fatalf("drained %v", w.nodes.drained)
	}

	// Repeated calls do not write or drain again.
	patches := w.patches
	expect(t, w.update("cp-1"), stateWaiting, "to accept")
	expect(t, w.update("cp-1"), stateWaiting, "to accept")
	if w.patches != patches || len(w.nodes.drained) != 1 {
		t.Fatalf("a repeated call wrote %d patches and drained %v", w.patches-patches, w.nodes.drained)
	}

	w.report("cp-1", infrav1.InPlaceUpgradeStatus{Phase: infrav1.InPlaceUpgradeRunning, Step: "KubeadmUpgrade", StartedAt: &metav1.Time{Time: w.now}})
	expect(t, w.update("cp-1"), stateWaiting, "step KubeadmUpgrade")

	w.report("cp-1", infrav1.InPlaceUpgradeStatus{Phase: infrav1.InPlaceUpgradeSucceeded, ObservedKubeletVersion: toV, ObservedNodeVersion: toV})
	w.nodes.versions["node-cp-1"] = toV
	expect(t, w.update("cp-1"), stateDone, "Node node-cp-1 runs v1.37.2")
	if len(w.nodes.uncordoned) != 1 {
		t.Fatalf("uncordoned %v", w.nodes.uncordoned)
	}
	// The Machine and the ByoHost keep their identity.
	if w.machine("cp-1").UID != "uid-m-cp-1" || w.host("cp-1").Spec.ByoMachineRef.UID != "uid-bm-cp-1" {
		t.Fatal("identity changed")
	}
}

func TestRolesAndSequencing(t *testing.T) {
	w := newWorld(t)
	w.bump("w-1")
	expect(t, w.update("w-1"), stateWaiting, "waiting for the control plane: 0 of 3")
	if w.host("w-1").Spec.InPlaceUpgrade != nil || len(w.nodes.drained) != 0 {
		t.Fatal("a worker was requested before its control plane")
	}

	w.upgraded("cp-1")
	w.bump("cp-2")
	expect(t, w.update("cp-2"), stateWaiting, "requested as ControlPlane")

	// cp-2 runs: no other host of the cluster starts.
	w.bump("cp-3")
	expect(t, w.update("cp-3"), stateWaiting, "runs an in-place upgrade; hosts are upgraded one at a time")
	w.report("cp-2", infrav1.InPlaceUpgradeStatus{Phase: infrav1.InPlaceUpgradeSucceeded, ObservedKubeletVersion: toV, ObservedNodeVersion: toV})
	w.upgraded("cp-2")
	expect(t, w.update("cp-3"), stateWaiting, "requested as ControlPlane")
	w.report("cp-3", infrav1.InPlaceUpgradeStatus{Phase: infrav1.InPlaceUpgradeSucceeded, ObservedKubeletVersion: toV, ObservedNodeVersion: toV})
	w.upgraded("cp-3")

	expect(t, w.update("w-1"), stateWaiting, "requested as Worker")
	if w.host("w-1").Spec.InPlaceUpgrade.Role != infrav1.InPlaceUpgradeWorker {
		t.Fatal("not a worker")
	}
}

func TestFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*world)
		machine string
		want    state
		message string
	}{
		{"another Machine UID", func(w *world) {}, "uid", stateFailed, "the update is for"},
		{"a ByoMachine of another Machine", func(w *world) {
			bm := &infrav1.ByoMachine{}
			_ = w.client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "bm-cp-1"}, bm)
			bm.OwnerReferences[0].UID = "uid-other"
			_ = w.client.Update(context.Background(), bm)
		}, "cp-1", stateFailed, "is not controlled by Machine"},
		{"two attached ByoHosts", func(w *world) {
			h := w.host("cp-2")
			h.Spec.ByoMachineRef = &infrav1.ByoMachineReference{Name: "bm-cp-1", UID: "uid-bm-cp-1"}
			_ = w.client.Update(context.Background(), h)
		}, "cp-1", stateFailed, "more than one ByoHost"},
		{"no attached ByoHost", func(w *world) {
			h := w.host("cp-1")
			h.Spec.ByoMachineRef = nil
			_ = w.client.Update(context.Background(), h)
		}, "cp-1", stateFailed, "no ByoHost is attached"},
		{"no Node", func(w *world) {
			m := w.machine("cp-1")
			m.Status.NodeRef = clusterv1.MachineNodeReference{}
			_ = w.client.Status().Update(context.Background(), m)
		}, "cp-1", stateFailed, "has no Node"},
		{"an unpinned toV", func(w *world) { w.h.Packages = inplace.PackageLock{} }, "cp-1", stateFailed, "not pinned"},
		{"a skipped minor version", func(w *world) {
			m := w.machine("cp-1")
			m.Status.NodeInfo.KubeletVersion = "v1.35.9"
			_ = w.client.Status().Update(context.Background(), m)
		}, "cp-1", stateFailed, "at most one minor version"},
		{"a Node already at the toV", func(w *world) {
			m := w.machine("cp-1")
			m.Status.NodeInfo.KubeletVersion = toV
			_ = w.client.Status().Update(context.Background(), m)
		}, "cp-1", stateFailed, "without an upgrade verified by its agent"},
		{"an unfinished upgrade on the ByoHost", func(w *world) {
			h := w.host("cp-1")
			h.Spec.InPlaceUpgrade = &infrav1.InPlaceUpgradeRequest{ID: "other", ClusterUID: "uid-cluster"}
			_ = w.client.Update(context.Background(), h)
		}, "cp-1", stateFailed, "unfinished in-place upgrade other"},
		{"a host without approval", func(w *world) {
			h := w.host("cp-1")
			h.Annotations = nil
			_ = w.client.Update(context.Background(), h)
		}, "cp-1", stateWaiting, "waiting for the approval of the upgrade of ByoHost host-cp-1"},
		{"an approval of another version", func(w *world) {
			h := w.host("cp-1")
			h.Annotations[ApprovalAnnotation] = "v1.37.1"
			_ = w.client.Update(context.Background(), h)
		}, "cp-1", stateWaiting, "waiting for the approval"},
		{"a blocked drain", func(w *world) {
			w.nodes.drainErr = errors.New("Cannot evict pod as it would violate the pod's disruption budget")
		}, "cp-1", stateWaiting, "disruption budget"},
		{"a conflict", func(w *world) { w.conflict = true }, "cp-1", stateWaiting, "changed while the upgrade was requested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.bump("cp-1")
			tc.prepare(w)
			var resp *runtimehooksv1.UpdateMachineResponse
			if tc.machine == "uid" {
				resp = &runtimehooksv1.UpdateMachineResponse{}
				m := w.machine("cp-1")
				m.UID = "uid-replacement"
				w.h.DoUpdateMachine(context.Background(), &runtimehooksv1.UpdateMachineRequest{Desired: runtimehooksv1.UpdateMachineRequestObjects{Machine: *m}}, resp)
			} else {
				resp = w.update(tc.machine)
			}
			expect(t, resp, tc.want, tc.message)
			if tc.name != "an unfinished upgrade on the ByoHost" && tc.name != "a conflict" {
				if req := w.host("cp-1").Spec.InPlaceUpgrade; req != nil {
					t.Fatalf("a request was written: %+v", req)
				}
			}
		})
	}
}

func TestProgressNeverReportsAnUnverifiedUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  infrav1.InPlaceUpgradeStatus
		node    string
		readErr error
		want    state
		message string
	}{
		{"a timeout", infrav1.InPlaceUpgradeStatus{Phase: infrav1.InPlaceUpgradeRunning, Step: "Verify", StartedAt: &metav1.Time{Time: time.Date(2026, 11, 23, 20, 0, 0, 0, time.UTC)}}, toV, nil, stateFailed, "has run for more than"},
		{"a failed step", infrav1.InPlaceUpgradeStatus{Phase: infrav1.InPlaceUpgradeFailed, Step: "Download", Message: "not the pinned"}, toV, nil, stateFailed, "at step Download: not the pinned"},
		{"Succeeded without the observed Node version", infrav1.InPlaceUpgradeStatus{Phase: infrav1.InPlaceUpgradeSucceeded, ObservedKubeletVersion: toV}, toV, nil, stateFailed, "reports Succeeded with"},
		{"a Node with another version", infrav1.InPlaceUpgradeStatus{Phase: infrav1.InPlaceUpgradeSucceeded, ObservedKubeletVersion: toV, ObservedNodeVersion: toV}, from, nil, stateFailed, "but Node node-cp-1 reports v1.36.6"},
		{"an unreadable Node", infrav1.InPlaceUpgradeStatus{Phase: infrav1.InPlaceUpgradeSucceeded, ObservedKubeletVersion: toV, ObservedNodeVersion: toV}, "", errors.New("i/o timeout"), stateWaiting, "i/o timeout"},
		{"an unknown phase", infrav1.InPlaceUpgradeStatus{Phase: "Done"}, toV, nil, stateFailed, "unknown phase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.bump("cp-1")
			expect(t, w.update("cp-1"), stateWaiting, "requested")
			w.report("cp-1", tc.status)
			w.nodes.versions["node-cp-1"] = tc.node
			w.nodes.readErr = tc.readErr
			expect(t, w.update("cp-1"), tc.want, tc.message)
			if len(w.nodes.uncordoned) != 0 {
				t.Fatal("an unverified Node was uncordoned")
			}
		})
	}
}

func TestRequestOfAnotherBinding(t *testing.T) {
	w := newWorld(t)
	w.bump("cp-1")
	expect(t, w.update("cp-1"), stateWaiting, "requested")
	h := w.host("cp-1")
	h.Spec.InPlaceUpgrade.NodeName = "node-other"
	if err := w.client.Update(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	expect(t, w.update("cp-1"), stateFailed, "bound to another Machine")
}

func TestInfrastructureKinds(t *testing.T) {
	w := newWorld(t)
	m := w.machine("cp-1")
	m.Spec.InfrastructureRef.Kind = "DockerMachine"
	m.Spec.Version = toV
	call := func() *runtimehooksv1.UpdateMachineResponse {
		resp := &runtimehooksv1.UpdateMachineResponse{}
		w.h.DoUpdateMachine(context.Background(), &runtimehooksv1.UpdateMachineRequest{Desired: runtimehooksv1.UpdateMachineRequestObjects{Machine: *m}}, resp)
		return resp
	}
	expect(t, call(), stateFailed, "does not update DockerMachine Machines")

	w.h.SimulateDockerMachines = true
	expect(t, call(), stateWaiting, "simulated")
	w.now = w.now.Add(time.Minute)
	expect(t, call(), stateDone, "simulated in-place update complete")

	// The simulation never applies to a ByoMachine without Node access.
	w.h.Nodes = nil
	expect(t, w.update("cp-1"), stateFailed, "does not update ByoMachine Machines")
}

func TestCanUpdateMachine(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     string
		to       string
		simulate bool
		accept   bool
	}{
		{"a pinned upgrade of a ByoMachine", "ByoMachine", toV, false, true},
		{"an unpinned version", "ByoMachine", "v1.37.1", false, false},
		{"a downgrade", "ByoMachine", "v1.36.5", false, false},
		{"a skipped minor version", "ByoMachine", "v1.38.0", false, false},
		{"an unsupported infrastructure", "AWSMachine", toV, true, false},
		{"a DockerMachine without the simulation", "DockerMachine", toV, false, false},
		{"a simulated DockerMachine", "DockerMachine", toV, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.h.SimulateDockerMachines = tc.simulate
			current := w.machine("cp-1")
			current.Spec.InfrastructureRef.Kind = tc.kind
			desired := current.DeepCopy()
			desired.Spec.Version = tc.to
			resp := &runtimehooksv1.CanUpdateMachineResponse{}
			w.h.DoCanUpdateMachine(context.Background(), &runtimehooksv1.CanUpdateMachineRequest{
				Current: runtimehooksv1.CanUpdateMachineRequestObjects{Machine: *current},
				Desired: runtimehooksv1.CanUpdateMachineRequestObjects{Machine: *desired},
			}, resp)
			if resp.Status != runtimehooksv1.ResponseStatusSuccess {
				t.Fatal(resp.Message)
			}
			var ops []map[string]any
			if err := json.Unmarshal(resp.MachinePatch.Patch, &ops); err != nil {
				t.Fatal(err)
			}
			accepted := len(ops) == 1 && ops[0]["path"] == "/spec/version" && ops[0]["value"] == tc.to
			if accepted != tc.accept || (!tc.accept && len(ops) != 0) {
				t.Fatalf("patch %s", resp.MachinePatch.Patch)
			}
			if string(resp.InfrastructureMachinePatch.Patch) != "[]" || string(resp.BootstrapConfigPatch.Patch) != "[]" {
				t.Fatal("the extension patched the InfraMachine or the BootstrapConfig")
			}
		})
	}
}
