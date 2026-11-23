// Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
// SPDX-License-Identifier: Apache-2.0

package noreplacement

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// BUILD STATUS: PENDING. Imports CAPI/controller-runtime; built & integration-
// tested on the Mac / in CI. This file is the READ-ONLY bridge between the live
// objects (extractors + grant reader) and the tested pure policy. It performs NO
// writes — the guard only verifies.

// GrantReader resolves the single active Upgrade Grant for a target, read-only.
type GrantReader interface {
	// GrantFor returns the matching, parsed, unexpired grant for the given target
	// binding as of now, or (nil, nil) if none exists. It MUST only read.
	GrantFor(ctx context.Context, namespace, clusterUID string, kind TargetKind, targetUID, machineUID string, now time.Time) (*Grant, error)
}

// KubernetesGrantReader lists grant ConfigMaps (by GrantLabel) read-only and
// returns the one bound to the requested target. Production callers pass an
// uncached reader (mgr.GetAPIReader()).
type KubernetesGrantReader struct {
	Reader client.Reader
}

// GrantFor implements GrantReader. It selects grant objects by label OR the
// okx-grant-* name convention (name-based recognition closes the objectSelector
// evasion path), parses each strictly, and returns the one whose immutable
// bindings match. Expired grants are skipped BEFORE the ambiguity check (grants
// are undeletable during the lifecycle, so an expired one must not block a later
// grant forever). Two simultaneously valid matches are ambiguous -> nil (fail-closed).
func (k KubernetesGrantReader) GrantFor(ctx context.Context, namespace, clusterUID string, kind TargetKind, targetUID, machineUID string, now time.Time) (*Grant, error) {
	var list corev1.ConfigMapList
	if err := k.Reader.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	var match *Grant
	for i := range list.Items {
		cm := &list.Items[i]
		if !IsGrantObject(cm.Name, cm.Labels) {
			continue
		}
		g, err := ParseGrant(cm.Data)
		if err != nil {
			continue // a malformed grant is ignored, never widens access
		}
		if g.ClusterUID != clusterUID || g.TargetKind != kind || g.TargetUID != targetUID {
			continue
		}
		if g.MachineUID != "" && machineUID != "" && g.MachineUID != machineUID {
			continue
		}
		if !now.Before(g.NotAfter) {
			continue // expired: can never authorize, must not cause ambiguity
		}
		if match != nil {
			return nil, nil // ambiguous -> fail closed
		}
		match = g
	}
	return match, nil
}

// baseQuery builds the common GrantQuery fields from a resolved grant.
func baseQuery(clusterUID string, g *Grant, dryRun bool, now time.Time) GrantQuery {
	return GrantQuery{
		DryRun:     dryRun,
		ClusterUID: clusterUID,
		TargetKind: g.TargetKind,
		TargetUID:  g.TargetUID,
		MachineUID: g.MachineUID,
		Now:        now,
	}
}

