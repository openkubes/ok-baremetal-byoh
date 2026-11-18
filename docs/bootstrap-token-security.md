# BYOH bootstrap token security: lifecycle contract (D1)

This document captures the security boundary for
`BootstrapKubeconfig` and its `kube-system/bootstrap-token-*` Secret.
Revocation-on-delete is implemented and has been verified on an
isolated kind management cluster using TokenReview and direct API
authentication checks.

## Implemented by PR #13

- The bootstrap token Secret name is derived from the immutable request UID,
  so retries after status patch failures and manager restarts recover the same
  credential rather than minting unlimited Secrets.
- The bootstrap token is valid for at most **30 minutes** from creation.
  Recovering an expired token fails closed; it does not silently rotate.
- The Secret must have the expected bootstrap-token type, exact token ID,
  a valid token value, expiration and matching request-UID annotation.
- Existing Secrets that claim a different request UID must not be read,
  overwritten or deleted. In particular, the 6-character token ID is a
  24-bit hash prefix, **not collision-free**. A collision prevents issuance for
  the later request; it never authorizes use of the previous request's Secret.
- Status patch failure and collision regression tests run against a fake
  Kubernetes API; this is not a complete apiserver/RBAC integration proof.

## Implemented revocation-on-delete

The controller persists a finalizer before issuing a bootstrap token.
When a BootstrapKubeconfig is deleted, the controller identifies its
kube-system token Secret using the immutable request UID.

It verifies Secret ownership and type before deletion, uses Kubernetes
UID and resourceVersion preconditions, and confirms that the Secret is
absent before removing the finalizer.

Missing Secrets are handled idempotently. Foreign or conflicting
Secrets fail closed; the controller does not delete them.

Cross-namespace ownerReferences are not used for garbage collection.

### Live verification on kind

An isolated test on ok-byoh-make-test verified:

- Bootstrap token authentication before deletion.
- The expected system:bootstrap identity via TokenReview.
- The same identity via a direct authenticated API request.
- Deletion of the BootstrapKubeconfig and its owned token Secret.
- Rejection of the previous token through TokenReview.
- HTTP 401 for the previous token on a direct API request.
- Successful cleanup without remaining test requests.

The live test is implemented in
hack/dev/test-byoh-bootstrap-revocation-live.py.

These observations establish functional revocation in the tested
local kind environment, not universal behavior across Kubernetes
distributions or production configurations.

### Remaining security gates

Before production or exposed-host enrollment, review and test:

- Existing requests created before the finalizer was introduced.
- Concurrent deletion, Secret replacement and controller restarts.
- Token ownership collisions and stuck-finalizer recovery.
- Failure injection and incomplete test cleanup.
- Restricted controller RBAC and admission authorization.
- Host identity binding, CSR approval and certificate lifecycle.

Manual CSR approval remains mandatory in the local test workflow.

Bootstrap-token expiration and optional token cleanup remain defense
in depth, not a replacement for explicit revocation.
