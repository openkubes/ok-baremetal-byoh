# Testing

This document is the test contract for `ok-baremetal-byoh`: what the tests are,
what each needs to run, what side effects they have, and how to run them
reproducibly. It describes the project **as it is today** (inherited from
upstream `superbrothers`); it does not change any test or workflow.

## Test tiers

Tests fall into four tiers by their real prerequisites:

| Tier | Prerequisites | Runs off-Linux / without Docker? |
|---|---|---|
| **unit** | Go only (deps injected / fakes) | ✅ anywhere |
| **envtest** | Go + `KUBEBUILDER_ASSETS` (an ephemeral `kube-apiserver`+`etcd` from `setup-envtest`) | ✅ Linux & macOS; no Docker |
| **agent-integration** | Linux + a real Docker daemon + the `byoh/node:e2e` host image | ❌ Linux + Docker only |
| **e2e** | Linux + Docker + `kind` + cluster templates | ❌ Linux + Docker only |

## Inventory (37 test files, 9 packages)

| Package | Files | Tier | Needs envtest | Needs Docker | Touches real host FS / exec |
|---|---|---|---|---|---|
| `installer` | 2 | unit | – | – | – |
| `agent/cloudinit` | 4 | unit | – | – | – (counterfeiter fakes) |
| `agent/version` | 1 | unit | – | – | – |
| `apis/infrastructure/v1beta1` | 5 | envtest | ✅ | – | – |
| `controllers/infrastructure` | 6 | envtest | ✅ | – | – |
| `agent/reconciler` | 2 | envtest | ✅ | – | – |
| `agent/registration` | 5 | envtest | ✅ | – | ⚠ one case (`host_registrar_internal_test.go`) calls real `os.ReadFile` for `/etc/os-release`; a permission assertion is privilege-dependent |
| `agent` (host-agent suite) | 4 | agent-integration | ✅ | ✅ creates/stops/removes containers | ⚠ reads host OS, runs host-agent flow |
| `test/e2e` | 8 | e2e | – | ✅ + `kind` | ⚠ creates kind clusters + containers |

## Side effects & security boundaries

Explicit list of tests that can affect resources outside the Go process:

- **`agent` (host-agent suite)** — connects to the host **Docker daemon**
  (`dClient.NewClientWithOpts(FromEnv…)`) and creates/stops/removes containers
  that act as fake BYO hosts. Side effect: Docker containers on the host.
- **`test/e2e`** — creates **kind clusters** and containers; heaviest side
  effect. Must run only against disposable, test-created clusters — **never**
  an existing management cluster.
- **deb-package test** (`hack/test-host-agent-deb.sh`, CI `build-agent` job) —
  installs/removes the `byoh-hostagent` **systemd service**; CI runs it on a
  throwaway runner. Do not run on a workstation you care about.
- **`installer` CI** (`hack/test-installer.sh`) — downloads from `pkgs.k8s.io`,
  GitHub and the Ubuntu archive and runs inside containers.
- **envtest tiers** — spin an **ephemeral** local `kube-apiserver`+`etcd`; no
  real cluster, no credentials beyond the throwaway ones envtest mints. No
  external side effects.

**Credential invariant:** no test uses real cloud/Scaleway credentials; all
bootstrap tokens, CSRs and kubeconfigs are generated inside envtest or against
disposable containers. CI must never inject real Scaleway credentials.

**Runner isolation is not automatic.** A GitHub-hosted runner is ephemeral *per
job*, but ephemerality alone does **not** guarantee Docker isolation or least
privilege *within* a job: all steps in a job share one Docker daemon (and
membership of the `docker` group is effectively root-equivalent), steps commonly
run as root, and concurrent tests on the same runner can see each other's
containers. "Throwaway runner" therefore bounds *blast radius across jobs*, not
*isolation within a job*. M1.3c must verify these boundaries explicitly — unique
per-run resource names, scoped cleanup that cannot touch unrelated containers,
and running suites non-root where an assertion depends on it (without assuming
non-root implies safe Docker access).

## Supported test matrix

