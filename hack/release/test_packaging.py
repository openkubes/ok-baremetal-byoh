"""Offline regression tests. Registry calls are mocked; DEBs use real dpkg."""
import importlib.util
import json
import os
import re
from pathlib import Path
import shutil
import struct
import subprocess
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
spec = importlib.util.spec_from_file_location("image_lock", HERE / "image-lock.py")
lock_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lock_module)
SHA = "a" * 40
INDEX_DIGEST = "sha256:" + "b" * 64
AMD = "sha256:" + "c" * 64
ARM = "sha256:" + "d" * 64


def index():
    media = "application/vnd.oci.image"
    return {"mediaType": media + ".index.v1+json", "manifests": [
        {"mediaType": media + ".manifest.v1+json", "digest": digest,
         "platform": {"os": "linux", "architecture": arch}}
        for arch, digest in (("amd64", AMD), ("arm64", ARM))
    ] + [{"annotations": {"vnd.docker.reference.type": "attestation-manifest",
                          "vnd.docker.reference.digest": AMD},
          "mediaType": media + ".manifest.v1+json",
          "digest": "sha256:" + "e" * 64,
          "platform": {"os": "unknown", "architecture": "unknown"}}]}


def image(name, digest):
    return {"reference": lock_module.reference(name, digest),
            "platforms": {"amd64": AMD, "arm64": ARM}}


class ImageLockTest(unittest.TestCase):
    def setUp(self):
        self.lock = lock_module.approved_inputs(SHA, "12", "1")
        self.lock["images"] = {name: image(name, INDEX_DIGEST) for name in lock_module.IMAGES}

    def test_two_platforms(self):
        self.assertEqual(lock_module.platform_digests(index()), {"amd64": AMD, "arm64": ARM})

    def test_missing_or_duplicate_platform(self):
        for manifests in (index()["manifests"][:1], index()["manifests"] * 2):
            with self.assertRaises(ValueError):
                lock_module.platform_digests({**index(), "manifests": manifests})

    def test_malformed_attestations_and_shared_runnable_digest(self):
        malformed = [
            (2, "platform", {"os": "linux", "architecture": "amd64"}),
            (2, "annotations", {}),
            (2, "mediaType", "application/octet-stream"),
            (2, "digest", AMD),
            (2, "digest", "not-a-digest"),
            (2, "annotations", {"vnd.docker.reference.type": "attestation-manifest",
                                "vnd.docker.reference.digest": INDEX_DIGEST}),
            (1, "digest", AMD),
        ]
        for position, field, value in malformed:
            with self.subTest(position=position, field=field, value=value):
                bad = index()
                bad["manifests"][position][field] = value
                with self.assertRaises(ValueError):
                    lock_module.platform_digests(bad)

    def test_build_output_not_mutable_tag(self):
        observed = []

        def registry(args, **kwargs):
            observed.append(args[-1])
            return json.dumps(index()).encode() if "--raw" in args else INDEX_DIGEST + "\n"

        with tempfile.TemporaryDirectory() as t, patch.object(subprocess, "check_output", registry):
            for name in lock_module.IMAGES:
                (Path(t) / (name + ".json")).write_text(json.dumps({"containerimage.digest": INDEX_DIGEST}))
            self.assertEqual(lock_module.create(SHA, "12", "1", Path(t)), self.lock)
        self.assertEqual(set(observed), {entry["reference"] for entry in self.lock["images"].values()})

    def test_registry_digest_mismatch(self):
        with patch.object(subprocess, "check_output", return_value=ARM), self.assertRaises(ValueError):
            lock_module.inspect_image("byoh-controller", INDEX_DIGEST)

    def test_wrong_source_or_producer(self):
        for commit, run, attempt in (("e" * 40, "12", "1"), (SHA, "13", "1"), (SHA, "12", "2")):
            with self.assertRaises(ValueError):
                lock_module.verify(self.lock, commit, run, attempt, image)

    def test_reject_wrong_registry_tag_and_platform_digest(self):
        name = "byoh-controller"
        for ref in ("ghcr.io/superbrothers/byoh-controller@" + INDEX_DIGEST,
                    "ghcr.io/openkubes/byoh-controller:latest"):
            self.lock["images"][name]["reference"] = ref
            with self.assertRaises(ValueError):
                lock_module.verify(self.lock, SHA, "12", "1", image)
        self.lock["images"][name] = image(name, INDEX_DIGEST)
        self.assertEqual(lock_module.verify(self.lock, SHA, "12", "1", image),
                         self.lock["images"][name]["reference"])
        self.lock["images"][name]["platforms"]["arm64"] = AMD
        with self.assertRaises(ValueError):
            lock_module.verify(self.lock, SHA, "12", "1", image)


