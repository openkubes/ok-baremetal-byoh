// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplaceupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
)

const (
	from        = "v1.36.6"
	to          = "v1.37.2"
	fingerprint = "DE15B14486CD377B9E876E1A234654DA9A296436"
	repo        = "https://pkgs.k8s.io/core:/stable:/v1.37/deb/"
)

// debContent is the content of the fake package of a name.
func debContent(name string) []byte { return []byte("deb of " + name + " " + to) }

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func request(role infrav1.InPlaceUpgradeRole) *infrav1.InPlaceUpgradeRequest {
	req := &infrav1.InPlaceUpgradeRequest{
		ID: "abc123", ClusterName: "ha1", ClusterUID: "uid-cluster",
		MachineName: "m1", MachineUID: "uid-m1", ByoMachineUID: "uid-bm1",
		NodeName: "host1", Role: role, FromVersion: from, ToVersion: to,
		Packages: infrav1.InPlaceUpgradePackages{Repository: repo, RepositoryKeyFingerprint: fingerprint},
	}
	for _, name := range []string{"kubeadm", "kubelet", "kubectl"} {
		req.Packages.Debs = append(req.Packages.Debs, infrav1.PinnedDeb{Name: name, Version: "1.37.2-1.1", SHA256: digest(debContent(name))})
	}
	return req
}

// fakeHost simulates the commands of a host.
type fakeHost struct {
	t           *testing.T
	manifests   string
	kubelet     string // the kubelet binary
	kubeadm     string
	server      string
	nodeVersion string // what the Node reports
	nodeReady   bool
	nodeStuck   bool // the Node never reports the new kubelet
	nodeErr     error
	keyring     string
	installed   map[string]string
	calls       []string
	failOnce    map[string]error // command prefix -> error, removed when used
	hang        string           // command prefix that blocks until the context ends
	fetched     []string
	badDigest   bool
	// The signed index of the fake repository.
	gpgvStatus     string // status of gpgv; a good signature of the pinned key by default
	indexDigests   map[string]string
	tamperPackages bool
}

// packagesFile is the Packages index of the fake repository.
func (h *fakeHost) packagesFile() string {
	var b strings.Builder
	for _, name := range []string{"kubeadm", "kubelet", "kubectl"} {
		sum := digest(debContent(name))
		if d, ok := h.indexDigests[name]; ok {
			sum = d
		}
		fmt.Fprintf(&b, "Package: %s\nVersion: 1.37.2-1.1\nArchitecture: amd64\nFilename: amd64/%s_1.37.2-1.1_amd64.deb\nSHA256: %s\nDescription: x\n second line\n\n", name, name, sum)
		fmt.Fprintf(&b, "Package: %s\nVersion: 1.37.2-1.1\nArchitecture: arm64\nFilename: arm64/%s_1.37.2-1.1_arm64.deb\nSHA256: %s\n\n", name, name, strings.Repeat("0", 64))
	}
	return b.String()
}

func newFakeHost(t *testing.T, controlPlane bool) *fakeHost {
	h := &fakeHost{
		t: t, kubelet: from, kubeadm: from, server: from, nodeVersion: from, nodeReady: true,
		keyring:   "pub:-:4096:1:234654DA9A296436:::::::::\nfpr:::::::::" + fingerprint + ":\nsub:-:4096:1:X:::::::::\nfpr:::::::::0000000000000000000000000000000000000000:\n",
		installed: map[string]string{"kubeadm": "1.36.6-1.1", "kubelet": "1.36.6-1.1", "kubectl": "1.36.6-1.1"},
		failOnce:  map[string]error{},
		manifests: filepath.Join(t.TempDir(), "manifests"),
	}
	if controlPlane {
		if err := os.MkdirAll(h.manifests, 0o700); err != nil {
			t.Fatal(err)
		}
		h.writeManifests(from)
	}
	return h
}