| Component | Arch | OS |
|---|---|---|
| Management / controller (envtest, controller & webhook tests) | **arm64** + amd64 | Linux; macOS for envtest & unit tiers |
| Host agent (agent-integration, deb, installer) | **amd64** + arm64 | **Linux** (Ubuntu 24.04 / 26.04) |

**Kubernetes version status (read carefully):**
- **OpenKubes target: Kubernetes 1.35.** Verified **locally only** so far — envtest
  `build`/`vet` + the controller, webhook and reconciler suites passed on a
  developer Mac M5 (Go 1.26.10, arm64) against K8s 1.35 envtest binaries.
- **Not yet validated by OpenKubes Linux CI.** No OpenKubes CI run has exercised
  1.35; a developer-machine result is not a CI guarantee. Pinning envtest to 1.35
  and proving it green in CI is an M1.3b deliverable.
- **Upstream CI default remains `1.36.2`** (`ENVTEST_K8S_VERSION` in the Makefile);
  OpenKubes adds 1.36 as a matrix entry only after the 1.35 baseline is stable.

## Reproducible local commands

Prerequisite for envtest tiers — provision assets once:

```bash
# installs setup-envtest + ginkgo into ./hack/tools/bin on first use (via make)
make controller-test     # envtest: controllers/infrastructure (no Docker)
make webhook-test        # envtest: apis/infrastructure/v1beta1 (no Docker)
```

| Goal | Command | Needs |
|---|---|---|
| Format + vet | `make fmt vet` | Go |
| Lint | `make lint` | Go (golangci-lint via tools) |
| Generated-file drift check | `make generate manifests cluster-templates cluster-templates-e2e fmt && git diff --exit-code` | Go |
| envtest — controllers | `make controller-test` | envtest |
| envtest — webhooks | `make webhook-test` | envtest |
| Full non-e2e suite (envtest + agent) | `make test-coverage` | envtest + **Docker** |
| Agent suite only | `make agent-test` | envtest + **Docker** |
| E2E (all specs) | `make test-e2e` | Docker + kind |
| E2E (PR-blocking only) | `make test-e2e-pr-blocking` | Docker + kind |

**macOS / no-Docker dev boxes** can run `fmt`, `vet`, `lint`, `make
controller-test`, `make webhook-test`, and the unit-tier packages. The
agent-integration and e2e tiers require Linux + Docker (see below).

## Known pre-existing cross-platform behavior

On a non-Linux / non-Docker host, these fail **by prerequisite, not by defect**
(identical on upstream at the same commit):

- host-agent suite — no `/var/run/docker.sock`.
- `agent/registration` — one case reads the real `/etc/os-release`
  (absent on macOS); a permission assertion depends on OS/privilege (running as
  root bypasses Linux DAC and invalidates a "permission denied" expectation).
- Ginkgo cleanup may panic after a setup failure (cascade from the missing
  prerequisite), masking the real cause.

These are tracked for focused, upstreamable fixes (inject the OS-release reader
at unit tier; nil-guard cleanup; run the Linux suite non-root) — **without
weakening the assertions**. The authoritative run for all tiers is Linux CI.

## Authoritative CI workflows (preserve)

The following GitHub Actions workflows are the source of truth for a green
build and must be preserved (changed only with a documented reason):

| Workflow | What it guarantees |
|---|---|
| `ci.yml` → `verify` | generated-file drift, `fmt`, `vet` |
| `ci.yml` → `test` | `make test-coverage` on ubuntu-24.04 **with Docker** (envtest + agent) |
| `ci.yml` → `build-agent` | agent deb build + install/remove test on **amd64 and arm64** |
| `ci.yml` → `build-image` | multi-arch (`amd64`,`arm64`) manager image build |
| `lint.yml` | `golangci-lint` |
| `e2e.yml` | `[PR-Blocking]` spec on PRs (amd64); full matrix amd64+arm64 weekly; drafts skipped |
| `installer.yml` | installer scripts on Ubuntu 24.04/26.04 × amd64/arm64 |
| `draft-release.yaml` | multi-arch image + release artifacts on `v*.*.*` tags |

OpenKubes extends this baseline (reproducibility, image pinning, machine-readable
reports, and a security-regression tier) through separate, reviewable PRs; it
does not rebuild it.
