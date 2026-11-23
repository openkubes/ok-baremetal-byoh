#!/usr/bin/env python3
# Copyright 2026 The Cluster API Provider BringYourOwnHost Authors.
# SPDX-License-Identifier: Apache-2.0
"""Identity checks of the kubeconfigs used by the in-place tooling (read-only).
The kubeconfig is read as JSON from stdin (`kubectl config view --raw -o json`).

  kubeconfig_identity.py management CONTEXT SERVER CA_SHA256
      The named context exists (the current-context is never used), its cluster has
      exactly a server and an embedded CA, the server is SERVER and the sha256 of the
      certificate-authority-data string is CA_SHA256 (the definition of remote.sh).
  kubeconfig_identity.py workload SERVER CA_B64
      The kubeconfig has exactly one context, its cluster has exactly a server and an
      embedded CA, the server is SERVER (the Cluster's controlPlaneEndpoint) and the CA
      is CA_B64 (tls.crt of the Secret <cluster>-ca). Prints the context name.
Exit 0 PASS, 1 FAIL (the reason on stderr), 2 usage.
"""
import base64
import binascii
import hashlib
import json
import sys


class Mismatch(Exception):
    pass


def cluster_of(cfg, context):
    ctx = [c.get("context") or {} for c in cfg.get("contexts") or [] if c.get("name") == context]
    if len(ctx) != 1:
        raise Mismatch(f"no single context {context!r}")
    cl = [c.get("cluster") or {} for c in cfg.get("clusters") or [] if c.get("name") == ctx[0].get("cluster")]
    if len(cl) != 1:
        raise Mismatch(f"context {context!r} has no single cluster")
    extra = sorted(set(cl[0]) - {"server", "certificate-authority-data"})
    if extra:
        # no TLS opt-out, CA file, proxy or other override
        raise Mismatch(f"context {context!r} has settings beyond server and embedded CA: {', '.join(extra)}")
    if not cl[0].get("certificate-authority-data"):
        raise Mismatch(f"context {context!r} has no embedded CA")
    return cl[0]


def management(cfg, context, server, ca_sha256):
    c = cluster_of(cfg, context)
    if c.get("server") != server:
        raise Mismatch(f"context {context!r} points at {c.get('server')}, not {server}")
    got = hashlib.sha256(c["certificate-authority-data"].encode()).hexdigest()
    if got != ca_sha256:
        raise Mismatch(f"context {context!r} trusts CA sha256 {got[:16]}, not {ca_sha256[:16]}")


def decoded(b64):
    try:
        return base64.b64decode(b64, validate=True)
    except (binascii.Error, ValueError):
        raise Mismatch("CA data is not base64") from None


def workload(cfg, server, ca_b64):
    contexts = [c.get("name") for c in cfg.get("contexts") or []]
    if len(contexts) != 1:
        raise Mismatch(f"workload kubeconfig has {len(contexts)} contexts, expected 1")
    c = cluster_of(cfg, contexts[0])
    if c.get("server") != server:
        raise Mismatch(f"workload kubeconfig points at {c.get('server')}, not the Cluster endpoint {server}")
    if decoded(c["certificate-authority-data"]) != decoded(ca_b64):
        raise Mismatch("workload kubeconfig trusts another CA than the Cluster's <cluster>-ca Secret")
    return contexts[0]


def main(argv):
    try:
        cfg = json.load(sys.stdin)
        if argv[:1] == ["management"] and len(argv) == 4:
            management(cfg, *argv[1:])
        elif argv[:1] == ["workload"] and len(argv) == 3:
            print(workload(cfg, *argv[1:]))
        else:
            print(__doc__.strip(), file=sys.stderr)
            return 2
    except (Mismatch, json.JSONDecodeError) as e:
        print(f"IDENTITY FAIL: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