// GuardMachineUpdate authorizes a protected Machine UPDATE: strict deny-by-default
// (EvaluateMachineUpdate) unless a valid grant authorizes the classified in-place
// transition.
func GuardMachineUpdate(ctx context.Context, reader GrantReader, resolver TargetResolver, scope Scope, clusterUID string, oldM, newM *clusterv1.Machine, dryRun bool, now time.Time) Decision {
	if !scope.Protected {
		return EvaluateMachineUpdate(scope, oldM, newM)
	}
	change := ExtractMachineChange(oldM, newM)
	// Try the grant exception only for a recognized in-place shape. Every miss is
	// logged and falls through to the strict deny below; grantReason keeps WHY the
	// grant path failed so the denial is attributable (missing / unclaimed / ...).
	grantReason := ""
	if shape := ClassifyMachineTransition(change); shape != ShapeNone {
		log := logf.FromContext(ctx).WithValues("shape", shape)
		// Resolve the grant target by climbing the owner chain (KCP, or MD via MS).
		kind, targetUID, err := resolver.ForMachine(ctx, newM)
		switch {
		case err != nil:
			grantReason = "could not resolve upgrade target for Machine"
			log.Info("grant path: target resolution failed", "err", err.Error())
		default:
			g, err := reader.GrantFor(ctx, newM.Namespace, clusterUID, kind, targetUID, string(newM.UID), now)
			switch {
			case err != nil:
				grantReason = "could not resolve upgrade grant for Machine"
				log.Info("grant path: grant lookup failed", "err", err.Error())
			case g == nil:
				grantReason = "in-place update requires an authorized upgrade grant (no unexpired, unambiguous grant matches)"
				log.Info("grant path: no matching grant", "targetKind", kind, "targetUID", targetUID)
			default:
				d := EvaluateMachineUpdateInPlace(scope, change, g, clusterUID, dryRun, now)
				if d.Allowed {
					log.Info("ALLOW in-place transition", "grantID", g.GrantID, "dryRun", dryRun)
					return d
				}
				grantReason = d.Reason
				log.Info("grant path: grant rejected", "grantID", g.GrantID, "reason", d.Reason)
			}
		}
	}
	// Fall back to the existing strict policy (deny-by-default). CREATE/DELETE are
	// handled by EvaluateMachineLifecycle and are NEVER granted here. The strict
	// decision stays authoritative; only the text of a denial is made specific.
	d := EvaluateMachineUpdate(scope, oldM, newM)
	if !d.Allowed && grantReason != "" {
		return deny(grantReason)
	}
	return d
}

// GuardByoMachineUpdate authorizes a protected ByoMachine (InfraMachine) UPDATE.
func GuardByoMachineUpdate(ctx context.Context, reader GrantReader, resolver TargetResolver, scope Scope, clusterUID string, oldU, newU *unstructured.Unstructured, dryRun bool, now time.Time) Decision {
	if !scope.Protected {
		return allow()
	}
	change := ExtractByoMachineChange(oldU, newU)
	kind, targetUID, err := resolver.ForByoMachine(ctx, newU)
	if err != nil {
		return deny("could not resolve upgrade target for ByoMachine")
	}
	g, err := reader.GrantFor(ctx, newU.GetNamespace(), clusterUID, kind, targetUID, "", now)
	if err != nil {
		return deny("could not resolve upgrade grant for ByoMachine")
	}
	return EvaluateByoMachineInPlace(scope, change, g, baseQuery(clusterUID, orEmptyGrant(g), dryRun, now))
}

// GuardMachineSetUpdate authorizes a protected MachineSet UPDATE (allow-by-default,
// move-annotation gated).
func GuardMachineSetUpdate(ctx context.Context, reader GrantReader, scope Scope, clusterUID string, oldMS, newMS *clusterv1.MachineSet, dryRun bool, now time.Time, mdUID string) Decision {
	if !scope.Protected {
		return allow()
	}
	touched := MachineSetMoveTouched(oldMS, newMS)
	if !touched {
		return allow()
	}
	g, err := reader.GrantFor(ctx, newMS.Namespace, clusterUID, TargetMachineDeployment, mdUID, "", now)
	if err != nil {
		return deny("could not resolve upgrade grant for MachineSet move")
	}
	q := baseQuery(clusterUID, orEmptyGrant(g), dryRun, now)
	q.Transition = TransitionMDAcknowledge
	return EvaluateMachineSetInPlace(scope, touched, g, q)
}

// orEmptyGrant avoids a nil-deref in baseQuery when no grant was found; the
// subsequent VerifyGrant(nil) still denies.
func orEmptyGrant(g *Grant) *Grant {
	if g == nil {
		return &Grant{}
	}
	return g
}

// GuardGrantConfigMap enforces the guarded-ConfigMap role/field separation plus
// the grant-label-removal protection, for objects recognized as grants.
func GuardGrantConfigMap(op Operation, actor GrantActor, oldCM, newCM *corev1.ConfigMap) Decision {
	var oldLabels, newLabels, oldData, newData map[string]string
	if oldCM != nil {
		oldLabels, oldData = oldCM.Labels, oldCM.Data
	}
	if newCM != nil {
		newLabels, newData = newCM.Labels, newCM.Data
	}
	return EvaluateGrantConfigMapGuarded(op, actor, oldLabels, newLabels, oldData, newData)
}