func (h *fakeHost) writeManifests(version string) {
	for _, c := range controlPlaneComponents {
		data := fmt.Sprintf("image: registry.k8s.io/%s:%s\n", c, version)
		if err := os.WriteFile(filepath.Join(h.manifests, c+".yaml"), []byte(data), 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *fakeHost) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	h.calls = append(h.calls, call)
	for prefix, err := range h.failOnce {
		if strings.HasPrefix(call, prefix) {
			delete(h.failOnce, prefix)
			return nil, err
		}
	}
	if h.hang != "" && strings.HasPrefix(call, h.hang) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	switch {
	case call == "kubelet --version":
		return []byte("Kubernetes " + h.kubelet + "\n"), nil
	case name == "kubectl" && slices.Contains(args, "version"):
		return json.Marshal(map[string]any{"serverVersion": map[string]string{"gitVersion": h.server}})
	case name == "kubectl" && slices.Contains(args, "node"):
		if h.nodeErr != nil {
			return nil, h.nodeErr
		}
		ready := "False"
		if h.nodeReady {
			ready = "True"
		}
		return json.Marshal(map[string]any{"status": map[string]any{
			"nodeInfo":   map[string]string{"kubeletVersion": h.nodeVersion},
			"conditions": []map[string]string{{"type": "Ready", "status": ready}},
		}})
	case name == "kubectl" && slices.Contains(args, "pod"):
		pod := args[len(args)-1]
		component := strings.TrimSuffix(pod, "-host1")
		data, err := os.ReadFile(filepath.Join(h.manifests, component+".yaml"))
		if err != nil {
			return nil, err
		}
		image := strings.TrimSpace(strings.TrimPrefix(string(data), "image: "))
		return json.Marshal(map[string]any{
			"spec":   map[string]any{"containers": []map[string]string{{"image": image}}},
			"status": map[string]any{"phase": "Running", "conditions": []map[string]string{{"type": "Ready", "status": "True"}}},
		})
	case name == "gpg":
		return []byte(h.keyring), nil
	case name == "gpgv":
		packages := []byte(h.packagesFile())
		release := fmt.Sprintf("Origin: test\nSHA256:\n %s %d Packages\n %s 10 Packages.gz\n", digest(packages), len(packages), strings.Repeat("1", 64))
		if data, err := os.ReadFile(args[len(args)-1]); err != nil || string(data) != release {
			return []byte("[GNUPG:] BADSIG X y\n"), nil
		}
		status := h.gpgvStatus
		if status == "" {
			status = "[GNUPG:] NEWSIG\n[GNUPG:] GOODSIG 234654DA9A296436 isv:kubernetes\n[GNUPG:] VALIDSIG DE15B14486CD377B9E876E1A234654DA9A296436 2026-10-28 0 4 0 1 8 00 " + fingerprint + "\n"
		}
		return []byte(status), nil
	case name == "apt-mark", call == "systemctl daemon-reload":
		return nil, nil
	case name == "dpkg":
		for _, path := range args[1:] {
			base := filepath.Base(path)
			parts := strings.Split(base, "_")
			h.installed[parts[0]] = parts[1]
			switch parts[0] {
			case "kubeadm":
				h.kubeadm = to
			case "kubelet":
				h.kubelet = to
			}
		}
		return nil, nil
	case name == "dpkg-query":
		return []byte(h.installed[args[len(args)-1]]), nil
	case call == "kubeadm version --output=short":
		return []byte(h.kubeadm + "\n"), nil
	case strings.HasPrefix(call, "kubeadm upgrade apply "+to+" --yes"), call == "kubeadm upgrade node":
		if _, err := os.Stat(h.manifests); err == nil {
			h.writeManifests(to)
		}
		return nil, nil
	case call == "systemctl restart kubelet":
		if !h.nodeStuck {
			h.nodeVersion = h.kubelet
		}
		return nil, nil
	case call == "systemctl is-active kubelet":
		return []byte("active\n"), nil
	}
	h.t.Fatalf("unexpected command %q", call)
	return nil, nil
}

func (h *fakeHost) Fetch(_ context.Context, url, path string) error {
	h.fetched = append(h.fetched, url)
	switch filepath.Base(url) {
	case "Release":
		packages := []byte(h.packagesFile())
		return os.WriteFile(path, []byte(fmt.Sprintf("Origin: test\nSHA256:\n %s %d Packages\n %s 10 Packages.gz\n", digest(packages), len(packages), strings.Repeat("1", 64))), 0o600)
	case "Release.gpg":
		return os.WriteFile(path, []byte("signature"), 0o600)
	case "Packages":
		data := h.packagesFile()
		if h.tamperPackages {
			data = strings.Replace(data, "Package: kubeadm", "Package: kubeadm\nX: y", 1)
		}
		return os.WriteFile(path, []byte(data), 0o600)
	}
	name := strings.Split(filepath.Base(url), "_")[0]
	data := debContent(name)
	if h.badDigest {
		data = []byte("tampered")
	}
	return os.WriteFile(path, data, 0o600)
}

func newExecutor(t *testing.T, h *fakeHost, stateDir string) *Executor {
	dir := t.TempDir()
	return &Executor{
		Runner: h, Fetcher: h, StateDir: stateDir, Hostname: "host1", Arch: "amd64",
		Paths:    Paths{Keyring: filepath.Join(dir, "keyring.gpg"), Sources: filepath.Join(dir, "byoh-kubernetes.sources"), Manifests: h.manifests},
		Timeouts: Timeouts{Step: time.Second, Download: time.Second, Upgrade: time.Second, Verify: 200 * time.Millisecond, VerifyPeriod: 10 * time.Millisecond},
	}
}

func assertNoReset(t *testing.T, h *fakeHost) {
	t.Helper()
	for _, call := range h.calls {
		if strings.Contains(call, "reset") || strings.Contains(call, "purge") || strings.Contains(call, "remove") {
			t.Fatalf("the executor ran %q", call)
		}
	}
}

func readCheckpoint(t *testing.T, dir string) checkpoint {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "inplace-upgrade-abc123.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cp checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		t.Fatal(err)
	}
	return cp
}

