#!/usr/bin/env python3
"""Live bootstrap-token revocation test for isolated ok-byoh-make-test."""

import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid
import ssl
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
KC = Path.home() / ".config/ok/byoh-make-test/kubeconfig"
NAME = "revocation-test-" + uuid.uuid4().hex[:12]
NAMESPACE = "default"
RESOURCE = "bootstrapkubeconfig"
TIMEOUT = 120


def command(args, data=None):
    result = subprocess.run(
        args,
        input=json.dumps(data) if data is not None else None,
        text=True,
        capture_output=True,
        timeout=45,
        check=False,
    )
    if result.returncode:
        raise RuntimeError(
            "Command failed (exit {}): {}".format(
                result.returncode, " ".join(args)
            )
        )
    return result.stdout


def kube(*args, data=None):
    return command(
        ["kubectl", "--kubeconfig", str(KC), *args],
        data=data,
    )


def kube_json(*args, data=None):
    output = kube(*args, data=data)
    return json.loads(output) if output.strip() else None


def get_optional(resource, name, namespace=None):
    args = ["get", resource, name, "--ignore-not-found", "-o", "json"]
    if namespace:
        args.extend(["-n", namespace])
    return kube_json(*args)


def wait_for(description, predicate):
    deadline = time.monotonic() + TIMEOUT
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(2)
    raise RuntimeError("Timed out: " + description)


def review(token):
    response = kube_json(
        "create", "-f", "-", "-o", "json",
        data={
            "apiVersion": "authentication.k8s.io/v1",
            "kind": "TokenReview",
            "spec": {"token": token},
        },
    )
    return response["status"]




def token_rejected(status):
    """Accept only explicit, unambiguous authentication rejection."""
    authenticated = status.get("authenticated")
    error = status.get("error")
    user = status.get("user")

    if authenticated is True:
        if error:
            raise RuntimeError(
                "Contradictory TokenReview: authenticated with error"
            )
        return False

    if user:
        raise RuntimeError(
            "Unexpected identity in rejected TokenReview"
        )

    if error == "invalid bearer token":
        if authenticated is False or authenticated is None:
            return True

    if authenticated is False and not error:
        return True

    raise RuntimeError("Unexpected TokenReview result")


def direct_identity(server, ca_data, token):
    """Authenticate directly to the API server, bypassing TokenReview."""
    ca_pem = base64.b64decode(ca_data).decode("ascii")
    context = ssl.create_default_context(cadata=ca_pem)

    body = json.dumps({
        "apiVersion": "authentication.k8s.io/v1",
        "kind": "SelfSubjectReview",
    }).encode()

    request = urllib.request.Request(
        server.rstrip("/") +
        "/apis/authentication.k8s.io/v1/selfsubjectreviews",
        data=body,
        headers={
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
        },
        method="POST",
    )

    try:
        with urllib.request.urlopen(
            request, context=context, timeout=15
        ) as response:
            result = json.load(response)
            user = result.get("status", {}).get("userInfo", {})
            return response.status, user.get("username")
    except urllib.error.HTTPError as exc:
        return exc.code, None


def check_target():
    if not KC.is_file():
        raise RuntimeError("Expected dedicated test kubeconfig missing")

    context = kube("config", "current-context").strip()
    if context != "kind-ok-byoh-make-test":
        raise RuntimeError("Unexpected kubeconfig context")

    env = os.environ.copy()
    env["BYOH_KIND_NAME"] = "ok-byoh-make-test"
    result = subprocess.run(
        ["bash", str(ROOT / "hack/dev/byoh-kind.sh"), "status"],
        env=env,
        capture_output=True,
        text=True,
        timeout=45,
        check=False,
    )
    if result.returncode:
        raise RuntimeError("Kind cluster identity verification failed")


