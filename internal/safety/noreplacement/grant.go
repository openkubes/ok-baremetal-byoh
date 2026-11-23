// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"crypto/subtle"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TargetKind is the kind of CAPI owner object an Upgrade Grant is bound to.
type TargetKind string

const (
	// TargetKubeadmControlPlane binds a grant to a KCP (control-plane in-place).
	TargetKubeadmControlPlane TargetKind = "KubeadmControlPlane"
	// TargetMachineDeployment binds a grant to a MachineDeployment (worker in-place).
	TargetMachineDeployment TargetKind = "MachineDeployment"
	// TargetMachineSet is used for the MD-ACK transition, where the admitted
	// object is the approved target MachineSet of the grant's MachineDeployment.
	TargetMachineSet TargetKind = "MachineSet"
)

// Enumerated in-place transition IDs. These are the ONLY shapes that may ever be
// allowed under a grant; every other object change remains denied (ADR-047).
// The exact per-object diff for each ID is specified and source-verified in
// labs/byoh-inplace-spike/design/M1-grant-contract-and-transition-matrix.md.
const (
	TransitionKCPSetInProgress   = "KCP-1"     // Machine: add update-in-progress
	TransitionKCPVersionBump     = "KCP-2"     // Machine: spec.version from->to
	TransitionKCPPendingHook     = "KCP-3"     // Machine: add pending-hooks(UpdateMachine)
	TransitionKCPCleanupHook     = "KCP-C1"    // Machine: remove pending-hooks(UpdateMachine)
	TransitionKCPCleanupFlag     = "KCP-C2"    // Machine: remove update-in-progress
	TransitionKCPInfraInProgress = "KCP-IM"    // InfraMachine: set clonedFrom+in-progress
	TransitionKCPInfraCleanup    = "KCP-IM-C"  // InfraMachine: remove in-progress
	TransitionMDMove             = "MD-MOVE"   // Machine: ownerRef+label+2 annos (single patch)
	TransitionMDFinalizeMachine  = "MD-FIN-M"  // Machine: spec.version+failureDomain
	TransitionMDTrigger          = "MD-TRIG"   // Machine: add pending-hooks; drop pending-ack
	TransitionMDCleanupHook      = "MD-C1"     // Machine: remove pending-hooks
	TransitionMDCleanupFlag      = "MD-C2"     // Machine: remove update-in-progress
	TransitionMDFinalizeInfra    = "MD-FIN-IM" // InfraMachine: set clonedFrom+in-progress
	TransitionMDAcknowledge      = "MD-ACK"    // MachineSet: add acknowledged-move+receive-from
)

// knownTransitions is the closed set of recognized transition IDs.
var knownTransitions = map[string]struct{}{
	TransitionKCPSetInProgress:   {},
	TransitionKCPVersionBump:     {},
	TransitionKCPPendingHook:     {},
	TransitionKCPCleanupHook:     {},
	TransitionKCPCleanupFlag:     {},
	TransitionKCPInfraInProgress: {},
	TransitionKCPInfraCleanup:    {},
	TransitionMDMove:             {},
	TransitionMDFinalizeMachine:  {},
	TransitionMDTrigger:          {},
	TransitionMDCleanupHook:      {},
	TransitionMDCleanupFlag:      {},
	TransitionMDFinalizeInfra:    {},
	TransitionMDAcknowledge:      {},
}

// validTargetKind reports whether k is a recognized target kind.
func validTargetKind(k TargetKind) bool {
	switch k {
	case TargetKubeadmControlPlane, TargetMachineDeployment, TargetMachineSet:
		return true
	default:
		return false
	}
}

// Grant is the immutable ADR-047 Upgrade Grant together with its single-use
// claim slot, as read READ-ONLY by the guard from the shared CAS object.
//
// The guard never mints, writes or consumes a grant; it only verifies it.
type Grant struct {
	GrantID             string
	ClusterUID          string
	TargetKind          TargetKind
	TargetName          string
	TargetUID           string
	MachineUID          string // REQUIRED for KCP singleton; empty for MD set-move
	FromVersion         string
	ToVersion           string
	Transitions         map[string]struct{}
	PayloadDigest       string
	NotAfter            time.Time
	PermittedUseCount   int
	RequestDigest       string
	AuthorizationDigest string

	// Claim slot, set out-of-band exactly once by the Upgrade-Initiator.
	Claimed    bool
	ClaimToken string
}

// AllowsTransition reports whether this grant enumerates the given transition ID.
func (g *Grant) AllowsTransition(id string) bool {
	if g == nil || id == "" {
		return false
	}
	_, ok := g.Transitions[id]
	return ok
}

