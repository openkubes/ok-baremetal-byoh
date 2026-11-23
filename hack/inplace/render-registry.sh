#!/usr/bin/env bash
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
#
# Renders the trusted No-Replacement registry of the protected Cluster from its
# live UID (read-only), for review before the deployment. Usage: render-registry.sh > registry.json
. "$(dirname "$0")/lib.sh"
uid=$(k -n "$NS" get cluster "$CLUSTER" -o jsonpath='{.metadata.uid}')
[ -n "$uid" ] || { echo "no Cluster $NS/$CLUSTER" >&2; exit 1; }
printf '{"registrations": [{"namespace": "%s", "clusterName": "%s", "clusterUID": "%s"}]}\n' "$NS" "$CLUSTER" "$uid"
