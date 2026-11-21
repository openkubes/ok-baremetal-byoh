#!/usr/bin/env python3
"""Render the pinned upstream CAPI v1.14.4 CAPD smoke template."""

from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "templates" / "upstream-v1.14.4"
VERSIONS = ROOT / "versions.env"
OUTPUT = Path("/tmp/ok-capd-smoke.yaml")


def read_versions():
    values = {}
    for line in VERSIONS.read_text(encoding="utf-8").splitlines():
        match = re.match(r'^([A-Z0-9_]+)="(.*)"$', line.strip())
        if match:
            values[match.group(1)] = match.group(2)
    return values


def main():
    values = read_versions()
    required = (
        "OK_CAPD_WORKLOAD_NAME",
        "OK_CAPD_KUBERNETES_VERSION",
        "OK_CAPD_CONTROL_PLANE_COUNT",
        "OK_CAPD_WORKER_COUNT",
        "OK_CAPD_POD_CIDR",
        "OK_CAPD_SERVICE_CIDR",
    )
    missing = [key for key in required if not values.get(key)]
    if missing:
        raise SystemExit("STOP: missing versions.env values: " + ", ".join(missing))

    parts = [
        (SOURCE / "cluster-with-kcp.yaml").read_text(encoding="utf-8"),
        (SOURCE / "md.yaml").read_text(encoding="utf-8"),
        (SOURCE / "crs.yaml").read_text(encoding="utf-8"),
    ]
    manifest = "\n---\n".join(part.rstrip() for part in parts) + "\n"

    replacements = {
        "${CLUSTER_NAME}": values["OK_CAPD_WORKLOAD_NAME"],
        "${KUBERNETES_VERSION}": values["OK_CAPD_KUBERNETES_VERSION"],
        "${CONTROL_PLANE_MACHINE_COUNT}": values["OK_CAPD_CONTROL_PLANE_COUNT"],
        "${WORKER_MACHINE_COUNT}": values["OK_CAPD_WORKER_COUNT"],
        "${DOCKER_POD_CIDRS}": values["OK_CAPD_POD_CIDR"],
        "${DOCKER_SERVICE_CIDRS}": values["OK_CAPD_SERVICE_CIDR"],
        "${DOCKER_SERVICE_DOMAIN}": "cluster.local",
        "${DOCKER_PRELOAD_IMAGES:-[]}": "[]",
    }
    for old, new in replacements.items():
        manifest = manifest.replace(old, new)

    # The upstream E2E framework injects the CNI file into this ConfigMap.
    # Reproduce that transformation using the pinned kindnet manifest.
    cni = (SOURCE / "kindnet.yaml").read_text(encoding="utf-8").rstrip()
    cni = cni.replace(
        "${DOCKER_POD_CIDRS},${DOCKER_POD_IPV6_CIDRS}",
        values["OK_CAPD_POD_CIDR"],
    )
    config_map_data = "data:\n  kindnet.yaml: |\n" + "".join(
        "    " + line + "\n" for line in cni.splitlines()
    )
    marker = "data: ${CNI_RESOURCES}"
    if marker not in manifest:
        raise SystemExit("STOP: upstream CRS template no longer contains CNI_RESOURCES marker")
    manifest = manifest.replace(marker, config_map_data.rstrip())

    unresolved = re.findall(r"\$\{[^}]+\}", manifest)
    if unresolved:
        raise SystemExit("STOP: unresolved template variables: " + ", ".join(sorted(set(unresolved))))

    OUTPUT.write_text(manifest, encoding="utf-8")
    print(f"Rendered {OUTPUT}")
    print(f"Cluster: {values['OK_CAPD_WORKLOAD_NAME']}")
    print(f"Kubernetes: {values['OK_CAPD_KUBERNETES_VERSION']}")
    print(f"Control plane: {values['OK_CAPD_CONTROL_PLANE_COUNT']}; workers: {values['OK_CAPD_WORKER_COUNT']}")
    print(f"Manifest lines: {len(manifest.splitlines())}")


if __name__ == "__main__":
    try:
        main()
    except OSError as exc:
        print(f"STOP: cannot render CAPD template: {exc}", file=sys.stderr)
        raise SystemExit(1)