// Grant ConfigMap keys (the shared CAS object; see M1 §1.1).
const (
	grantKeyGrantID             = "grantID"
	grantKeyClusterUID          = "clusterUID"
	grantKeyTargetKind          = "targetKind"
	grantKeyTargetName          = "targetName"
	grantKeyTargetUID           = "targetUID"
	grantKeyMachineUID          = "machineUID"
	grantKeyFromVersion         = "fromVersion"
	grantKeyToVersion           = "toVersion"
	grantKeyTransitions         = "transitions"
	grantKeyPayloadDigest       = "payloadDigest"
	grantKeyNotAfter            = "notAfter"
	grantKeyPermittedUseCount   = "permittedUseCount"
	grantKeyRequestDigest       = "requestDigest"
	grantKeyAuthorizationDigest = "authorizationDigest"
	// GrantClaimKey is the single claim slot key. It is the ONLY key the
	// Upgrade-Initiator may add, and it may be added at most once.
	GrantClaimKey = "claim"
	// GrantLabel marks a ConfigMap as a protected Upgrade Grant CAS object.
	GrantLabel = "safety.openkubes.org/grant"
)

// grantDataKeys is the closed set of immutable grant-data keys (the Approver's
// write surface). The claim key is deliberately excluded.
var grantDataKeys = map[string]struct{}{
	grantKeyGrantID: {}, grantKeyClusterUID: {}, grantKeyTargetKind: {},
	grantKeyTargetName: {}, grantKeyTargetUID: {}, grantKeyMachineUID: {},
	grantKeyFromVersion: {}, grantKeyToVersion: {}, grantKeyTransitions: {},
	grantKeyPayloadDigest: {}, grantKeyNotAfter: {}, grantKeyPermittedUseCount: {},
	grantKeyRequestDigest: {}, grantKeyAuthorizationDigest: {},
}

// requiredGrantDataKeys must all be present on a well-formed grant.
// machineUID is optional (KCP-only); every other data key is required.
var requiredGrantDataKeys = []string{
	grantKeyGrantID, grantKeyClusterUID, grantKeyTargetKind, grantKeyTargetName,
	grantKeyTargetUID, grantKeyFromVersion, grantKeyToVersion, grantKeyTransitions,
	grantKeyPayloadDigest, grantKeyNotAfter, grantKeyPermittedUseCount,
	grantKeyRequestDigest, grantKeyAuthorizationDigest,
}

// ParseGrant strictly parses a grant from the shared CAS object's string map.
//
// It fails closed on any unknown key, missing required key, malformed value, or
// an empty immutable field. The claim slot (GrantClaimKey) is optional: when
// present and non-empty the grant is Claimed.
//
//nolint:gocyclo // a strict field-by-field parser that fails closed on each malformed key.
func ParseGrant(data map[string]string) (*Grant, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("grant is empty")
	}

	// Reject any key that is neither a known data key nor the claim key.
	for key := range data {
		if key == GrantClaimKey {
			continue
		}
		if _, ok := grantDataKeys[key]; !ok {
			return nil, fmt.Errorf("grant contains unknown key %q", key)
		}
	}

	for _, key := range requiredGrantDataKeys {
		v, ok := data[key]
		if !ok || strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("grant is missing required key %q", key)
		}
	}

	g := &Grant{
		GrantID:             data[grantKeyGrantID],
		ClusterUID:          data[grantKeyClusterUID],
		TargetKind:          TargetKind(data[grantKeyTargetKind]),
		TargetName:          data[grantKeyTargetName],
		TargetUID:           data[grantKeyTargetUID],
		MachineUID:          strings.TrimSpace(data[grantKeyMachineUID]),
		FromVersion:         data[grantKeyFromVersion],
		ToVersion:           data[grantKeyToVersion],
		PayloadDigest:       data[grantKeyPayloadDigest],
		RequestDigest:       data[grantKeyRequestDigest],
		AuthorizationDigest: data[grantKeyAuthorizationDigest],
	}

	if !validTargetKind(g.TargetKind) {
		return nil, fmt.Errorf("grant has invalid targetKind %q", g.TargetKind)
	}
	if g.FromVersion == g.ToVersion {
		return nil, fmt.Errorf("grant fromVersion and toVersion must differ")
	}

	transitions, err := parseTransitions(data[grantKeyTransitions])
	if err != nil {
		return nil, err
	}
	g.Transitions = transitions

	notAfter, err := time.Parse(time.RFC3339, strings.TrimSpace(data[grantKeyNotAfter]))
	if err != nil {
		return nil, fmt.Errorf("grant notAfter is not RFC3339: %w", err)
	}
	g.NotAfter = notAfter

	count, err := strconv.Atoi(strings.TrimSpace(data[grantKeyPermittedUseCount]))
	if err != nil || count != 1 {
		return nil, fmt.Errorf("grant permittedUseCount must be exactly 1")
	}
	g.PermittedUseCount = count

	// KCP singleton grants MUST bind a Machine UID.
	if g.TargetKind == TargetKubeadmControlPlane && g.MachineUID == "" {
		return nil, fmt.Errorf("KubeadmControlPlane grant must bind machineUID")
	}

	if claim, ok := data[GrantClaimKey]; ok && strings.TrimSpace(claim) != "" {
		g.Claimed = true
		g.ClaimToken = claim
	}

	return g, nil
}

