// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplaceupgrade

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	infrav1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
)

// systemd answers the three questions of the checker from these fields. The
// forms of the answers are those of systemd 255 (Ubuntu 24.04).
type fakeSystemd struct {
	listUnits    string // output of list-units --output=json
	listUnitsErr error
	listJobs     string // output of list-jobs
	listJobsErr  error
	show         map[string]string // unit -> output of show
	showErr      error
	cgroups      map[string]string // path of cgroup.procs -> content
	cgroupErr    error
	calls        []string
}

func (f *fakeSystemd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "systemctl" {
		return nil, errors.New("unexpected command " + name)
	}
	switch args[0] {
	case "list-units":
		return []byte(f.listUnits), f.listUnitsErr
	case "list-jobs":
		return []byte(f.listJobs), f.listJobsErr
	case "show":
		if f.showErr != nil {
			return nil, f.showErr
		}
		return []byte(f.show[args[len(args)-1]]), nil
	}
	return nil, errors.New("unexpected systemctl " + args[0])
}

func (f *fakeSystemd) readFile(path string) ([]byte, error) {
	if f.cgroupErr != nil {
		return nil, f.cgroupErr
	}
	content, ok := f.cgroups[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(content), nil
}

const (
	unitA0 = "okx-inplace-efbe475768eb7c3ac4e3-0-kubeadm.service"
	unitA1 = "okx-inplace-efbe475768eb7c3ac4e3-1-kubeadm-upgrade.service"
	unitB0 = "okx-inplace-30712a53ad616b4a9b19-0-kubelet.service"
)

func listed(units ...string) string {
	entries := make([]string, 0, len(units))
	for _, u := range units {
		entries = append(entries, `{"unit":"`+u+`","load":"loaded","active":"x","sub":"x","description":"x"}`)
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func shown(state, sub, mainPID, controlPID, job, cgroup string) string {
	return "MainPID=" + mainPID + "\nControlPID=" + controlPID + "\nControlGroup=" + cgroup + "\nActiveState=" + state + "\nSubState=" + sub + "\nJob=" + job + "\n"
}

// failedIn is a failed unit without processes, with the given control group.
func failedIn(cgroup string) string {
	return shown(unitFailed, unitFailed, "0", "0", "", cgroup)
}

var (
	ended   = shown("inactive", "dead", "0", "0", "", "")
	failed  = failedIn("")
	running = shown("active", "running", "4711", "0", "", "/system.slice/"+unitA0)
)

//nolint:funlen // One table: what counts as ended, what as busy and what as unknown.
func TestUnitCheckerBusy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		systemd   fakeSystemd
		wantBusy  []string // substrings, one per busy unit
		wantError string
	}{
		{name: "no unit of an upgrade", systemd: fakeSystemd{listUnits: "[]"}},
		{name: "a failed unit that has ended (as left on a host by a failed attempt)",
			systemd: fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: failed}}},
		{name: "ended units of two attempts and of another request",
			systemd: fakeSystemd{listUnits: listed(unitA0, unitA1, unitB0), show: map[string]string{unitA0: failed, unitA1: ended, unitB0: ended}}},
		{name: "a running unit",
			systemd:  fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: running}},
			wantBusy: []string{unitA0 + " (active/running)"}},
		{name: "a running unit of an earlier attempt beside an ended later one",
			systemd:  fakeSystemd{listUnits: listed(unitA0, unitA1), show: map[string]string{unitA0: running, unitA1: ended}},
			wantBusy: []string{unitA0 + " (active/running)"}},
		{name: "a running unit of another request",
			systemd:  fakeSystemd{listUnits: listed(unitA0, unitB0), show: map[string]string{unitA0: failed, unitB0: shown("activating", "start", "0", "812", "", "")}},
			wantBusy: []string{unitB0 + " (activating/start)"}},
		{name: "a unit that is stopping",
			systemd:  fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: shown("deactivating", "stop-sigterm", "4711", "0", "", "")}},
			wantBusy: []string{"deactivating/stop-sigterm"}},
		{name: "a state that this code does not know",
			systemd:  fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: shown("maintenance", "x", "0", "0", "", "")}},
			wantBusy: []string{"maintenance/x"}},
		{name: "an ended unit with a main process",
			systemd:  fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: shown(unitFailed, unitFailed, "4711", "0", "", "")}},
			wantBusy: []string{"main process 4711"}},
		{name: "an ended unit with a control process",
			systemd:  fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: shown("inactive", "dead", "0", "99", "", "")}},
			wantBusy: []string{"control process 99"}},
		{name: "an ended unit with a queued job in its properties",
			systemd:  fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: shown("inactive", "dead", "0", "0", "1234", "")}},
			wantBusy: []string{"queued systemd job 1234"}},
		{name: "a job in the queue of systemd",
			systemd:  fakeSystemd{listUnits: listed(unitA0), listJobs: "1234 " + unitA0 + " start waiting\n", show: map[string]string{unitA0: ended}},
			wantBusy: []string{unitA0 + " (queued systemd job)"}},
		{name: "a job in the queue for a unit that is not listed, in another output form",
			systemd:  fakeSystemd{listUnits: "[]", listJobs: `[{"job":7,"unit":"` + unitB0 + `","type":"start","state":"waiting"}]`},
			wantBusy: []string{unitB0 + " (queued systemd job)"}},
		{name: "jobs of other units do not count",
			systemd: fakeSystemd{listUnits: "[]", listJobs: "12 kubelet.service restart running\n"}},
		{name: "a failed unit with processes left in its control group",
			systemd: fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: failedIn("/system.slice/" + unitA0)},
				cgroups: map[string]string{"/sys/fs/cgroup/system.slice/" + unitA0 + "/cgroup.procs": "4712\n4713\n"}},
			wantBusy: []string{"2 process(es) left in its control group"}},
		{name: "a failed unit with an empty control group",
			systemd: fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: failedIn("/system.slice/" + unitA0)},
				cgroups: map[string]string{"/sys/fs/cgroup/system.slice/" + unitA0 + "/cgroup.procs": ""}}},
		{name: "a failed unit whose control group is gone",
			systemd: fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: failedIn("/system.slice/" + unitA0)}}},

		{name: "systemd does not answer the unit listing", systemd: fakeSystemd{listUnitsErr: errors.New("Failed to connect to bus")}, wantError: "list-units"},
		{name: "an empty unit listing is not an empty list", systemd: fakeSystemd{listUnits: ""}, wantError: "unreadable output"},
		{name: "a unit listing in another form", systemd: fakeSystemd{listUnits: unitA0 + " loaded inactive dead\n"}, wantError: "unreadable output"},
		{name: "a unit listing with a foreign unit", systemd: fakeSystemd{listUnits: listed("kubelet.service")}, wantError: "unexpected unit"},
		{name: "systemd does not answer the job listing", systemd: fakeSystemd{listUnits: "[]", listJobsErr: errors.New("timeout")}, wantError: "list-jobs"},
		{name: "systemd does not answer for a unit", systemd: fakeSystemd{listUnits: listed(unitA0), showErr: errors.New("timeout")}, wantError: "systemctl show"},
		{name: "no state of a unit", systemd: fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: "MainPID=0\nControlPID=0\n"}}, wantError: "no ActiveState"},
		{name: "no process properties of a unit", systemd: fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: "ActiveState=inactive\n"}}, wantError: "no MainPID"},
		{name: "an unreadable control group",
			systemd:   fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: failedIn("/system.slice/x")}, cgroupErr: errors.New("permission denied")},
			wantError: "reading the control group"},
		{name: "a control group outside the hierarchy",
			systemd:   fakeSystemd{listUnits: listed(unitA0), show: map[string]string{unitA0: failedIn("/system.slice/../../etc")}},
			wantError: "unexpected control group"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			systemd := tc.systemd
			checker := &UnitChecker{Runner: &systemd, ReadFile: systemd.readFile}
			busy, err := checker.Busy(context.Background())
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error %v, want %q", err, tc.wantError)
				}
				if len(busy) != 0 {
					t.Fatalf("an unknown state must not come with a result: %v", busy)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(busy) != len(tc.wantBusy) {
				t.Fatalf("busy %v, want %v", busy, tc.wantBusy)
			}
			for i, want := range tc.wantBusy {
				if !strings.Contains(busy[i], want) {
					t.Fatalf("busy[%d] = %q, want it to contain %q", i, busy[i], want)
				}
			}
			for _, call := range systemd.calls {
				for _, forbidden := range []string{" stop", " kill", " reset-failed", " start", " restart", " clean"} {
					if strings.Contains(call, forbidden+" ") || strings.HasSuffix(call, forbidden) {
						t.Fatalf("the checker only reads, but ran %q", call)
					}
				}
			}
		})
	}
}

func TestUnitCheckerWithoutSystemdIsUnknown(t *testing.T) {
	for _, checker := range []*UnitChecker{nil, {}} {
		if busy, err := checker.Busy(context.Background()); err == nil || len(busy) != 0 {
			t.Fatalf("got %v, %v; want an error", busy, err)
		}
	}
}

// The executor names its units with the prefix that the checker looks for.
func TestExecutorUnitsCarryThePrefixOfTheChecker(t *testing.T) {
	e := &Executor{}
	for _, what := range []string{"kubeadm", "kubeadm-upgrade", "kubelet"} {
		if unit := e.unit(request(infrav1.InPlaceUpgradeWorker), what); !strings.HasPrefix(unit, UnitPrefix) {
			t.Fatalf("%s does not start with %s", unit, UnitPrefix)
		}
	}
}