func stepOf(t *testing.T, err error) Step {
	t.Helper()
	var stepErr *StepError
	if !errors.As(err, &stepErr) {
		t.Fatalf("error %v is not a StepError", err)
	}
	return stepErr.Step
}

func TestWorkerUpgrade(t *testing.T) {
	h := newFakeHost(t, false)
	h.server = to
	state := t.TempDir()
	e := newExecutor(t, h, state)
	var steps []Step
	result, err := e.Execute(context.Background(), request(infrav1.InPlaceUpgradeWorker), func(s Step) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(err)
	}
	if result.KubeletVersion != to || result.NodeVersion != to {
		t.Fatalf("result %+v", result)
	}
	if !slices.Equal(steps, Steps) {
		t.Fatalf("steps %v", steps)
	}
	if !slices.Contains(h.calls, "kubeadm upgrade node") || slices.ContainsFunc(h.calls, func(c string) bool { return strings.HasPrefix(c, "kubeadm upgrade apply") }) {
		t.Fatalf("a worker runs kubeadm upgrade node only: %v", h.calls)
	}
	sources, err := os.ReadFile(e.Paths.Sources)
	if err != nil || !strings.Contains(string(sources), "URIs: "+repo) || !strings.Contains(string(sources), "Signed-By: "+KeyringPath) {
		t.Fatalf("sources %q %v", sources, err)
	}
	if cp := readCheckpoint(t, state); !slices.Equal(cp.Done, Steps) || cp.MachineUID != "uid-m1" {
		t.Fatalf("checkpoint %+v", cp)
	}
	assertNoReset(t, h)
}

func TestControlPlaneRoles(t *testing.T) {
	for role, command := range map[infrav1.InPlaceUpgradeRole]string{
		infrav1.InPlaceUpgradeControlPlaneFirst: "kubeadm upgrade apply v1.37.2 --yes",
		infrav1.InPlaceUpgradeControlPlane:      "kubeadm upgrade node",
	} {
		t.Run(string(role), func(t *testing.T) {
			h := newFakeHost(t, true)
			e := newExecutor(t, h, t.TempDir())
			if _, err := e.Execute(context.Background(), request(role), nil); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(h.calls, command) {
				t.Fatalf("no %q in %v", command, h.calls)
			}
			assertNoReset(t, h)
		})
	}
}

