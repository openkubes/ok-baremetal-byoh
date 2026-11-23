# Two-host E2E lab helpers (provider-specific)

These helpers belong to the OpenKubes two-host E2E lab, which ran on Scaleway. Their
defaults are that lab's host names and addresses (`HOST_CP`, `HOST_WORKER`, `WORKER_SSH`,
`WORKER_ENDPOINT`); set them for your hosts. The enrollment steps themselves are generic
BYOH (`docs/byoh-host-onboarding.md`); `tests/offline/test-byoh-e2e.sh` tests them offline.
Lab records: `docs/scaleway-kubeadm-e2e/`.