// parseTransitions parses a comma-separated, non-empty list of known transition
// IDs into a set, rejecting unknown or duplicate IDs.
func parseTransitions(raw string) (map[string]struct{}, error) {
	parts := strings.Split(raw, ",")
	out := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		id := strings.TrimSpace(p)
		if id == "" {
			continue
		}
		if _, ok := knownTransitions[id]; !ok {
			return nil, fmt.Errorf("grant lists unknown transition %q", id)
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("grant lists duplicate transition %q", id)
		}
		out[id] = struct{}{}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("grant lists no transitions")
	}
	return out, nil
}

// GrantQuery is the admission context handed to VerifyGrant by the per-object
// handlers. It carries ONLY facts the handler has independently established:
// the live cluster UID, the owner object the admitted change binds to, the
// transition the diff was classified as, and (when relevant) the observed
// version delta and the digest of the ACTUAL InstallationSecret payload bytes.
type GrantQuery struct {
	DryRun          bool
	ClusterUID      string
	TargetKind      TargetKind
	TargetUID       string
	MachineUID      string
	Transition      string
	VersionRelevant bool
	FromVersion     string
	ToVersion       string
	PayloadRelevant bool
	PayloadDigest   string
	Now             time.Time
}

// VerifyGrant is the read-only authorization predicate (M1 §1.5 / §1.6).
//
// It returns allow() only when every binding holds; otherwise it fails closed.
// For dry-run requests the single-use claim is NOT required and is never treated
// as consumed (M1 §1.6), so that CAPI's CanUpdateMachine diff probe can proceed
// without falling back to a dangerous replacement on a singleton control plane.
//
//nolint:gocyclo,gocritic // a flat fail-closed checklist of the grant bindings (M1 §1.5); the query is taken by value so that the predicate cannot change it.
func VerifyGrant(scope Scope, grant *Grant, q GrantQuery) Decision {
	if !scope.Verified {
		return deny(reasonScopeUnverified)
	}
	if !scope.Protected {
		// Unprotected scope is outside the in-place-only policy entirely.
		return allow()
	}

	if grant == nil {
		return deny("in-place update requires an authorized upgrade grant")
	}
	if q.Now.IsZero() {
		return deny("grant verification requires a trusted current time")
	}
	if !grant.NotAfter.IsZero() && !q.Now.Before(grant.NotAfter) {
		return deny("upgrade grant has expired")
	}
	if q.ClusterUID == "" || grant.ClusterUID != q.ClusterUID {
		return deny("upgrade grant is not bound to this cluster")
	}
	if !validTargetKind(q.TargetKind) ||
		grant.TargetKind != q.TargetKind ||
		grant.TargetUID == "" ||
		grant.TargetUID != q.TargetUID {
		return deny("upgrade grant is not bound to this target object")
	}
	// KCP singleton: the grant binds an exact Machine UID.
	if grant.MachineUID != "" && q.MachineUID != "" && grant.MachineUID != q.MachineUID {
		return deny("upgrade grant is not bound to this Machine")
	}
	if q.Transition == "" {
		return deny("change is not a recognized in-place transition")
	}
	if !grant.AllowsTransition(q.Transition) {
		return deny(fmt.Sprintf("transition %q is not authorized by the upgrade grant", q.Transition))
	}
	if q.VersionRelevant {
		if grant.FromVersion != q.FromVersion || grant.ToVersion != q.ToVersion {
			return deny("version transition does not match the upgrade grant")
		}
	}
	if q.PayloadRelevant {
		if grant.PayloadDigest == "" || q.PayloadDigest == "" ||
			subtle.ConstantTimeCompare([]byte(grant.PayloadDigest), []byte(q.PayloadDigest)) != 1 {
			return deny("installation payload digest does not match the upgrade grant")
		}
	}
	if !q.DryRun {
		if !grant.Claimed {
			return deny("upgrade grant has not been claimed by the upgrade initiator")
		}
		if grant.PermittedUseCount < 1 {
			return deny("upgrade grant single-use has been exhausted")
		}
	}
	return allow()
}

// TransitionList returns a grant's authorized transition IDs, sorted (for logs/tests).
func (g *Grant) TransitionList() []string {
	if g == nil {
		return nil
	}
	out := make([]string, 0, len(g.Transitions))
	for id := range g.Transitions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