class PushAndTagTest(unittest.TestCase):
    def test_make_push_allowlist_and_local_build(self):
        with tempfile.TemporaryDirectory() as t:
            sentinel = Path(t) / "docker-called"
            fake = Path(t) / "docker"
            fake.write_text("#!/bin/sh\ntouch " + str(sentinel) + "\n")
            fake.chmod(0o755)
            env = {**os.environ, "PATH": t + os.pathsep + os.environ["PATH"]}
            sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
            for target, var, name in (("docker-push", "IMG", "byoh-controller"),
                                      ("docker-buildx", "IMG", "byoh-controller"),
                                      ("docker-buildx-guard", "IMG_GUARD", "no-replacement-guard"),
                                      ("docker-buildx-extension", "IMG_EXTENSION", "inplace-extension")):
                for ref in (f"ghcr.io/superbrothers/{name}:git-{sha[:12]}",
                            f"quay.io/openkubes/{name}:git-{sha[:12]}",
                            f"ghcr.io/other/{name}:git-{sha[:12]}",
                            f"ghcr.io/openkubes/wrong-image:git-{sha[:12]}",
                            f"ghcr.io/openkubes/{'inplace-extension' if name != 'inplace-extension' else 'byoh-controller'}:git-{sha[:12]}",
                            f"ghcr.io/openkubes/{name}:latest",
                            f"ghcr.io/openkubes/{name}:v1.2.3",
                            f"ghcr.io/openkubes/{name}:git-{'a' * 12 if sha[:12] != 'a' * 12 else 'b' * 12}"):
                    result = subprocess.run(["make", target, "DOCKER_BUILDX_PUSH=true", var + "=" + ref],
                                            cwd=ROOT, env=env, capture_output=True, text=True)
                    self.assertNotEqual(result.returncode, 0, ref)
                    self.assertIn("STOP: push", result.stderr)
                    self.assertFalse(sentinel.exists())
                subprocess.run(["make", target, "DOCKER_BUILDX_PUSH=true",
                                var + f"=ghcr.io/openkubes/{name}:git-{sha[:12]}"],
                               cwd=ROOT, env=env, check=True, capture_output=True)
                self.assertTrue(sentinel.exists())
                sentinel.unlink()
            subprocess.run(["make", "docker-buildx", "IMG=localhost/byoh:dev",
                            "DOCKER_BUILDX_PUSH=false"], cwd=ROOT, env=env,
                           check=True, capture_output=True)
            self.assertTrue(sentinel.exists())

    def test_exact_pinned_action_inputs_and_staging(self):
        workflow = (ROOT / ".github/workflows/draft-release.yaml").read_text()
        action = re.search(
            r"      - name: Create the draft release\n"
            r"        uses: softprops/action-gh-release@8a0cd651e5b2365bd640854a3fa5dfc70e4875f3[^\n]*\n"
            r"(.*)\Z", workflow, re.S)
        self.assertIsNotNone(action)
        self.assertRegex(action[1], r"(?m)^          working_directory: _dist$")
        self.assertRegex(action[1], r"(?m)^          overwrite_files: false$")
        self.assertRegex(action[1], r"(?m)^          draft: true$")
        self.assertNotRegex(action[1], r"(?m)^          working-directory:")
        self.assertLess(workflow.index("bash hack/release/require-new-release.sh"), action.start())
        self.assertEqual(workflow.count("name: byoh-release-bundle-attempt-"), 2)
        self.assertIn("cancel-in-progress: false", workflow)
        package_step = workflow.split("      - name: Package and verify release", 1)[1].split("      - name: ", 1)[0]
        self.assertNotIn("GH_TOKEN", package_step)

    def test_release_api_fail_closed_across_pages_and_reruns(self):
        with tempfile.TemporaryDirectory() as t:
            path = Path(t)
            gh = path / "gh"
            gh.write_text('#!/bin/sh\n[ "$1" = api ] && [ "$2" = --paginate ] || exit 99\n'
                          '[ "${MOCK_FAIL:-}" != true ] || exit 42\nprintf "%s" "$MOCK_PAGES"\n')
            gh.chmod(0o755)
            tag = "v1.2.3"
            cases = (
                ("[]", False, 0, ""),
                ('[{"tag_name":"v0.0.1","draft":false}]\n[{"tag_name":"v1.2.3","draft":false}]', False, 1, "published"),
                ('[{"tag_name":"v1.2.3","draft":true}]', False, 1, "manual reconciliation"),
                ('[{"tag_name":"v1.2.3","draft":true,"assets":[{"name":"partial"}]}]', False, 1, "manual reconciliation"),
                ('[{"tag_name":"v1.2.3"}]', False, 1, "classify"),
                ("invalid-json", False, 1, "classify"),
                ("", True, 1, "cannot verify"),
            )
            for pages, fail, status, diagnostic in cases:
                env = {**os.environ, "PATH": t + os.pathsep + os.environ["PATH"],
                       "MOCK_PAGES": pages, "MOCK_FAIL": str(fail).lower()}
                result = subprocess.run(["bash", HERE / "require-new-release.sh", tag],
                                        env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, status, result.stderr)
                self.assertIn(diagnostic, result.stderr)

    def test_no_upstream_or_registry_write_in_tag_workflow(self):
        draft = (ROOT / ".github/workflows/draft-release.yaml").read_text()
        publisher = (ROOT / ".github/workflows/publish-ghcr.yml").read_text()
        for text in (draft, publisher, (ROOT / "config/manager/manager.yaml").read_text()):
            self.assertNotIn("ghcr.io/superbrothers", text)
        for token in ("packages: write", "DOCKER_BUILDX_PUSH", "docker push", "docker login"):
            self.assertNotIn(token, draft)
        self.assertIn("github.event.workflow_run.head_sha", publisher)
        self.assertIn("git rev-parse HEAD^{commit}", publisher)

    def test_only_download_from_trusted_successful_producer(self):
        with tempfile.TemporaryDirectory() as t:
            root = Path(t)
            name = f"byoh-image-lock-{SHA}-attempt-1"
            mock = root / "gh"
            mock.write_text("""#!/usr/bin/env bash
set -eu
case "$*" in
  *'/attempts/1') printf '%s' "$MOCK_RUN";;
  *'/artifacts'*) printf '%s' "$MOCK_ARTIFACT";;
  'run download'*) out="${@: -1}"; printf '{}' > "$out/image-lock.json";;
  *) exit 91;;
esac
""")
            mock.chmod(0o755)
            run = dict(id=12, run_attempt=1, repository=dict(full_name=lock_module.REPOSITORY),
                       head_repository=dict(full_name=lock_module.REPOSITORY),
                       path=".github/workflows/publish-ghcr.yml", head_branch="main",
                       event="workflow_run", status="completed", conclusion="success")
            artifact = dict(total_count=1, artifacts=[dict(name=name, expired=False)])
            for fields, metadata, expected in (({}, {}, True), ({"conclusion": "failure"}, {}, False),
                                               ({"head_branch": "fix"}, {}, False),
                                               ({}, {"total_count": 2}, False),
                                               ({}, {"total_count": 0, "artifacts": []}, False),
                                               ({}, {"artifacts": [dict(name=name, expired=True)]}, False)):
                out = root / "lock"
                env = {**os.environ, "PATH": t + os.pathsep + os.environ["PATH"],
                       "MOCK_RUN": json.dumps({**run, **fields}),
                       "MOCK_ARTIFACT": json.dumps({**artifact, **metadata})}
                result = subprocess.run(["bash", HERE / "fetch-image-lock.sh", SHA, "12", "1", out],
                                        env=env, capture_output=True)
                self.assertEqual(result.returncode == 0, expected, result.stderr)
                self.assertEqual((out / "image-lock.json").exists(), expected)
                if expected:
                    shutil.rmtree(out)

    def test_annotated_source_matches_checked_out_commit(self):
        with tempfile.TemporaryDirectory() as t:
            def git(*args):
                return subprocess.check_output(["git", "-C", t, *args], text=True).strip()

            git("init", "-q")
            git("-c", "user.name=Fixture", "-c", "user.email=test@example.invalid",
                "commit", "--allow-empty", "-qm", "fixture")
            commit = git("rev-parse", "HEAD")
            for version, sha in (("v1.0.0", commit), ("v1.0.1", SHA)):
                git("-c", "user.name=Fixture", "-c", "user.email=test@example.invalid",
                    "tag", "-am", f"BYOH_RELEASE_COMMIT={sha}\nBYOH_IMAGE_RUN_ID=12\nBYOH_IMAGE_RUN_ATTEMPT=1", version)
                result = subprocess.run(["bash", HERE / "tag-inputs.sh", "refs/tags/" + version],
                                        cwd=t, capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, sha == commit)
            keys = [f"BYOH_RELEASE_COMMIT={commit}", "BYOH_IMAGE_RUN_ID=12", "BYOH_IMAGE_RUN_ATTEMPT=1"]
            cases = []
            for pos, entry in enumerate(keys):
                key = entry.split("=")[0]
                cases.extend([
                    (keys + [entry], "duplicate", key),
                    (keys + [f"{key}=777"], "duplicate", key),
                    (keys[:pos] + keys[pos+1:], "missing", key),
                    (keys[:pos] + [f"{key}=bad"] + keys[pos+1:], "malformed", key),
                    (keys + [f" {entry}"], "malformed", key),
                ])
            for i, (lines, diagnostic, key) in enumerate(cases):
                tag = f"v2.0.{i}"
                git("-c", "user.name=Fixture", "-c", "user.email=test@example.invalid",
                    "tag", "-am", "\n".join(lines), tag)
                result = subprocess.run(["bash", HERE / "tag-inputs.sh", "refs/tags/" + tag],
                                        cwd=t, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0, lines)
                self.assertIn(f"{diagnostic} tag annotation: {key}", result.stderr)
            git("tag", "v1.0.2")
            self.assertNotEqual(subprocess.run(["bash", HERE / "tag-inputs.sh", "refs/tags/v1.0.2"],
                                               cwd=t, capture_output=True).returncode, 0)