def main():
    if os.environ.get("BYOH_LIVE_REVOCATION_ACK") != "yes":
        raise RuntimeError("Explicit BYOH_LIVE_REVOCATION_ACK=yes required")

    check_target()
    config = kube_json("config", "view", "--raw", "--minify", "-o", "json")
    cluster = config["clusters"][0]["cluster"]
    server = cluster["server"]
    ca = cluster["certificate-authority-data"]

    request = {
        "apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
        "kind": "BootstrapKubeconfig",
        "metadata": {"name": NAME, "namespace": NAMESPACE},
        "spec": {
            "apiserver": server,
            "certificate-authority-data": ca,
        },
    }

    secret_name = None
    created = False
    try:
        kube_json("create", "-f", "-", "-o", "json", data=request)
        created = True
        print("PASS: Test request created:", NAME)

        def issued():
            obj = get_optional(RESOURCE, NAME, NAMESPACE)
            if not obj:
                return None
            finalizer = (
                "infrastructure.cluster.x-k8s.io/bootstrap-token-cleanup"
            )
            if finalizer not in obj["metadata"].get("finalizers", []):
                return None
            if not obj.get("status", {}).get("bootstrapKubeconfigData"):
                return None
            return obj

        obj = wait_for("bootstrap credential issuance", issued)
        uid = obj["metadata"]["uid"]
        token_id = hashlib.sha256(uid.encode()).hexdigest()[:6]
        secret_name = "bootstrap-token-" + token_id

        secret = get_optional("secret", secret_name, "kube-system")
        if not secret:
            raise RuntimeError("Expected bootstrap Secret missing")

        if (
            secret["type"] != "bootstrap.kubernetes.io/token"
            or secret["metadata"].get("annotations", {}).get(
                "byoh.openkubes.io/bootstrap-request-uid"
            ) != uid
        ):
            raise RuntimeError("Bootstrap Secret ownership mismatch")

        data = secret["data"]
        actual_id = base64.b64decode(data["token-id"]).decode()
        token_secret = base64.b64decode(data["token-secret"]).decode()
        if actual_id != token_id:
            raise RuntimeError("Bootstrap token ID mismatch")

        token = token_id + "." + token_secret
        before = review(token)
        expected_user = "system:bootstrap:" + token_id

        if (
            before.get("authenticated") is not True
            or before.get("user", {}).get("username") != expected_user
        ):
            raise RuntimeError("Bootstrap authentication not established")

        print("PASS: Token authenticated with expected identity")

        direct_code, direct_user = direct_identity(
            server, ca, token
        )
        if direct_code != 201 or direct_user != expected_user:
            raise RuntimeError(
                "Direct API bootstrap identity verification failed"
            )

        print("PASS: Direct API authenticated bootstrap identity")

        kube("delete", RESOURCE, NAME, "-n", NAMESPACE)
        print("PASS: Revocation requested")

        def revoked():
            request_gone = not get_optional(RESOURCE, NAME, NAMESPACE)
            secret_gone = not get_optional(
                "secret", secret_name, "kube-system"
            )
            return request_gone and secret_gone

        wait_for("request and Secret removal", revoked)
        print("PASS: Request and owned Secret removed")

        def authentication_denied():
            return token_rejected(review(token))

        wait_for("old token authentication rejection", authentication_denied)
        print("PASS: Old token no longer authenticates")

        direct_code, direct_user = direct_identity(
            server, ca, token
        )
        if direct_code != 401:
            raise RuntimeError(
                "Direct API revocation not proven; HTTP status "
                + str(direct_code)
            )

        print("PASS: Direct API rejected revoked token (HTTP 401)")
        print("PASS: Live bootstrap-token revocation verified")

    finally:
        cleanup_failures = []
        if created:
            try:
                remaining = get_optional(RESOURCE, NAME, NAMESPACE)
                if remaining:
                    kube(
                        "delete", RESOURCE, NAME,
                        "-n", NAMESPACE, "--wait=false"
                    )
                def cleaned():
                    return not get_optional(RESOURCE, NAME, NAMESPACE)
                wait_for("test request cleanup", cleaned)
            except Exception as exc:
                cleanup_failures.append("request cleanup failed")
                print(
                    "CLEANUP WARNING: inspect test request", NAME,
                    str(exc), file=sys.stderr
                )
            if secret_name:
                try:
                    leftover = get_optional(
                        "secret", secret_name, "kube-system"
                    )
                    if leftover:
                        cleanup_failures.append("bootstrap Secret remains")
                        print(
                            "CLEANUP WARNING: bootstrap Secret remains:",
                            secret_name,
                            file=sys.stderr,
                        )
                except Exception:
                    cleanup_failures.append("Secret status unknown")
                    print(
                        "CLEANUP WARNING: Secret status unknown:",
                        secret_name,
                        file=sys.stderr,
                    )

        if cleanup_failures:
            original_error = sys.exception()
            raise RuntimeError(
                "Test cleanup incomplete: " +
                ", ".join(cleanup_failures)
            ) from original_error


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print("FAIL:", exc, file=sys.stderr)
        sys.exit(1)
