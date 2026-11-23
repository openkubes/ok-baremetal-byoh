// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

// Package inplaceupgrade upgrades Kubernetes on the host of the agent in
// place, for the request in spec.inPlaceUpgrade of its ByoHost.
//
// The upgrade is a fixed sequence of steps. Each finished step is recorded in
// a checkpoint file under the ID of the request, which binds it to the Machine
// and the target version, so that a restarted agent resumes the upgrade
// instead of repeating finished steps, and never applies the checkpoint of one
// upgrade to another. Every step either is idempotent or checks the host state
// before it changes it.
//
// The executor never resets the host, never removes the Kubernetes components
// and never touches the bootstrap of the node: a failed step stops the
// upgrade and is reported, and the host stays as it is for a forward recovery
// (docs/scaleway-kubeadm-e2e/inplace-upgrade-runbook.md).
package inplaceupgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
)

// Step is a step of an in-place upgrade.
type Step string

// The steps of an in-place upgrade, in their order.
const (
	StepPreflight      Step = "Preflight"
	StepRepository     Step = "Repository"
	StepIndex          Step = "Index"
	StepDownload       Step = "Download"
	StepKubeadm        Step = "Kubeadm"
	StepKubeadmUpgrade Step = "KubeadmUpgrade"
	StepKubelet        Step = "Kubelet"
	StepVerify         Step = "Verify"
)

// Steps are all the steps, in their order.
var Steps = []Step{StepPreflight, StepRepository, StepIndex, StepDownload, StepKubeadm, StepKubeadmUpgrade, StepKubelet, StepVerify}

// The files of the installer that the upgrade switches to the repository of
// the target version (installer/internal/algo/ubuntu.go). The uninstall
// script of the installer removes them, so the upgrade keeps their paths.
const (
	KeyringPath = "/etc/apt/keyrings/byoh-kubernetes.gpg"
	SourcesPath = "/etc/apt/sources.list.d/byoh-kubernetes.sources"
	// KubeletKubeconfig is the kubeconfig of the kubelet, with the identity
	// of the node. The node authorizer lets it read its Node and the pods on
	// it, which is all that the verification reads.
	KubeletKubeconfig = "/etc/kubernetes/kubelet.conf"
	// ManifestsDir holds the static pods of a control-plane node.
	ManifestsDir = "/etc/kubernetes/manifests"
)

// Values of Node and pod status, and the modes of the files of the upgrade.
const (
	conditionReady         = "Ready"
	conditionTrue          = "True"
	podRunning             = "Running"
	sourcesMode            = os.FileMode(0o644)
	stateDirMode           = os.FileMode(0o700)
	stateFileMode          = os.FileMode(0o600)
	majorMinorParts        = 2
	unitFailed             = "failed"
	unitActive             = "active"
	unitDeactivating       = "deactivating"
	releaseFile            = "Release"
	maxPackagesLineBytes   = 1024 * 1024
	initialPackagesBufSize = 64 * 1024
)

// unitRunningStates are the ActiveStates of a systemd unit whose command has
// not finished.
var unitRunningStates = []string{unitActive, "activating", unitDeactivating, "reloading"}

// controlPlaneComponents are the static pods that kubeadm upgrades on a
// control-plane node.
var controlPlaneComponents = []string{"kube-apiserver", "kube-controller-manager", "kube-scheduler"}

// Paths are the files of the host that the upgrade reads or writes.
type Paths struct {
	Keyring   string
	Sources   string
	Manifests string
}

// DefaultPaths are the paths on a host.
var DefaultPaths = Paths{Keyring: KeyringPath, Sources: SourcesPath, Manifests: ManifestsDir}

// Runner runs a command without a shell and returns its standard output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Fetcher downloads a URL into a file.
type Fetcher interface {
	Fetch(ctx context.Context, url, path string) error
}

// Timeouts bound the steps. A step that runs out of time fails; it is never
// taken as done.
type Timeouts struct {
	Step         time.Duration
	Download     time.Duration
	Upgrade      time.Duration
	Verify       time.Duration
	VerifyPeriod time.Duration
}

// DefaultTimeouts are the timeouts of the agent.
var DefaultTimeouts = Timeouts{
	Step:         2 * time.Minute,
	Download:     10 * time.Minute,
	Upgrade:      20 * time.Minute,
	Verify:       10 * time.Minute,
	VerifyPeriod: 5 * time.Second,
}

