#!/usr/bin/env python3
"""Record the digests returned by Buildx and verify them from GHCR by digest."""
import json
import pathlib
import re
import subprocess
import sys


IMAGES = ("byoh-controller", "no-replacement-guard", "inplace-extension")
REGISTRY = "ghcr.io/openkubes/"
ARCHES = ("amd64", "arm64")
REPOSITORY = "openkubes/ok-baremetal-byoh"
INDEX_TYPES = {"application/vnd.oci.image.index.v1+json",
               "application/vnd.docker.distribution.manifest.list.v2+json"}
IMAGE_TYPES = {"application/vnd.oci.image.manifest.v1+json",
               "application/vnd.docker.distribution.manifest.v2+json"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def approved_inputs(commit, run, attempt):
    require(re.fullmatch(r"[a-f0-9]{40}", commit), "expected full source commit SHA")
    for value in (run, attempt):
        require(re.fullmatch(r"[1-9][0-9]*", value), "expected positive run ID and attempt")
    return {"repository": REPOSITORY, "source_sha": commit,
            "run_id": run, "run_attempt": attempt}


def reference(name, digest):
    require(name in IMAGES, f"unexpected image: {name}")
    require(isinstance(digest, str) and re.fullmatch(r"sha256:[a-f0-9]{64}", digest),
            f"invalid immutable digest for {name}")
    return REGISTRY + name + "@" + digest


def platform_digests(index):
    require(index.get("mediaType") in INDEX_TYPES, "expected multi-platform image index")
    result = {}
    for descriptor in index["manifests"]:
        platform = descriptor.get("platform", {})
        arch = platform.get("architecture")
        if descriptor.get("annotations", {}).get("vnd.docker.reference.type") == "attestation-manifest":
            continue
        if platform.get("os") == "unknown" and arch == "unknown":
            raise ValueError("unidentified index descriptor")
        require(platform.get("os") == "linux" and arch in ARCHES and arch not in result
                and descriptor.get("mediaType") in IMAGE_TYPES,
                f"unexpected or duplicate runnable platform: {platform}")
        digest = descriptor["digest"]
        require(re.fullmatch(r"sha256:[a-f0-9]{64}", digest), "invalid platform digest")
        result[arch] = digest
    require(set(result) == set(ARCHES), "expected both linux/amd64 and linux/arm64")
    return result


def inspect_image(name, digest):
    ref = reference(name, digest)
    args = ["docker", "buildx", "imagetools", "inspect"]
    observed = subprocess.check_output(args + ["--format", "{{.Manifest.Digest}}", ref],
                                       text=True).strip()
    require(observed == digest, f"registry index digest mismatch: {ref}")
    index = json.loads(subprocess.check_output(args + ["--raw", ref]))
    return {"reference": ref, "platforms": platform_digests(index)}


def create(commit, run, attempt, metadata_dir):
    lock = approved_inputs(commit, run, attempt)
    lock["images"] = {}
    for name in IMAGES:
        metadata = json.loads((metadata_dir / (name + ".json")).read_text())
        # Use this build's output, not a mutable tag lookup (including on reruns).
        lock["images"][name] = inspect_image(name, metadata["containerimage.digest"])
    return lock


def verify(lock, commit, run, attempt, inspect=inspect_image):
    expected = approved_inputs(commit, run, attempt)
    require(set(lock) == {*expected, "images"}, "unexpected image lock format")
    require(all(lock[k] == v for k, v in expected.items()),
            "image lock does not match approved checkout or producer run")
    require(set(lock["images"]) == set(IMAGES), "image lock has missing or extra images")
    for name, entry in lock["images"].items():
        digest = entry["reference"].rsplit("@", 1)[-1]
        require(entry["reference"] == reference(name, digest),
                f"unexpected registry, namespace or image: {name}")
        require(entry == inspect(name, digest), f"image lock and registry disagree: {name}")
    return lock["images"]["byoh-controller"]["reference"]


def main(args):
    mode, commit, run, attempt, path = args
    path = pathlib.Path(path)
    if mode == "create":
        print(json.dumps(create(commit, run, attempt, path), indent=2, sort_keys=True))
    elif mode == "verify":
        print(verify(json.loads(path.read_text()), commit, run, attempt))
    else:
        raise ValueError("expected create or verify")


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except (ValueError, KeyError, TypeError, AttributeError, OSError, subprocess.CalledProcessError) as exc:
        sys.exit(f"STOP: {exc}")