class BundleTest(unittest.TestCase):
    def test_bundle_rejects_stale_arch_and_verifies_both_debs(self):
        with tempfile.TemporaryDirectory() as t:
            root = Path(t)
            bundle = root / "bundle"
            bundle.mkdir()
            controller = lock_module.reference("byoh-controller", INDEX_DIGEST)
            for name in ("metadata.yaml", "cluster-template.yaml", "cluster-template-topology.yaml",
                         "clusterclass-quickstart.yaml", "cluster-template-docker.yaml",
                         "cluster-template-topology-docker.yaml", "clusterclass-quickstart-docker.yaml",
                         "image-lock.json"):
                (bundle / name).write_text("{}\n")
            (bundle / "infrastructure-components.yaml").write_text(f"  image: {controller}\n")
            for arch, machine in (("amd64", 62), ("arm64", 183)):
                body = struct.pack("<16sHHIQQQIHHHHHH", b"\x7fELF\x02\x01\x01", 2, machine,
                                   1, 0, 0, 0, 0, 64, 0, 0, 0, 0, 0) + SHA.encode()
                filename = bundle / ("byoh-hostagent-linux-" + arch)
                filename.write_bytes(body)
                package = root / arch
                (package / "DEBIAN").mkdir(parents=True)
                (package / "DEBIAN/control").write_text(
                    "Package: byoh-hostagent\nVersion: 1.2.3~rc.1\n"
                    f"Architecture: {arch}\nMaintainer: Test <test@example.invalid>\nDescription: Fixture\n")
                (package / "usr/bin").mkdir(parents=True)
                shutil.copyfile(filename, package / "usr/bin/byoh-hostagent")
                subprocess.run(["dpkg-deb", "--build", package,
                                bundle / f"byoh-hostagent_1.2.3-rc.1_{arch}.deb"],
                               check=True, capture_output=True)

            def verify():
                return subprocess.run(["bash", HERE / "verify-bundle.sh", bundle,
                                       SHA, "1.2.3-rc.1", controller], capture_output=True, text=True)

            result = verify()
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(len((bundle / "checksums.txt").read_text().splitlines()), 13)
            (bundle / "checksums.txt").unlink()
            stale = bundle / "byoh-hostagent-linux-arm64"
            stale.write_bytes((bundle / "byoh-hostagent-linux-amd64").read_bytes())
            self.assertNotEqual(verify().returncode, 0)