// Executor upgrades the host.
type Executor struct {
	Runner  Runner
	Fetcher Fetcher
	// StateDir holds the checkpoints and the downloaded packages, in the
	// working directory of the agent (/var/lib/byoh-hostagent).
	StateDir string
	// Hostname is the name of the host, which must be the Node of the
	// request.
	Hostname string
	// Arch is the architecture of the packages; runtime.GOARCH by default.
	Arch     string
	Timeouts Timeouts
	// Paths are the files of the host; DefaultPaths when empty.
	Paths Paths
	// Sleep waits between the polls of the verification; time.Sleep with the
	// context by default.
	Sleep func(context.Context, time.Duration) error
}

func (e *Executor) paths() Paths {
	if e.Paths == (Paths{}) {
		return DefaultPaths
	}
	return e.Paths
}

// Result is what the executor verified on the host.
type Result struct {
	KubeletVersion string
	NodeVersion    string
}

// StepError is a failed step.
type StepError struct {
	Step Step
	Err  error
}

func (e *StepError) Error() string { return fmt.Sprintf("step %s: %v", e.Step, e.Err) }
func (e *StepError) Unwrap() error { return e.Err }

// checkpoint is the durable state of an upgrade.
type checkpoint struct {
	ID         string `json:"id"`
	MachineUID string `json:"machineUID"`
	ToVersion  string `json:"toVersion"`
	Role       string `json:"role"`
	Done       []Step `json:"done"`
}

// Execute runs the steps of the request that are not done yet, and returns
// what it verified. It calls onStep, which may be nil, before each step that
// runs, for the progress in the status of the ByoHost. An error is a
// *StepError.
func (e *Executor) Execute(ctx context.Context, req *infrav1.InPlaceUpgradeRequest, onStep func(Step)) (Result, error) {
	if req == nil {
		return Result{}, &StepError{StepPreflight, errors.New("no request")}
	}
	cp, err := e.loadCheckpoint(req)
	if err != nil {
		return Result{}, &StepError{StepPreflight, err}
	}
	var result Result
	for _, step := range Steps {
		// The verification always runs: a finished upgrade is reported only
		// with the versions that the agent observes now.
		if step != StepVerify && slices.Contains(cp.Done, step) {
			continue
		}
		if onStep != nil {
			onStep(step)
		}
		if err := e.runStep(ctx, step, req, &result); err != nil {
			return result, &StepError{step, err}
		}
		if !slices.Contains(cp.Done, step) {
			cp.Done = append(cp.Done, step)
			if err := e.saveCheckpoint(cp); err != nil {
				return result, &StepError{step, err}
			}
		}
	}
	return result, nil
}