func TestControlPlaneWaitsForItsStaticPods(t *testing.T) {
	h := newFakeHost(t, true)
	e := newExecutor(t, h, t.TempDir())
	// kubeadm fails to write the new manifests, but exits 0: the
	// verification must not report success.
	e.Runner = runnerFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "kubeadm" && len(args) > 1 && args[1] == "node" {
			h.calls = append(h.calls, "kubeadm upgrade node (no-op)")
			return nil, nil
		}
		return h.Run(ctx, name, args...)
	})
	_, err := e.Execute(context.Background(), request(infrav1.InPlaceUpgradeControlPlane), nil)
	if err == nil || stepOf(t, err) != StepVerify || !strings.Contains(err.Error(), "not verified in time") {
		t.Fatalf("error %v", err)
	}
}

type runnerFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

func TestInterruptedUpgradeResumes(t *testing.T) {
	h := newFakeHost(t, false)
	h.server = to
	state := t.TempDir()
	h.failOnce["kubeadm upgrade node"] = errors.New("connection reset")
	_, err := newExecutor(t, h, state).Execute(context.Background(), request(infrav1.InPlaceUpgradeWorker), nil)
	if err == nil || stepOf(t, err) != StepKubeadmUpgrade {
		t.Fatalf("error %v", err)
	}
	if cp := readCheckpoint(t, state); !slices.Equal(cp.Done, []Step{StepPreflight, StepRepository, StepIndex, StepDownload, StepKubeadm}) {
		t.Fatalf("checkpoint %v", cp.Done)
	}
	// The agent restarts: the kubelet is still the old one, kubeadm the new
	// one, and the packages are downloaded.
	fetched, calls := len(h.fetched), len(h.calls)
	result, err := newExecutor(t, h, state).Execute(context.Background(), request(infrav1.InPlaceUpgradeWorker), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NodeVersion != to {
		t.Fatalf("result %+v", result)
	}
	if len(h.fetched) != fetched {
		t.Fatalf("the resumed upgrade downloaded again: %v", h.fetched[fetched:])
	}
	resumed := h.calls[calls:]
	if slices.ContainsFunc(resumed, func(c string) bool { return strings.Contains(c, "kubeadm_") || strings.HasPrefix(c, "gpg") }) {
		t.Fatalf("the resumed upgrade repeated a finished step: %v", resumed)
	}
	if resumed[0] != "kubeadm upgrade node" {
		t.Fatalf("the resumed upgrade started with %q", resumed[0])
	}
	assertNoReset(t, h)
}

func TestFinishedUpgradeIsVerifiedAgain(t *testing.T) {
	h := newFakeHost(t, false)
	h.server = to
	state := t.TempDir()
	if _, err := newExecutor(t, h, state).Execute(context.Background(), request(infrav1.InPlaceUpgradeWorker), nil); err != nil {
		t.Fatal(err)
	}
	h.nodeReady = false
	_, err := newExecutor(t, h, state).Execute(context.Background(), request(infrav1.InPlaceUpgradeWorker), nil)
	if err == nil || stepOf(t, err) != StepVerify {
		t.Fatalf("a NotReady node was reported as upgraded: %v", err)
	}
}

func TestFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		role    infrav1.InPlaceUpgradeRole
		prepare func(*fakeHost, *Executor, *infrav1.InPlaceUpgradeRequest)
		step    Step
		message string
	}{
		{"another host", infrav1.InPlaceUpgradeWorker, func(_ *fakeHost, e *Executor, _ *infrav1.InPlaceUpgradeRequest) { e.Hostname = "host2" }, StepPreflight, "not for host host2"},
		{"a worker before its control plane", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) { h.server = from }, StepPreflight, "only after the control plane"},
		{"an unexpected kubelet", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) { h.kubelet = "v1.35.0" }, StepPreflight, "kubelet is v1.35.0"},
		{"a control-plane request on a worker", infrav1.InPlaceUpgradeControlPlane, func(*fakeHost, *Executor, *infrav1.InPlaceUpgradeRequest) {}, StepPreflight, "static pods of a control-plane node"},
		{"an invalid request", infrav1.InPlaceUpgradeWorker, func(_ *fakeHost, _ *Executor, r *infrav1.InPlaceUpgradeRequest) { r.FromVersion = "v1.35.9" }, StepPreflight, "at most one minor version"},
		{"an unreachable API server", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) {
			h.failOnce["kubectl"] = errors.New("i/o timeout")
		}, StepPreflight, "i/o timeout"},
		{"a foreign repository key", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) {
			h.keyring = "pub:-:4096:1:X:::::::::\nfpr:::::::::1111111111111111111111111111111111111111:\n"
		}, StepRepository, "not only " + fingerprint},
		{"an extra repository key", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) {
			h.keyring += "pub:-:4096:1:Y:::::::::\nfpr:::::::::2222222222222222222222222222222222222222:\n"
		}, StepRepository, "not only " + fingerprint},
		{"a request digest that the repository has not signed", infrav1.InPlaceUpgradeWorker, func(_ *fakeHost, _ *Executor, r *infrav1.InPlaceUpgradeRequest) {
			r.Packages.Debs[0].SHA256 = digest([]byte("a forged kubeadm"))
		}, StepIndex, "the request pins"},
		{"a bad signature", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) {
			h.gpgvStatus = "[GNUPG:] BADSIG 234654DA9A296436 x\n"
		}, StepIndex, "BADSIG"},
		{"a signature of another key", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) {
			h.gpgvStatus = "[GNUPG:] GOODSIG X y\n[GNUPG:] VALIDSIG AAAA 2026-10-28 0 4 0 1 8 00 1111111111111111111111111111111111111111\n"
		}, StepIndex, "is not signed by the key"},
		{"a Packages file that Release does not list", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) { h.tamperPackages = true }, StepIndex, "the signed Release lists"},
		{"an index with another digest", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) {
			h.indexDigests = map[string]string{"kubelet": strings.Repeat("a", 64)}
		}, StepIndex, "the signed index lists kubelet"},
		{"a package for another architecture", infrav1.InPlaceUpgradeWorker, func(_ *fakeHost, e *Executor, _ *infrav1.InPlaceUpgradeRequest) { e.Arch = "s390x" }, StepIndex, "does not list kubeadm"},
		{"a tampered package", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) { h.badDigest = true }, StepDownload, "not the pinned"},
		{"a failed kubeadm upgrade", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) {
			h.failOnce["kubeadm upgrade node"] = errors.New("exit status 1")
		}, StepKubeadmUpgrade, "exit status 1"},
		{"a hanging kubeadm upgrade", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) { h.hang = "kubeadm upgrade node" }, StepKubeadmUpgrade, "deadline exceeded"},
		{"a Node that keeps the old kubelet", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) { h.nodeStuck = true }, StepVerify, "not verified in time"},
		{"a Node that cannot be read", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) {
			h.nodeErr = errors.New("Unauthorized")
		}, StepVerify, "Unauthorized"},
		{"a NotReady Node", infrav1.InPlaceUpgradeWorker, func(h *fakeHost, _ *Executor, _ *infrav1.InPlaceUpgradeRequest) { h.nodeReady = false }, StepVerify, "is not Ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost(t, false)
			h.server = to
			state := t.TempDir()
			e := newExecutor(t, h, state)
			req := request(tc.role)
			tc.prepare(h, e, req)
			result, err := e.Execute(context.Background(), req, nil)
			if err == nil {
				t.Fatalf("no error, result %+v", result)
			}
			if step := stepOf(t, err); step != tc.step || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error %v, want step %s with %q", err, tc.step, tc.message)
			}
			if tc.step == StepDownload {
				matches, _ := filepath.Glob(filepath.Join(state, "inplace-upgrade-abc123", "*.deb*"))
				if len(matches) != 0 {
					t.Fatalf("unverified files stayed: %v", matches)
				}
				if slices.ContainsFunc(h.calls, func(c string) bool { return strings.HasPrefix(c, "dpkg") }) {
					t.Fatal("a package was installed")
				}
			}
			if tc.step == StepIndex && slices.ContainsFunc(h.fetched, func(u string) bool { return strings.HasSuffix(u, ".deb") }) {
				t.Fatalf("a package was downloaded without a signed index: %v", h.fetched)
			}
			if tc.step == StepRepository {
				if _, err := os.Stat(e.Paths.Sources); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("the source was switched")
				}
			}
			assertNoReset(t, h)
		})
	}
}

