// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplaceupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	// UnitPrefix is the prefix of the transient systemd units in which the
	// executor runs the commands of an in-place upgrade (dpkg, kubeadm
	// upgrade). The rest of the name is the request, the attempt and the
	// command.
	UnitPrefix = "okx-inplace-"

	unitInactive    = "inactive"
	propActiveState = "ActiveState"
	cgroupRoot      = "/sys/fs/cgroup"
)

// unitName matches the name of a unit of an in-place upgrade inside a text.
var unitName = regexp.MustCompile(regexp.QuoteMeta(UnitPrefix) + `[A-Za-z0-9:_.@\\-]+`)

// UnitChecker tells whether a command of an in-place upgrade may still run on
// the host. It looks at every unit of an upgrade, whatever its request or
// attempt: the units outlive the agent, a step timeout and a failed attempt.
//
// It only reads. It never stops a unit and never removes anything.
type UnitChecker struct {
	Runner Runner
	// ReadFile reads a file of the control group file system; os.ReadFile
	// when nil.
	ReadFile func(string) ([]byte, error)
	// CgroupRoot is the mount point of the control groups; /sys/fs/cgroup
	// when empty.
	CgroupRoot string
}

type listedUnit struct {
	Unit string `json:"unit"`
}

// Busy returns the units of in-place upgrades that have not provably ended,
// each with the reason. An empty result means that no such unit runs, has a
// queued job or has processes left.
//
// An error means that the state of the host could not be established (systemd
// did not answer, or answered something that this code does not understand).
// The caller must treat it as busy.
func (c *UnitChecker) Busy(ctx context.Context) ([]string, error) {
	if c == nil || c.Runner == nil {
		return nil, errors.New("no access to systemd")
	}
	units, err := c.units(ctx)
	if err != nil {
		return nil, err
	}
	// A job that is queued for a unit (a start that has not begun, a stop
	// that has not finished) may not show in the state of the unit yet.
	jobs, err := c.jobs(ctx)
	if err != nil {
		return nil, err
	}

	var busy []string
	for _, unit := range jobs {
		busy = append(busy, unit+" (queued systemd job)")
	}
	for _, unit := range units {
		if slices.Contains(jobs, unit) {
			continue
		}
		reason, err := c.notEnded(ctx, unit)
		if err != nil {
			return nil, fmt.Errorf("state of %s: %w", unit, err)
		}
		if reason != "" {
			busy = append(busy, unit+" ("+reason+")")
		}
	}
	slices.Sort(busy)
	return busy, nil
}

// units returns every loaded unit of an in-place upgrade, also the inactive
// and the failed ones. It asks for JSON, so that an output that it cannot
// read is an error and never an empty list ("[]" is the empty list).
func (c *UnitChecker) units(ctx context.Context) ([]string, error) {
	out, err := c.Runner.Run(ctx, "systemctl", "list-units", "--all", "--output=json", "--no-pager", UnitPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	var listed []listedUnit
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, fmt.Errorf("systemctl list-units: unreadable output: %w", err)
	}
	units := make([]string, 0, len(listed))
	for _, l := range listed {
		if !strings.HasPrefix(l.Unit, UnitPrefix) {
			return nil, fmt.Errorf("systemctl list-units: unexpected unit %q", l.Unit)
		}
		units = append(units, l.Unit)
	}
	return units, nil
}

// jobs returns the units of in-place upgrades that the queue of systemd jobs
// names. systemd prints nothing when no job is queued, so the queue is read
// as text and searched for the unit names: an output in a form that this
// code does not know still shows a unit of an upgrade in it.
func (c *UnitChecker) jobs(ctx context.Context) ([]string, error) {
	out, err := c.Runner.Run(ctx, "systemctl", "list-jobs", "--no-legend", "--plain", "--no-pager")
	if err != nil {
		return nil, fmt.Errorf("systemctl list-jobs: %w", err)
	}
	var units []string
	for _, unit := range unitName.FindAllString(string(out), -1) {
		if !slices.Contains(units, unit) {
			units = append(units, unit)
		}
	}
	return units, nil
}

// notEnded returns why the unit has not provably ended, or "". A unit has
// ended when it is inactive or failed, has no main or control process, no
// queued job, and no process left in its control group. Each of these is
// read; none is assumed.
func (c *UnitChecker) notEnded(ctx context.Context, unit string) (string, error) {
	out, err := c.Runner.Run(ctx, "systemctl", "show",
		"--property="+propActiveState, "--property=SubState", "--property=MainPID", "--property=ControlPID",
		"--property=Job", "--property=ControlGroup", unit)
	if err != nil {
		return "", fmt.Errorf("systemctl show: %w", err)
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	for _, required := range []string{propActiveState, "MainPID", "ControlPID"} {
		if props[required] == "" {
			return "", fmt.Errorf("systemctl show: no %s", required)
		}
	}
	state := props[propActiveState]
	switch {
	case state != unitInactive && state != unitFailed:
		return fmt.Sprintf("%s/%s", state, props["SubState"]), nil
	case props["MainPID"] != "0":
		return "main process " + props["MainPID"], nil
	case props["ControlPID"] != "0":
		return "control process " + props["ControlPID"], nil
	case props["Job"] != "":
		return "queued systemd job " + props["Job"], nil
	}
	return c.cgroupProcesses(props["ControlGroup"])
}

// cgroupProcesses returns a reason when processes are left in the control
// group of a unit that systemd reports as ended.
func (c *UnitChecker) cgroupProcesses(group string) (string, error) {
	if group == "" {
		return "", nil
	}
	if !strings.HasPrefix(group, "/") || strings.Contains(group, "..") {
		return "", fmt.Errorf("unexpected control group %q", group)
	}
	root, read := c.CgroupRoot, c.ReadFile
	if root == "" {
		root = cgroupRoot
	}
	if read == nil {
		read = os.ReadFile
	}
	procs, err := read(filepath.Join(root, group, "cgroup.procs"))
	if errors.Is(err, os.ErrNotExist) {
		// systemd has removed the control group: nothing is left in it.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the control group %s: %w", group, err)
	}
	if pids := strings.Fields(string(procs)); len(pids) > 0 {
		return fmt.Sprintf("%d process(es) left in its control group", len(pids)), nil
	}
	return "", nil
}