func (e *Executor) runStep(ctx context.Context, step Step, req *infrav1.InPlaceUpgradeRequest, result *Result) error {
	t := e.timeouts()
	timeout := t.Step
	switch step {
	case StepDownload, StepIndex:
		timeout = t.Download
	case StepKubeadmUpgrade, StepKubeadm, StepKubelet:
		timeout = t.Upgrade
	case StepVerify:
		timeout = t.Verify
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch step {
	case StepPreflight:
		return e.preflight(ctx, req)
	case StepRepository:
		return e.repository(ctx, req)
	case StepIndex:
		return e.index(ctx, req)
	case StepDownload:
		return e.download(ctx, req)
	case StepKubeadm:
		return e.install(ctx, req, "kubeadm")
	case StepKubeadmUpgrade:
		return e.kubeadmUpgrade(ctx, req)
	case StepKubelet:
		return e.kubelet(ctx, req)
	case StepVerify:
		return e.verify(ctx, req, result)
	}
	return fmt.Errorf("unknown step %s", step)
}

func (e *Executor) timeouts() Timeouts {
	t := e.Timeouts
	if t.Step == 0 {
		t = DefaultTimeouts
	}
	return t
}

// preflight checks that the request is for this host and its current
// versions, and the version skew: a kubelet is never newer than the API
// server, so a worker waits for the control plane.
func (e *Executor) preflight(ctx context.Context, req *infrav1.InPlaceUpgradeRequest) error {
	if errs := infrav1.ValidateInPlaceUpgradeRequest(&infrav1.ByoHostSpec{
		ByoMachineRef:  &infrav1.ByoMachineReference{Name: "-", UID: req.ByoMachineUID},
		InPlaceUpgrade: req,
	}); len(errs) > 0 {
		return errs.ToAggregate()
	}
	if req.NodeName != e.Hostname {
		return fmt.Errorf("the request is for node %s, not for host %s", req.NodeName, e.Hostname)
	}
	kubelet, err := e.kubeletVersion(ctx)
	if err != nil {
		return err
	}
	if kubelet != req.FromVersion && kubelet != req.ToVersion {
		return fmt.Errorf("kubelet is %s, the upgrade is from %s to %s", kubelet, req.FromVersion, req.ToVersion)
	}
	server, err := e.serverVersion(ctx)
	if err != nil {
		return err
	}
	if req.Role == infrav1.InPlaceUpgradeWorker && minorLess(server, req.ToVersion) {
		return fmt.Errorf("the API server is %s: a worker is upgraded to %s only after the control plane", server, req.ToVersion)
	}
	if req.Role != infrav1.InPlaceUpgradeWorker {
		if _, err := os.Stat(filepath.Join(e.paths().Manifests, "kube-apiserver.yaml")); err != nil {
			return fmt.Errorf("a control-plane upgrade needs the static pods of a control-plane node: %w", err)
		}
	}
	return nil
}

// repository checks the keyring of the installer against the pinned
// fingerprint and switches the apt source to the repository of the target.
func (e *Executor) repository(ctx context.Context, req *infrav1.InPlaceUpgradeRequest) error {
	p := e.paths()
	out, err := e.Runner.Run(ctx, "gpg", "--show-keys", "--with-colons", p.Keyring)
	if err != nil {
		return fmt.Errorf("reading the keyring %s: %w", p.Keyring, err)
	}
	fingerprints := primaryFingerprints(out)
	if len(fingerprints) != 1 || fingerprints[0] != req.Packages.RepositoryKeyFingerprint {
		return fmt.Errorf("the keyring %s has the keys %v, not only %s", p.Keyring, fingerprints, req.Packages.RepositoryKeyFingerprint)
	}
	// The source names the keyring by its path on the host, as the
	// installer writes it.
	sources := fmt.Sprintf("Types: deb\nURIs: %s\nSuites: /\nSigned-By: %s\n", req.Packages.Repository, KeyringPath)
	return writeFileAtomic(p.Sources, []byte(sources), sourcesMode)
}

// primaryFingerprints returns the fingerprints of the primary keys in the
// output of gpg --with-colons: the fpr record that follows a pub record.
func primaryFingerprints(out []byte) []string {
	var fprs []string
	afterPub := false
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ":")
		switch fields[0] {
		case "pub":
			afterPub = true
		case "fpr":
			if afterPub && len(fields) > 9 {
				fprs = append(fprs, fields[9])
			}
			afterPub = false
		case "sub":
			afterPub = false
		}
	}
	return fprs
}

func (e *Executor) arch() string {
	if e.Arch != "" {
		return e.Arch
	}
	return runtime.GOARCH
}

func (e *Executor) debPath(req *infrav1.InPlaceUpgradeRequest, deb *infrav1.PinnedDeb) string {
	return filepath.Join(e.StateDir, "inplace-upgrade-"+req.ID, fmt.Sprintf("%s_%s_%s.deb", deb.Name, deb.Version, e.arch()))
}

