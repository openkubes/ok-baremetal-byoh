// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"testing"
	"time"
)

func validKCPGrantData() map[string]string {
	return map[string]string{
		grantKeyGrantID:             "g-1",
		grantKeyClusterUID:          "cluster-uid-1",
		grantKeyTargetKind:          string(TargetKubeadmControlPlane),
		grantKeyTargetName:          "kcp-1",
		grantKeyTargetUID:           "kcp-uid-1",
		grantKeyMachineUID:          "machine-uid-1",
		grantKeyFromVersion:         "v1.33.4",
		grantKeyToVersion:           "v1.34.1",
		grantKeyTransitions:         "KCP-1,KCP-2,KCP-3,KCP-C1,KCP-C2,KCP-IM,KCP-IM-C",
		grantKeyPayloadDigest:       "sha256:abc",
		grantKeyNotAfter:            time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		grantKeyPermittedUseCount:   "1",
		grantKeyRequestDigest:       "sha256:req",
		grantKeyAuthorizationDigest: "sha256:auth",
	}
}

func validMDGrantData() map[string]string {
	d := validKCPGrantData()
	d[grantKeyTargetKind] = string(TargetMachineDeployment)
	d[grantKeyTargetName] = "md-1"
	d[grantKeyTargetUID] = "md-uid-1"
	delete(d, grantKeyMachineUID) // MD set-move binds no single Machine UID
	d[grantKeyTransitions] = "MD-MOVE,MD-FIN-M,MD-TRIG,MD-C1,MD-C2,MD-FIN-IM"
	return d
}

func TestParseGrant_Valid(t *testing.T) {
	g, err := ParseGrant(validKCPGrantData())
	if err != nil {
		t.Fatalf("expected valid grant, got error: %v", err)
	}
	if g.Claimed {
		t.Fatalf("grant without claim key must not be Claimed")
	}
	if !g.AllowsTransition(TransitionKCPVersionBump) {
		t.Fatalf("expected KCP-2 to be allowed")
	}
	if g.AllowsTransition(TransitionMDMove) {
		t.Fatalf("MD-MOVE must not be allowed by a KCP grant")
	}
}

func TestParseGrant_ClaimPresent(t *testing.T) {
	d := validKCPGrantData()
	d[GrantClaimKey] = "claim-token-xyz"
	g, err := ParseGrant(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !g.Claimed || g.ClaimToken != "claim-token-xyz" {
		t.Fatalf("expected grant to be claimed with token")
	}
}

func TestParseGrant_Rejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{"unknown key", func(d map[string]string) { d["bogus"] = "x" }},
		{"missing required", func(d map[string]string) { delete(d, grantKeyClusterUID) }},
		{"empty required", func(d map[string]string) { d[grantKeyToVersion] = "  " }},
		{"useCount not 1", func(d map[string]string) { d[grantKeyPermittedUseCount] = "2" }},
		{"unknown transition", func(d map[string]string) { d[grantKeyTransitions] = "KCP-2,BOGUS" }},
		{"duplicate transition", func(d map[string]string) { d[grantKeyTransitions] = "KCP-2,KCP-2" }},
		{"no transitions", func(d map[string]string) { d[grantKeyTransitions] = " , " }},
		{"from==to", func(d map[string]string) { d[grantKeyToVersion] = d[grantKeyFromVersion] }},
		{"bad notAfter", func(d map[string]string) { d[grantKeyNotAfter] = "not-a-time" }},
		{"invalid targetKind", func(d map[string]string) { d[grantKeyTargetKind] = "Pod" }},
		{"KCP without machineUID", func(d map[string]string) { delete(d, grantKeyMachineUID) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := validKCPGrantData()
			tc.mutate(d)
			if _, err := ParseGrant(d); err == nil {
				t.Fatalf("expected rejection for %q, got none", tc.name)
			}
		})
	}
}

func protectedScope() Scope { return Scope{Verified: true, Protected: true} }

func baseKCPQuery() GrantQuery {
	return GrantQuery{
		DryRun:          false,
		ClusterUID:      "cluster-uid-1",
		TargetKind:      TargetKubeadmControlPlane,
		TargetUID:       "kcp-uid-1",
		MachineUID:      "machine-uid-1",
		Transition:      TransitionKCPVersionBump,
		VersionRelevant: true,
		FromVersion:     "v1.33.4",
		ToVersion:       "v1.34.1",
		PayloadRelevant: false,
		Now:             time.Now(),
	}
}

