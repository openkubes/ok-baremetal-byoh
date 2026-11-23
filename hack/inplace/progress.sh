#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# Read-only per-node progress of an in-place upgrade: Machine version and Node
# kubelet, the in-place annotations of CAPI, and the request and status of the
# ByoHost. Usage: progress.sh
. "$(dirname "$0")/lib.sh"
echo "== $(ts) $NS/$CLUSTER"
k -n "$NS" get machines -l "cluster.x-k8s.io/cluster-name=$CLUSTER" -o \
	'custom-columns=MACHINE:.metadata.name,UID:.metadata.uid,VERSION:.spec.version,KUBELET:.status.nodeInfo.kubeletVersion,IN-PROGRESS:.metadata.annotations.in-place-updates\.internal\.cluster\.x-k8s\.io/update-in-progress,HOOKS:.metadata.annotations.runtime\.cluster\.x-k8s\.io/pending-hooks'
k -n "$NS" get byohosts -o \
	'custom-columns=BYOHOST:.metadata.name,MACHINE:.spec.inPlaceUpgrade.machineName,ROLE:.spec.inPlaceUpgrade.role,TO:.spec.inPlaceUpgrade.toVersion,ATTEMPT:.spec.inPlaceUpgrade.attempt,PHASE:.status.inPlaceUpgrade.phase,STEP:.status.inPlaceUpgrade.step,KUBELET:.status.inPlaceUpgrade.observedKubeletVersion,NODE:.status.inPlaceUpgrade.observedNodeVersion'