// download fetches the pinned packages and verifies their digests. A package
// that is already there with the pinned digest is kept; one with another
// digest is removed and the step fails, so that nothing unverified stays.
func (e *Executor) download(ctx context.Context, req *infrav1.InPlaceUpgradeRequest) error {
	for i := range req.Packages.Debs {
		deb := &req.Packages.Debs[i]
		path := e.debPath(req, deb)
		if err := os.MkdirAll(filepath.Dir(path), stateDirMode); err != nil {
			return err
		}
		if sum, err := fileSHA256(path); err == nil && sum == deb.SHA256 {
			continue
		}
		url := fmt.Sprintf("%s%s/%s", req.Packages.Repository, e.arch(), filepath.Base(path))
		tmp := path + ".part"
		if err := e.Fetcher.Fetch(ctx, url, tmp); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("downloading %s: %w", url, err)
		}
		sum, err := fileSHA256(tmp)
		if err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if sum != deb.SHA256 {
			_ = os.Remove(tmp)
			return fmt.Errorf("%s has the SHA256 %s, not the pinned %s", url, sum, deb.SHA256)
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}

// install installs pinned packages that were downloaded and verified, after
// it checks their digests again, and holds them, as the installer does.
func (e *Executor) install(ctx context.Context, req *infrav1.InPlaceUpgradeRequest, names ...string) error {
	paths := make([]string, 0, len(names))
	for _, name := range names {
		deb := req.Packages.Deb(name)
		if deb == nil {
			return fmt.Errorf("no pinned %s package", name)
		}
		path := e.debPath(req, deb)
		sum, err := fileSHA256(path)
		if err != nil {
			return err
		}
		if sum != deb.SHA256 {
			return fmt.Errorf("%s has the SHA256 %s, not the pinned %s", path, sum, deb.SHA256)
		}
		paths = append(paths, path)
	}
	if _, err := e.Runner.Run(ctx, "apt-mark", append([]string{"unhold"}, names...)...); err != nil {
		return err
	}
	// The packages are held again also after a failed installation, so that
	// no later apt run moves them.
	installErr := e.durable(ctx, e.unit(req, names[0]), "dpkg", append([]string{"--install"}, paths...)...)
	if _, err := e.Runner.Run(ctx, "apt-mark", append([]string{"hold"}, names...)...); err != nil && installErr == nil {
		return err
	}
	if installErr != nil {
		return installErr
	}
	for _, name := range names {
		deb := req.Packages.Deb(name)
		out, err := e.Runner.Run(ctx, "dpkg-query", "--show", "--showformat=${Version}", name)
		if err != nil {
			return err
		}
		if got := strings.TrimSpace(string(out)); got != deb.Version {
			return fmt.Errorf("%s %s is installed, not %s", name, got, deb.Version)
		}
	}
	if slices.Contains(names, "kubeadm") {
		out, err := e.Runner.Run(ctx, "kubeadm", "version", "--output=short")
		if err != nil {
			return err
		}
		if got := strings.TrimSpace(string(out)); got != req.ToVersion {
			return fmt.Errorf("kubeadm is %s, not %s", got, req.ToVersion)
		}
	}
	return nil
}

// kubeadmUpgrade upgrades the node with kubeadm: kubeadm upgrade apply on the
// first control-plane node, kubeadm upgrade node on the others. Both can run
// again after an interruption.
func (e *Executor) kubeadmUpgrade(ctx context.Context, req *infrav1.InPlaceUpgradeRequest) error {
	unit := e.unit(req, "kubeadm-upgrade")
	if req.Role == infrav1.InPlaceUpgradeControlPlaneFirst {
		return e.durable(ctx, unit, "kubeadm", "upgrade", "apply", req.ToVersion, "--yes")
	}
	return e.durable(ctx, unit, "kubeadm", "upgrade", "node")
}

// unit names the transient systemd unit of a durable command of the request,
// per attempt: a failed unit stays for the journal and never blocks the next
// attempt.
func (e *Executor) unit(req *infrav1.InPlaceUpgradeRequest, what string) string {
	return fmt.Sprintf("okx-inplace-%s-%d-%s.service", req.ID, req.Attempt, what)
}

// durable runs a command that must not be interrupted (dpkg, kubeadm upgrade)
// in a transient systemd unit. The unit outlives the agent (a restart or an
// update of the agent does not kill it) and a step timeout only stops the
// waiting, never the command. A unit of the same attempt that still runs, from
// an earlier agent process, is waited for instead of started again; one that
// failed meanwhile fails the step.
func (e *Executor) durable(ctx context.Context, unit, name string, args ...string) error {
	state, result, err := e.unitState(ctx, unit)
	for err == nil && slices.Contains(unitRunningStates, state) {
		if err = e.sleep(ctx, e.timeouts().VerifyPeriod); err != nil {
			return fmt.Errorf("%s still runs in %s; it was not stopped: %w", name, unit, err)
		}
		state, result, err = e.unitState(ctx, unit)
	}
	if err != nil {
		return err
	}
	if state == unitFailed {
		return fmt.Errorf("%s failed in %s (%s) while the agent was not waiting; see journalctl -u %s", name, unit, result, unit)
	}
	_, err = e.Runner.Run(ctx, "systemd-run", append([]string{"--unit=" + unit, "--wait", "--quiet", "--property=TimeoutStartSec=infinity", "--", name}, args...)...)
	if ctx.Err() != nil {
		return fmt.Errorf("%s still runs in %s after the step timeout; it was not stopped: %w", name, unit, ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("%s failed in %s (journalctl -u %s): %w", name, unit, unit, err)
	}
	return nil
}

// unitState returns the ActiveState and the Result of a systemd unit; an
// unknown unit is inactive.
func (e *Executor) unitState(ctx context.Context, unit string) (state, result string, err error) {
	out, err := e.Runner.Run(ctx, "systemctl", "show", "--property=ActiveState", "--property=Result", unit)
	if err != nil {
		return "", "", fmt.Errorf("reading the state of %s: %w", unit, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			switch k {
			case "ActiveState":
				state = v
			case "Result":
				result = v
			}
		}
	}
	if state == "" {
		return "", "", fmt.Errorf("no ActiveState of %s", unit)
	}
	return state, result, nil
}

// kubelet installs the pinned kubelet and kubectl and restarts the kubelet.
func (e *Executor) kubelet(ctx context.Context, req *infrav1.InPlaceUpgradeRequest) error {
	if err := e.install(ctx, req, "kubelet", "kubectl"); err != nil {
		return err
	}
	if _, err := e.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, err := e.Runner.Run(ctx, "systemctl", "restart", "kubelet")
	return err
}

// verify waits until the host, its Node and, on a control-plane node, its
// static pods run the target version. It reports success only with the
// versions that it has observed.
func (e *Executor) verify(ctx context.Context, req *infrav1.InPlaceUpgradeRequest, result *Result) error {
	var last error
	for {
		last = e.verifyOnce(ctx, req, result)
		if last == nil {
			return nil
		}
		if err := e.sleep(ctx, e.timeouts().VerifyPeriod); err != nil {
			return fmt.Errorf("not verified in time: %w", last)
		}
	}
}

func (e *Executor) verifyOnce(ctx context.Context, req *infrav1.InPlaceUpgradeRequest, result *Result) error {
	kubelet, err := e.kubeletVersion(ctx)
	if err != nil {
		return err
	}
	result.KubeletVersion = kubelet
	if kubelet != req.ToVersion {
		return fmt.Errorf("kubelet is %s, not %s", kubelet, req.ToVersion)
	}
	if out, err := e.Runner.Run(ctx, "systemctl", "is-active", "kubelet"); err != nil || strings.TrimSpace(string(out)) != unitActive {
		return fmt.Errorf("the kubelet is not active: %q %v", strings.TrimSpace(string(out)), err)
	}
	var node struct {
		Status struct {
			NodeInfo struct {
				KubeletVersion string `json:"kubeletVersion"`
			} `json:"nodeInfo"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := e.kubectlJSON(ctx, &node, "get", "node", req.NodeName); err != nil {
		return err
	}
	result.NodeVersion = node.Status.NodeInfo.KubeletVersion
	if result.NodeVersion != req.ToVersion {
		return fmt.Errorf("node %s reports kubelet %s, not %s", req.NodeName, result.NodeVersion, req.ToVersion)
	}
	ready := false
	for _, c := range node.Status.Conditions {
		if c.Type == conditionReady {
			ready = c.Status == conditionTrue
		}
	}
	if !ready {
		return fmt.Errorf("node %s is not Ready", req.NodeName)
	}
	if req.Role == infrav1.InPlaceUpgradeWorker {
		return nil
	}
	for _, component := range controlPlaneComponents {
		if err := e.verifyStaticPod(ctx, req, component); err != nil {
			return err
		}
	}
	return nil
}

// verifyStaticPod checks the manifest and the mirror pod of a control-plane
// component: the image of the target version, running and ready.
func (e *Executor) verifyStaticPod(ctx context.Context, req *infrav1.InPlaceUpgradeRequest, component string) error {
	manifest, err := os.ReadFile(filepath.Join(e.paths().Manifests, component+".yaml"))
	if err != nil {
		return err
	}
	if !bytes.Contains(manifest, []byte("/"+component+":"+req.ToVersion)) {
		return fmt.Errorf("the manifest of %s does not have the image of %s", component, req.ToVersion)
	}
	var pod struct {
		Spec struct {
			Containers []struct {
				Image string `json:"image"`
			} `json:"containers"`
		} `json:"spec"`
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := e.kubectlJSON(ctx, &pod, "--namespace=kube-system", "get", "pod", component+"-"+req.NodeName); err != nil {
		return err
	}
	if len(pod.Spec.Containers) != 1 || !strings.HasSuffix(pod.Spec.Containers[0].Image, "/"+component+":"+req.ToVersion) {
		return fmt.Errorf("the pod of %s does not run %s", component, req.ToVersion)
	}
	ready := false
	for _, c := range pod.Status.Conditions {
		if c.Type == conditionReady {
			ready = c.Status == conditionTrue
		}
	}
	if pod.Status.Phase != podRunning || !ready {
		return fmt.Errorf("the pod of %s is not running and ready", component)
	}
	return nil
}

func (e *Executor) kubectlJSON(ctx context.Context, into any, args ...string) error {
	out, err := e.Runner.Run(ctx, "kubectl", append([]string{"--kubeconfig=" + KubeletKubeconfig, "--request-timeout=10s", "--output=json"}, args...)...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, into)
}

// kubeletVersion returns the version of the kubelet binary, from
// "Kubernetes v1.37.2".
func (e *Executor) kubeletVersion(ctx context.Context) (string, error) {
	out, err := e.Runner.Run(ctx, "kubelet", "--version")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[0] != "Kubernetes" {
		return "", fmt.Errorf("unexpected kubelet --version output %q", strings.TrimSpace(string(out)))
	}
	return fields[1], nil
}

// serverVersion returns the version of the API server that the kubelet uses.
func (e *Executor) serverVersion(ctx context.Context) (string, error) {
	var version struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if err := e.kubectlJSON(ctx, &version, "version"); err != nil {
		return "", err
	}
	if version.ServerVersion.GitVersion == "" {
		return "", errors.New("no server version")
	}
	return version.ServerVersion.GitVersion, nil
}

// minorLess returns whether the minor version of a is less than that of b.
func minorLess(a, b string) bool {
	minor := func(v string) int {
		parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
		if len(parts) < majorMinorParts {
			return -1
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			return -1
		}
		return n
	}
	return minor(a) < minor(b)
}

func (e *Executor) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *Executor) checkpointPath(id string) string {
	return filepath.Join(e.StateDir, "inplace-upgrade-"+id+".json")
}

// loadCheckpoint returns the checkpoint of the request, or a new one. A
// checkpoint of the ID that is bound to another Machine, version or role is
// an error: it is never applied to another upgrade.
func (e *Executor) loadCheckpoint(req *infrav1.InPlaceUpgradeRequest) (*checkpoint, error) {
	fresh := &checkpoint{ID: req.ID, MachineUID: string(req.MachineUID), ToVersion: req.ToVersion, Role: string(req.Role)}
	data, err := os.ReadFile(e.checkpointPath(req.ID))
	if errors.Is(err, os.ErrNotExist) {
		return fresh, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the checkpoint: %w", err)
	}
	cp := &checkpoint{}
	if err := json.Unmarshal(data, cp); err != nil {
		return nil, fmt.Errorf("the checkpoint is unreadable: %w", err)
	}
	if cp.ID != fresh.ID || cp.MachineUID != fresh.MachineUID || cp.ToVersion != fresh.ToVersion || cp.Role != fresh.Role {
		return nil, fmt.Errorf("the checkpoint %s belongs to another upgrade (Machine %s, %s, %s)", e.checkpointPath(req.ID), cp.MachineUID, cp.ToVersion, cp.Role)
	}
	for _, step := range cp.Done {
		if !slices.Contains(Steps, step) {
			return nil, fmt.Errorf("the checkpoint has the unknown step %q", step)
		}
	}
	return cp, nil
}

func (e *Executor) saveCheckpoint(cp *checkpoint) error {
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return writeFileAtomic(e.checkpointPath(cp.ID), data, stateFileMode)
}

// writeFileAtomic writes a file through a temporary file in its directory,
// so that the file never has a part of the data.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