func claimedKCPGrant(t *testing.T) *Grant {
	t.Helper()
	d := validKCPGrantData()
	d[GrantClaimKey] = "claim-1"
	g, err := ParseGrant(d)
	if err != nil {
		t.Fatalf("setup grant: %v", err)
	}
	return g
}

func TestVerifyGrant_AllowClaimed(t *testing.T) {
	d := VerifyGrant(protectedScope(), claimedKCPGrant(t), baseKCPQuery())
	if !d.Allowed {
		t.Fatalf("expected allow, got deny: %s", d.Reason)
	}
}

func TestVerifyGrant_Denials(t *testing.T) {
	tests := []struct {
		name   string
		grant  func(*testing.T) *Grant
		mutate func(*GrantQuery)
	}{
		{"nil grant", func(*testing.T) *Grant { return nil }, func(*GrantQuery) {}},
		{"wrong cluster", claimedKCPGrant, func(q *GrantQuery) { q.ClusterUID = "other" }},
		{"wrong target uid", claimedKCPGrant, func(q *GrantQuery) { q.TargetUID = "other" }},
		{"wrong target kind", claimedKCPGrant, func(q *GrantQuery) { q.TargetKind = TargetMachineDeployment }},
		{"wrong machine uid", claimedKCPGrant, func(q *GrantQuery) { q.MachineUID = "other" }},
		{"transition empty", claimedKCPGrant, func(q *GrantQuery) { q.Transition = "" }},
		{"transition not granted", claimedKCPGrant, func(q *GrantQuery) { q.Transition = TransitionMDMove }},
		{"version mismatch", claimedKCPGrant, func(q *GrantQuery) { q.ToVersion = "v1.99.9" }},
		{"zero now", claimedKCPGrant, func(q *GrantQuery) { q.Now = time.Time{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := baseKCPQuery()
			tc.mutate(&q)
			d := VerifyGrant(protectedScope(), tc.grant(t), q)
			if d.Allowed {
				t.Fatalf("expected deny for %q, got allow", tc.name)
			}
		})
	}
}

func TestVerifyGrant_Expired(t *testing.T) {
	d := validKCPGrantData()
	d[GrantClaimKey] = "claim-1"
	d[grantKeyNotAfter] = time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	g, err := ParseGrant(d)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if VerifyGrant(protectedScope(), g, baseKCPQuery()).Allowed {
		t.Fatalf("expired grant must be denied")
	}
}

// Two competing claims: the loser never acquires the claim, so its real
// (non-dry-run) in-place write must be denied for lack of a claim.
func TestVerifyGrant_UnclaimedRealWriteDenied(t *testing.T) {
	g, err := ParseGrant(validKCPGrantData()) // no claim
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if VerifyGrant(protectedScope(), g, baseKCPQuery()).Allowed {
		t.Fatalf("unclaimed grant must deny a real in-place write")
	}
}

// Dry-run must be allowed WITHOUT a claim (M1 §1.6) so CanUpdateMachine's diff
// probe does not force a replacement on a singleton control plane.
func TestVerifyGrant_DryRunAllowedWithoutClaim(t *testing.T) {
	g, err := ParseGrant(validKCPGrantData()) // no claim
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	q := baseKCPQuery()
	q.DryRun = true
	if !VerifyGrant(protectedScope(), g, q).Allowed {
		t.Fatalf("dry-run probe must be allowed without a claim")
	}
}

func TestVerifyGrant_PayloadDigest(t *testing.T) {
	g := claimedKCPGrant(t) // PayloadDigest sha256:abc
	q := baseKCPQuery()
	q.PayloadRelevant = true
	q.PayloadDigest = "sha256:abc"
	if !VerifyGrant(protectedScope(), g, q).Allowed {
		t.Fatalf("matching payload digest must allow")
	}
	q.PayloadDigest = "sha256:WRONG"
	if VerifyGrant(protectedScope(), g, q).Allowed {
		t.Fatalf("mismatched payload digest must deny")
	}
}

func TestVerifyGrant_Scope(t *testing.T) {
	if VerifyGrant(Scope{}, claimedKCPGrant(t), baseKCPQuery()).Allowed {
		t.Fatalf("unverified scope must deny")
	}
	// Unprotected scope is outside the policy; allow regardless of grant.
	if !VerifyGrant(Scope{Verified: true, Protected: false}, nil, baseKCPQuery()).Allowed {
		t.Fatalf("unprotected scope must allow")
	}
}
