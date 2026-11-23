// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package inplace

import (
	"testing"
	"time"
)

func TestAcceptInPlace_VersionOnly(t *testing.T) {
	cur := SpecView{Version: "v1.33.4"}
	des := SpecView{Version: "v1.34.1"}
	got, fields := AcceptInPlace(cur, des)
	if got.Version != "v1.34.1" {
		t.Fatalf("version not accepted: %+v", got)
	}
	if len(fields) != 1 || fields[0] != "spec.version" {
		t.Fatalf("expected only spec.version accepted, got %v", fields)
	}
}

func TestAcceptInPlace_NoChange(t *testing.T) {
	cur := SpecView{Version: "v1.34.1"}
	got, fields := AcceptInPlace(cur, cur)
	if got != cur || len(fields) != 0 {
		t.Fatalf("expected no-op, got %+v fields=%v", got, fields)
	}
	if VersionChanged(cur, cur) {
		t.Fatalf("VersionChanged must be false when equal")
	}
}

func TestVersionChanged(t *testing.T) {
	if !VersionChanged(SpecView{Version: "v1.33.4"}, SpecView{Version: "v1.34.1"}) {
		t.Fatalf("expected version change to be detected")
	}
}

func TestUpdateDone(t *testing.T) {
	start := time.Unix(1000, 0)
	if UpdateDone(start, start.Add(5*time.Second), 10*time.Second) {
		t.Fatalf("must not be done before completeAfter")
	}
	if !UpdateDone(start, start.Add(10*time.Second), 10*time.Second) {
		t.Fatalf("must be done at completeAfter")
	}
	if !UpdateDone(start, start.Add(30*time.Second), 10*time.Second) {
		t.Fatalf("must be done after completeAfter")
	}
}