func TestCheckpointOfAnotherUpgrade(t *testing.T) {
	for name, cp := range map[string]string{
		"another Machine": `{"id":"abc123","machineUID":"uid-m2","toVersion":"v1.37.2","role":"Worker","done":["Preflight","Repository","Download","Kubeadm","KubeadmUpgrade","Kubelet"]}`,
		"another version": `{"id":"abc123","machineUID":"uid-m1","toVersion":"v1.37.1","role":"Worker","done":["Preflight"]}`,
		"another role":    `{"id":"abc123","machineUID":"uid-m1","toVersion":"v1.37.2","role":"ControlPlaneFirst","done":["Preflight"]}`,
		"an unknown step": `{"id":"abc123","machineUID":"uid-m1","toVersion":"v1.37.2","role":"Worker","done":["Reset"]}`,
		"garbage":         `{`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newFakeHost(t, false)
			state := t.TempDir()
			if err := os.WriteFile(filepath.Join(state, "inplace-upgrade-abc123.json"), []byte(cp), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := newExecutor(t, h, state).Execute(context.Background(), request(infrav1.InPlaceUpgradeWorker), nil)
			if err == nil || stepOf(t, err) != StepPreflight {
				t.Fatalf("error %v", err)
			}
			if len(h.calls) != 0 {
				t.Fatalf("commands ran: %v", h.calls)
			}
		})
	}
}

