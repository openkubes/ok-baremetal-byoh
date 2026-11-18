#!/usr/bin/env python3
"""Fail-closed renderer for an isolated, locally loaded kind controller image."""
import pathlib
import re
import sys

def render(text: str, image: str) -> str:
    local = re.fullmatch(
        r"localhost/openkubes/byoh-controller:git-[0-9a-f]{12}", image
    )
    ghcr = re.fullmatch(
        r"ghcr[.]io/openkubes/byoh-controller@sha256:[0-9a-f]{64}", image
    )
    if not (local or ghcr):
        raise ValueError("controller image must be a local Git revision or pinned OpenKubes GHCR digest")
    changes = {
        "ghcr.io/superbrothers/cluster-api-byoh-controller:dev": image,
        "${MANUAL_CSR_APPROVAL:=disable}": "enable",
        "${CAPI_DIAGNOSTICS_ADDRESS:=:8443}": ":8443",
        "${CAPI_INSECURE_DIAGNOSTICS:=false}": "false",
        "${BYOH_REQUEUE_PERIOD:=10s}": "10s",
        "${BYOH_BYOMACHINE_CONCURRENCY:=1}": "1",
    }
    for old, new in changes.items():
        if text.count(old) != 1:
            raise ValueError(f"unexpected occurrence count for {old!r}: {text.count(old)}")
        text = text.replace(old, new)
    if "${" in text or "ghcr.io/superbrothers/cluster-api-byoh-controller" in text:
        raise ValueError("unresolved substitution or upstream image")
    approval = re.findall(
        r'(?m)^\s*- name: MANUAL_CSR_APPROVAL\s*\n\s*value: ["\']?enable["\']?\s*$',
        text,
    )
    if text.count("image: " + image) != 1 or len(approval) != 1:
        raise ValueError("controller image or manual-CSR setting mismatch")
    return text

if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit("usage: byoh-controller-manifest.py INPUT OUTPUT IMAGE")
    src, dst, image = sys.argv[1:]
    try:
        value = render(pathlib.Path(src).read_text(), image)
        pathlib.Path(dst).write_text(value)
    except (OSError, ValueError) as exc:
        sys.exit(f"STOP: {exc}")
