# BYOH bootstrap token security: lifecycle contract (D1)

This document captures the security boundary for
`BootstrapKubeconfig` and its `kube-system/bootstrap-token-*` Secret.
It does **not** imply that revocation-on-delete is implemented.

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

## Revocation and cleanup: remaining security gate

`BootstrapKubeconfig` is namespaced, but Kubernetes bootstrap-token Secrets
must be in `kube-system`. A cross-namespace ownerReference is invalid for
garbage collection. Consequently, adding an ownerReference on the Secret is
**not** a valid solution.

The configured Secret expiration limits the token's authentication lifetime,
subject to the Kubernetes bootstrap-token authenticator being enabled and
respecting expiry. Kubernetes also has an optional bootstrap token cleaner,
but the code in PR #13 does **not** establish that the target management
cluster runs it. Expiration is **not** equivalent to immediate revocation,
and it does not prove that the physical Secret has been removed.

Before a production or exposed-host enrollment flow, implement and verify an
explicit revocation-on-delete contract in a separate, reviewable change:

1. Add a finalizer to `BootstrapKubeconfig` **before** issuing its Secret,
   including safe adoption/transition rules for existing requests without it.
2. On deletion, identify the Secret strictly by request UID and token ID.
   Verify Secret type and ownership before deletion; never delete collisions,
   unannotated Secrets, or foreign Secrets.
3. Handle already-absent Secrets idempotently, preserve finalizer and retry
   on transient API failures, and remove finalizer only after revocation
   has been verified. Define behavior when ownership verification fails,
   without allowing permanent silent orphaning.
4. Cover deletion-before-issuance, deletion-after-issuance, concurrent
   reconciliation, expired Secret, missing Secret, name collision, and API
   delete failures with regression tests.
5. Verify revocation against a real Kubernetes API server and confirm that
   subsequent authentication with the old token fails. Keep CSR approval
   manual until the host enrollment identity and authorization review is done.

The present code is appropriate for *isolated development review only*.
Do not install or enable the BYOH controller for live host enrollment based
solely on green unit tests or a green E2E suite.