func TestPrimaryFingerprints(t *testing.T) {
	out := "pub:-:4096:1:A:::::::::\nfpr:::::::::AAAA:\nuid:::\nsub:-:4096:1:B:::::::::\nfpr:::::::::BBBB:\npub:-:1:1:C:::::::::\nfpr:::::::::CCCC:\n"
	if got := primaryFingerprints([]byte(out)); !slices.Equal(got, []string{"AAAA", "CCCC"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSignatureStatus(t *testing.T) {
	ok := "[GNUPG:] GOODSIG K n\n[GNUPG:] VALIDSIG SUB 2026 0 4 0 1 8 00 " + fingerprint + "\n"
	if err := checkSignatureStatus([]byte(ok), fingerprint); err != nil {
		t.Fatal(err)
	}
	for name, status := range map[string]string{
		"no VALIDSIG":           "[GNUPG:] GOODSIG K n\n",
		"no GOODSIG":            "[GNUPG:] VALIDSIG SUB 2026 0 4 0 1 8 00 " + fingerprint + "\n",
		"an expired key":        ok + "[GNUPG:] EXPKEYSIG K n\n",
		"a missing key":         "[GNUPG:] ERRSIG K 1 8 00 2026 9\n[GNUPG:] NO_PUBKEY K\n",
		"the fingerprint early": "[GNUPG:] GOODSIG K n\n[GNUPG:] VALIDSIG " + fingerprint + " 2026 0 4 0 1 8 00 OTHER\n",
	} {
		if err := checkSignatureStatus([]byte(status), fingerprint); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReleaseEntry(t *testing.T) {
	release := []byte("MD5Sum:\n 00 1 Packages\nSHA256:\n " + strings.Repeat("b", 64) + " 27229 Packages\n " + strings.Repeat("c", 64) + " 5 Packages.gz\n")
	sum, size, err := releaseEntry(release, "Packages")
	if err != nil || sum != strings.Repeat("b", 64) || size != 27229 {
		t.Fatalf("%s %d %v", sum, size, err)
	}
	if _, _, err := releaseEntry([]byte("MD5Sum:\n 00 1 Packages\n"), "Packages"); err == nil {
		t.Fatal("an MD5 entry was accepted")
	}
}
