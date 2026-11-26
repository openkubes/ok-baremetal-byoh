# Ensure Make is run with bash shell as some syntax below is bash-specific
SHELL:=/usr/bin/env bash

# The registry of the image of the manager
REGISTRY ?= ghcr.io/openkubes

IMAGE_NAME ?= byoh-controller
TAG ?= dev
RELEASE_DIR := _dist

# Image URL to use all building/pushing image targets
IMG ?= ${REGISTRY}/${IMAGE_NAME}:${TAG}
BYOH_BASE_IMG = byoh/node:e2e
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)

REPO_ROOT := $(shell pwd)
# Go version of the container images that build the binaries, taken from the
# toolchain directive in go.mod.
GO_VERSION ?= $(shell sed -n 's/^toolchain go//p' go.mod)
GINKGO_FOCUS  ?=
GINKGO_SKIP ?=
GINKGO_NODES  ?= 1
E2E_CONF_FILE  ?= ${REPO_ROOT}/test/e2e/config/provider.yaml
ARTIFACTS ?= ${REPO_ROOT}/_artifacts
SKIP_RESOURCE_CLEANUP ?= false
USE_EXISTING_CLUSTER ?= false
EXISTING_CLUSTER_KUBECONFIG_PATH ?=
GINKGO_NOCOLOR ?= false

TOOLS_DIR := $(REPO_ROOT)/hack/tools
BIN_DIR := bin
TOOLS_BIN_DIR := $(TOOLS_DIR)/$(BIN_DIR)
GINKGO := $(TOOLS_BIN_DIR)/ginkgo
GINKGO_PKG := github.com/onsi/ginkgo/v2/ginkgo
SETUP_ENVTEST_VER := v0.25.2
SETUP_ENVTEST := $(TOOLS_BIN_DIR)/setup-envtest
# Kubernetes version of the control plane binaries that envtest runs.
ENVTEST_K8S_VERSION ?= 1.36.2
ENVTEST_ASSETS = KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use -p path --bin-dir $(TOOLS_BIN_DIR) $(ENVTEST_K8S_VERSION))"

BYOH_TEMPLATES := $(REPO_ROOT)/test/e2e/data/infrastructure-provider-byoh

LDFLAGS := -w -s $(shell hack/version.sh)
STATIC=-extldflags '-static'

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.DEFAULT_GOAL := help

all: build

HOST_AGENT_DIR ?= agent
# The architectures to build the manager image and the host agent for.
ALL_ARCH ?= amd64 arm64
HOST_AGENT_ARCHS ?= $(ALL_ARCH)

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# https://linuxcommand.org/lc3_adv_awk.php

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

manifests: controller-gen yq ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) crd:crdVersions=v1 rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_bootstrapkubeconfigs.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byoclusters.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byoclustertemplates.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byohosts.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byomachines.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byomachinetemplates.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_k8sinstallerconfigs.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_k8sinstallerconfigtemplates.yaml


generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	go vet ./...

GOLANGCI_LINT_VER := v2.12.2
GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint-$(GOLANGCI_LINT_VER)
lint: golangci-lint ## Run golangci-lint.
	$(GOLANGCI_LINT) config verify
	$(GOLANGCI_LINT) run
golangci-lint: ## Download golangci-lint locally if necessary.
	$(call go-get-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VER))

update-containerd-service: ## Update the systemd unit of containerd in the installer to the one of its default version.
	hack/update-containerd-service.sh

# CI runs this target, so that the default version of containerd does not
# change without its systemd unit.
verify-containerd-service: update-containerd-service ## Verify that the systemd unit of containerd in the installer is the one of its default version.
	git diff --exit-code -- installer/internal/algo/containerd.service

##@ Build

build: generate fmt vet ## Build manager binary.
	go build -o bin/manager main.go

run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

docker-build: ## Build docker image with the manager for the platform of this machine.
	docker build --build-arg GO_VERSION=$(GO_VERSION) -t ${IMG} .

docker-push: ## Push docker image with the manager.
	bash hack/release/check-push-target.sh "${IMG}" byoh-controller
	docker push ${IMG}

empty :=
space := $(empty) $(empty)
comma := ,
# The platforms of the multi-platform image, linux/<arch> for each of ALL_ARCH.
IMAGE_PLATFORMS ?= $(subst $(space),$(comma),$(addprefix linux/,$(ALL_ARCH)))
# Set to true to push the multi-platform image to the registry of IMG.
DOCKER_BUILDX_PUSH ?= false

docker-buildx: ## Build docker image with the manager for all the platforms in ALL_ARCH, and push it if DOCKER_BUILDX_PUSH is true.
	$(if $(filter true,$(DOCKER_BUILDX_PUSH)),bash hack/release/check-push-target.sh "${IMG}" byoh-controller)
	docker buildx build $(if $(BUILDX_METADATA_FILE),--metadata-file "$(BUILDX_METADATA_FILE)") --platform $(IMAGE_PLATFORMS) --build-arg GO_VERSION=$(GO_VERSION) -t ${IMG} $(if $(filter true,$(DOCKER_BUILDX_PUSH)),--push) .

docker-buildx-guard: ## Build the No-Replacement guard image (IMG_GUARD) for IMAGE_PLATFORMS, and push it if DOCKER_BUILDX_PUSH is true.
	$(if $(filter true,$(DOCKER_BUILDX_PUSH)),bash hack/release/check-push-target.sh "${IMG_GUARD}" no-replacement-guard)
	docker buildx build $(if $(BUILDX_METADATA_FILE),--metadata-file "$(BUILDX_METADATA_FILE)") --platform $(IMAGE_PLATFORMS) --build-arg GO_VERSION=$(GO_VERSION) -f cmd/no-replacement-guard/Dockerfile -t ${IMG_GUARD} $(if $(filter true,$(DOCKER_BUILDX_PUSH)),--push) .

docker-buildx-extension: ## Build the in-place extension image (IMG_EXTENSION) for IMAGE_PLATFORMS, and push it if DOCKER_BUILDX_PUSH is true.
	$(if $(filter true,$(DOCKER_BUILDX_PUSH)),bash hack/release/check-push-target.sh "${IMG_EXTENSION}" inplace-extension)
	docker buildx build $(if $(BUILDX_METADATA_FILE),--metadata-file "$(BUILDX_METADATA_FILE)") --platform $(IMAGE_PLATFORMS) --build-arg GO_VERSION=$(GO_VERSION) -f cmd/inplace-extension/Dockerfile -t ${IMG_EXTENSION} $(if $(filter true,$(DOCKER_BUILDX_PUSH)),--push) .

prepare-byoh-docker-host-image:
	docker build test/e2e -f test/e2e/BYOHDockerFile -t ${BYOH_BASE_IMG}

cluster-templates-v1beta1: kustomize ## Generate cluster templates for v1beta1
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/vm --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template.yaml
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/docker --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template.yaml

##@ Test

# Run tests
test: $(GINKGO) generate fmt vet manifests test-coverage ## Run unit tests

# Only the end-to-end suite is skipped here (it runs in its own workflow, e2e.yml). The pattern is a substring of the
# package path: "test" alone also skipped drainertest, admissiontest and hardeningtest.
test-coverage: prepare-byoh-docker-host-image $(GINKGO) $(SETUP_ENVTEST) ## Run test-coverage
	$(ENVTEST_ASSETS) $(GINKGO) --randomize-all -r --cover --coverprofile=cover.out --output-dir=. --skip-package=test/e2e .

agent-test: prepare-byoh-docker-host-image $(GINKGO) $(SETUP_ENVTEST) ## Run agent tests
	$(ENVTEST_ASSETS) $(GINKGO) --randomize-all -r --coverprofile cover.out $(HOST_AGENT_DIR)

controller-test: $(GINKGO) $(SETUP_ENVTEST) ## Run controller tests
	$(ENVTEST_ASSETS) $(GINKGO) --randomize-all --coverprofile cover.out --vv controllers/infrastructure

webhook-test: $(GINKGO) $(SETUP_ENVTEST) ## Run webhook tests
	$(ENVTEST_ASSETS) $(GINKGO) --coverprofile cover.out apis/infrastructure/v1beta1

_SKIP_ARGS := $(if $(GINKGO_SKIP),-skip="$(GINKGO_SKIP)")

# The test framework of Cluster API runs kustomize for the provider from
# config/default.
test-e2e: docker-build prepare-byoh-docker-host-image $(GINKGO) cluster-templates-e2e ## Run the end-to-end tests
	CAPI_KUSTOMIZE_PATH=$(KUSTOMIZE) $(GINKGO) -v -trace -tags=e2e -focus="$(GINKGO_FOCUS)" $(_SKIP_ARGS) -nodes=$(GINKGO_NODES) --no-color=$(GINKGO_NOCOLOR) $(GINKGO_ARGS) test/e2e -- \
	    -e2e.artifacts-folder="$(ARTIFACTS)" \
	    -e2e.config="$(E2E_CONF_FILE)" \
	    -e2e.skip-resource-cleanup=$(SKIP_RESOURCE_CLEANUP) -e2e.use-existing-cluster=$(USE_EXISTING_CLUSTER) \
		-e2e.existing-cluster-kubeconfig-path=$(EXISTING_CLUSTER_KUBECONFIG_PATH)

# The pull requests run only this spec, which is the one to run locally first.
test-e2e-pr-blocking: GINKGO_FOCUS := \[PR-Blocking\]
test-e2e-pr-blocking: test-e2e ## Run the [PR-Blocking] end-to-end test, which the pull requests run

cluster-templates: kustomize cluster-templates-v1beta1

cluster-templates-e2e: kustomize
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/e2e --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/e2e/cluster-template.yaml

$(GINKGO): # Build ginkgo from tools folder.
	cd $(TOOLS_DIR); GOBIN=$(TOOLS_BIN_DIR) go install $(GINKGO_PKG)

$(SETUP_ENVTEST): # Build setup-envtest, which downloads the envtest binaries.
	GOBIN=$(TOOLS_BIN_DIR) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VER)

##@ Deployment

install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image ghcr.io/openkubes/byoh-controller=${IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete -f -

publish-infra-yaml:kustomize # Generate infrastructure-components.yaml for the provider
	cd config/manager && $(KUSTOMIZE) edit set image ghcr.io/openkubes/byoh-controller=${IMG}
	$(KUSTOMIZE) build config/default > infrastructure-components.yaml

CONTROLLER_GEN_VER := v0.21.0
CONTROLLER_GEN = $(shell pwd)/bin/controller-gen-$(CONTROLLER_GEN_VER)
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VER))

KUSTOMIZE_VER := v5.7.0
KUSTOMIZE = $(shell pwd)/bin/kustomize-$(KUSTOMIZE_VER)
kustomize: ## Download kustomize locally if necessary.
	$(call go-get-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VER))

YQ_VER := v4.45.4
YQ = $(shell pwd)/bin/yq-$(YQ_VER)
yq: ## Download yq locally if necessary.
	$(call go-get-tool,$(YQ),github.com/mikefarah/yq/v4@$(YQ_VER))

NFPM_VER := v2.48.0
# Not in bin, which the container that builds the host agent creates as root.
NFPM = $(TOOLS_BIN_DIR)/nfpm-$(NFPM_VER)
nfpm: ## Download nfpm locally if necessary.
	$(call go-get-tool,$(NFPM),github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VER))

host-agent-binaries: $(addprefix host-agent-binary-,$(HOST_AGENT_ARCHS)) ## Builds the binaries for the host-agent

host-agent-binary-%:
	RELEASE_BINARY=./byoh-hostagent GOOS=linux GOARCH=$* GOLDFLAGS="$(LDFLAGS) $(STATIC)" \
	HOST_AGENT_DIR=./$(HOST_AGENT_DIR) $(MAKE) host-agent-binary

host-agent-binary: $(RELEASE_DIR)
	docker run \
		--rm \
		-e CGO_ENABLED=0 \
		-e GOOS=$(GOOS) \
		-e GOARCH=$(GOARCH) \
		-v "$$(pwd):/workspace$(DOCKER_VOL_OPTS)" \
		-w /workspace \
		golang:$(GO_VERSION) \
		go build -buildvcs=false -a -ldflags "$(GOLDFLAGS)" \
		-o ./bin/$(notdir $(RELEASE_BINARY))-$(GOOS)-$(GOARCH) $(HOST_AGENT_DIR)

# The version of the deb packages of the host agent in semver, without the
# leading v: the tag on HEAD, the last tag followed by the number of commits
# since it and the commit, or, when there is no tag, 0.0.0-g followed by the
# commit, with .dirty when the tree has changes, for the agent that reports
# v0.0.0-<commit>. The commit has the prefix g, as a pre-release identifier of
# only digits must not start with 0. It names the files as it is, and nfpm turns
# a pre-release such as 0.6.0-rc.0 into the version 0.6.0~rc.0 in the package,
# which dpkg orders before 0.6.0.
DEB_COMMIT := $(shell git rev-parse --short=14 HEAD 2>/dev/null)
DEB_DEV_VERSION = 0.0.0-$(if $(DEB_COMMIT),g$(DEB_COMMIT)$(if $(shell git status --porcelain 2>/dev/null),.dirty),unknown)
DEB_VERSION ?= $(or $(shell git describe --tags --match 'v[0-9]*' 2>/dev/null | sed -E 's/^v//; s/-([0-9]+)-g([0-9a-f]+)$$/+\1.g\2/'),$(DEB_DEV_VERSION))

host-agent-debs: $(addprefix host-agent-deb-,$(HOST_AGENT_ARCHS)) ## Build the deb packages of the host agent

host-agent-deb-%: host-agent-binary-% nfpm $(RELEASE_DIR)
	GOARCH=$* DEB_VERSION=$(DEB_VERSION) $(NFPM) package \
		--config packaging/byoh-hostagent/nfpm.yaml \
		--packager deb \
		--target $(RELEASE_DIR)/byoh-hostagent_$(DEB_VERSION)_$*.deb

##@ Release

$(RELEASE_DIR):
	mkdir -p $(RELEASE_DIR)

build-release-artifacts: build-cluster-templates build-infra-yaml build-metadata-yaml build-host-agent-binary host-agent-debs ## Builds release artifacts

build-cluster-templates: $(RELEASE_DIR) cluster-templates
	cp $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template.yaml $(RELEASE_DIR)/cluster-template-docker.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template-topology-docker.yaml $(RELEASE_DIR)/cluster-template-topology-docker.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/docker/clusterclass-quickstart-docker.yaml $(RELEASE_DIR)/clusterclass-quickstart-docker.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template.yaml $(RELEASE_DIR)/cluster-template.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template-topology.yaml $(RELEASE_DIR)/cluster-template-topology.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/vm/clusterclass-quickstart.yaml $(RELEASE_DIR)/clusterclass-quickstart.yaml


build-infra-yaml:kustomize ## Generate infrastructure-components.yaml for the provider
	cd config/manager && $(KUSTOMIZE) edit set image ghcr.io/openkubes/byoh-controller=${IMG}
	$(KUSTOMIZE) build config/default > $(RELEASE_DIR)/infrastructure-components.yaml

build-metadata-yaml:
	cp metadata.yaml $(RELEASE_DIR)/metadata.yaml

build-host-agent-binary: host-agent-binaries
	cp $(addprefix bin/byoh-hostagent-linux-,$(HOST_AGENT_ARCHS)) $(RELEASE_DIR)/


# go-get-tool installs the package $2 (with @version) as the binary $1. The
# binary names carry the version, so that changing a version installs it again.
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
echo "Downloading $(2)" ;\
TMP_DIR=$$(mktemp -d) ;\
GOBIN=$$TMP_DIR go install $(2) ;\
mkdir -p $(dir $(1)) ;\
mv $$TMP_DIR/* $(1) ;\
rm -rf $$TMP_DIR ;\
}
endef

##@ Local BYOH management bootstrap

# Single entry point for the existing, guarded Kind -> CAPI -> BYOH workflow.
# BYOH_KIND_NAME must be an allow-listed disposable local Kind environment.
BYOH_ENROLL ?= false

.PHONY: ok-up
ok-up: byoh-kind-up ## Bootstrap local BYOH; with BYOH_ENROLL=true stage host enrollment.
	@case '$(BYOH_ENROLL)' in \
	  false|'') ;; \
	  true) $(MAKE) --no-print-directory BYOH_KIND_NAME='$(BYOH_KIND_NAME)' BYOH_HOSTS_INVENTORY='$(BYOH_HOSTS_INVENTORY)' byoh-hosts-enroll ;; \
	  *) echo 'STOP: BYOH_ENROLL must be true or false' >&2; exit 1 ;; \
	esac

BYOH_KIND_NAME ?= ok-byoh-dev

# Replays only the manually verified Kind -> CAPI steps. Existing Kind clusters
# are preserved. Unlike upstream E2E, no BYOH provider or hosts are installed.
byoh-preflight: ## Read-only local Docker/Kind/CAPI prerequisite checks.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-kind.sh preflight

byoh-kind-create: ## Create isolated Kind (set BYOH_KIND_NAME=ok-byoh-make-test for testing).
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-kind.sh create

byoh-kind-status: ## Read-only status for the isolated Kind cluster.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-kind.sh status

byoh-capi-init: ## Install CAPI v1.14.4 to a fresh verified BYOH Kind cluster.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-kind.sh capi-init

byoh-capi-verify: ## Read-only rollout and provider version verification.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-kind.sh capi-verify

##@ BYOH host onboarding (inventory-driven)

.PHONY: byoh-python-check
byoh-python-check: ## Prepare and verify isolated Python dependencies for BYOH tooling.
	@bash hack/dev/python-toolchain.sh --check


# Local host inventory (ignored by Git), created from
# config/hosts/my-provider-hosts-inventar.tpl for your hosts and provider.
BYOH_HOSTS_INVENTORY ?= config/hosts/hosts.yaml

.PHONY: byoh-hosts-preflight
byoh-hosts-preflight: ## Read-only inventory, SSH, agent and management-tunnel identity checks.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-preflight.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-hosts-enroll
byoh-hosts-enroll: ## One approved N-host plan: bootstrap credentials and stopped agent-unit preparation.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-enroll.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-upgrade-plan-test
byoh-cluster-upgrade-plan-test: ## Offline safety regressions for read-only BYOH upgrade preflight.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-upgrade-plan.py

.PHONY: byoh-cluster-upgrade-plan
byoh-cluster-upgrade-plan: ## Read-only Kubernetes 1.36.6 to 1.37.2 preflight; upgrade remains blocked.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-upgrade-plan.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-worker-status
byoh-cluster-worker-status: ## Read-only post-install: both CAPI Machines/Nodes Ready and Cilium 2/2.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-worker-status.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-worker-install-plan
byoh-cluster-worker-install-plan: ## Read-only, UID/digest-bound worker install readiness and systemd preflight.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-worker-arm.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-worker-install
byoh-cluster-worker-install: ## Confirmed one-time worker agent activation for CAPI-managed kubeadm join.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-worker-arm.py --inventory '$(BYOH_HOSTS_INVENTORY)' --install

.PHONY: byoh-cluster-worker-verify
byoh-cluster-worker-verify: ## Read-only worker Machine/ByoHost UID binding and ready kubeadm/installer Secret verification.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-worker-verify.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-worker-apply
byoh-cluster-worker-apply: ## Confirmed four-object worker MD apply; preserves stopped agent.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-worker-apply.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-worker-plan
byoh-cluster-worker-plan: ## Read-only MD worker plan: CAPI template, stopped agent, and worker-to-CP certificate identity.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-worker-plan.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-cilium-plan
byoh-cluster-cilium-plan: ## Read-only pinned chart, fresh workload TLS identity and Helm target checks.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-cilium.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-cilium-install
byoh-cluster-cilium-install: ## Confirmed Cilium 1.19.6 installation on TLS-validated workload only.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-cilium.py --inventory '$(BYOH_HOSTS_INVENTORY)' --install

.PHONY: byoh-cluster-cilium-verify
byoh-cluster-cilium-verify: ## Read-only Cilium Helm values, daemon/operator pods, Node and CoreDNS readiness.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-cilium.py --inventory '$(BYOH_HOSTS_INVENTORY)' --verify

.PHONY: byoh-cluster-control-plane-status
byoh-cluster-control-plane-status: ## Read-only live CP API, CAPI binding, kubeadm components, and pre-CNI readiness status.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-control-plane-status.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-control-plane-install-plan
byoh-cluster-control-plane-install-plan: ## Read-only CP install readiness: exact binding, installer Secret and stopped unit.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-control-plane-install.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-control-plane-install
byoh-cluster-control-plane-install: ## Confirmed CP install: verified systemd override and one-time agent start.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-control-plane-install.py --inventory '$(BYOH_HOSTS_INVENTORY)' --install

.PHONY: byoh-cluster-control-plane-verify
byoh-cluster-control-plane-verify: ## Read-only CAPI CP binding, kubeadm data Secret, installer, and stopped agent checks.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-control-plane-verify.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-control-plane-apply
byoh-cluster-control-plane-apply: ## Confirmed lab CAPI CP manifest apply, no kubeadm/agent activation.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-control-plane-apply.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-network-plan
byoh-cluster-network-plan: ## Read-only workload API SSH tunnel and isolated kind NAT preflight.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-network.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-cluster-network-up
byoh-cluster-network-up: ## Confirmed SSH workload API forwarding plus isolated kind NAT (API must already be serving).
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-network.py --inventory '$(BYOH_HOSTS_INVENTORY)' --up

.PHONY: byoh-cluster-control-plane-plan
byoh-cluster-control-plane-plan: ## Read-only CAPI v1.36.6 control-plane manifest preflight and server dry-run (two-host E2E lab).
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-cluster-control-plane-plan.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-hosts-bootstrap-revoke-plan
byoh-hosts-bootstrap-revoke-plan: ## Read-only revocation plan after verifying permanent host registration.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-bootstrap-revoke.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-hosts-bootstrap-revoke
byoh-hosts-bootstrap-revoke: ## Explicitly revoke owned tokens and matching staged host files after registration.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-bootstrap-revoke.py --inventory '$(BYOH_HOSTS_INVENTORY)' --revoke

.PHONY: byoh-hosts-register-verify
byoh-hosts-register-verify: ## Read-only N-host check: issued CSR, ByoHost and permanent host certificate/key.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-register-verify.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-hosts-csr-approve-plan
byoh-hosts-csr-approve-plan: ## Read-only N-host CSR approval evidence including SSH private-key challenge.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-csr-approve.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-hosts-csr-approve
byoh-hosts-csr-approve: ## Explicit UID/RV-bound approval of verified host CSRs (one plan confirmation).
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-csr-approve.py --inventory '$(BYOH_HOSTS_INVENTORY)' --approve

.PHONY: byoh-hosts-csr-verify
byoh-hosts-csr-verify: ## Read-only N-host CSR subject, signer, bootstrap token-UID ownership and host checksum validation.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-csr-verify.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-hosts-register-plan
byoh-hosts-register-plan: ## Read-only all-host registration preflight and digest-bound start plan.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-register.py --inventory '$(BYOH_HOSTS_INVENTORY)' --plan-only

.PHONY: byoh-hosts-register
byoh-hosts-register: ## Confirmed N-host enrollment-only agent start; CSR inspection, never approval.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-register.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-hosts-bootstrap-renew
byoh-hosts-bootstrap-renew: ## Confirmed renewal of unconsumed bootstrap credentials on inventory hosts.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-bootstrap-renew.py --inventory '$(BYOH_HOSTS_INVENTORY)'

.PHONY: byoh-hosts-idempotency-test
byoh-hosts-idempotency-test: ## Offline regression tests for BYOH token replay and systemd idempotency.
	@PYTHONDONTWRITEBYTECODE=1 python3 -B hack/dev/test-byoh-hosts-idempotency.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-hosts-csr-verify.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-hosts-csr-approve.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-hosts-register-verify.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-control-plane-plan.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-control-plane-lifecycle.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-control-plane-verify.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-control-plane-install.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-control-plane-status.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-cilium.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-worker-plan.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-worker-apply.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-worker-verify.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-worker-arm.py
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/test-byoh-cluster-worker-status.py

.PHONY: byoh-hosts-agent-prepare
byoh-hosts-agent-prepare: ## Safely prepare existing stopped agent units for enrollment only.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/python-toolchain.sh hack/dev/byoh-hosts-agent-prepare.py --inventory '$(BYOH_HOSTS_INVENTORY)'

##@ Safe local BYOH controller lifecycle

# These targets only target allow-listed kind clusters through their dedicated
# ~/.config/ok/<name>/kubeconfig; never use the ambient kubectl context.
byoh-controller-build: ## Build the controller locally, tagged with Git revision.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-controller.sh build

byoh-controller-load: ## Load the local revision-tagged controller image into kind.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-controller.sh load

byoh-controller-render: kustomize ## Render a fail-closed local controller manifest (no cluster writes).
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-controller.sh render /tmp/ok-byoh-kind-rendered.yaml

byoh-controller-deploy: kustomize ## Install BYOH only after target/CAPI/image/manifest checks.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-controller.sh deploy

byoh-controller-verify: ## Verify controller image, manual CSR mode, RBAC, rollout.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-controller.sh verify

byoh-controller-test: ## Run offline manifest and bootstrap security tests.
	@PYTHONDONTWRITEBYTECODE=1 python3 hack/dev/test-byoh-controller-manifest.py
	@PYTHONDONTWRITEBYTECODE=1 python3 hack/dev/test-byoh-bootstrap-revocation-unit.py

byoh-kind-up: kustomize ## Preserve existing kind/CAPI and safely bring up BYOH management plane.
	@BYOH_KIND_NAME='$(BYOH_KIND_NAME)' bash hack/dev/byoh-controller.sh up


##@ Local CAPD developer environment

# Deliberately separate from ok-up / BYOH_KIND_NAME. This workflow owns only
# the disposable ok-capd-dev Kind management cluster and its private kubeconfig.
.PHONY: ok-capd-setup ok-capd ok-capd-status ok-capd-test ok-capd-inplace-test ok-capd-guard-test ok-capd-report ok-capd-clean

ok-capd-setup: ## Create or verify the CAPD management cluster and one workload cluster.
	@bash dev/ok-capd/scripts/setup.sh

ok-capd: ## Create or verify the isolated local CAPI + CAPD management cluster.
	@bash dev/ok-capd/scripts/up.sh


ok-capd-status: ## Read-only status of the isolated CAPD management cluster.
	@bash dev/ok-capd/scripts/status.sh

ok-capd-test: ## Verify CAPI/CAPD readiness and Docker socket access (bootstrap smoke test).
	@bash dev/ok-capd/scripts/test.sh

ok-capd-inplace-test: ## Run the CAPD-backed RuntimeSDK in-place upgrade scenarios (not yet implemented).
	@bash dev/ok-capd/scripts/not-yet-implemented.sh inplace-upgrade

ok-capd-guard-test: ## Run the No-Replacement Guard scenarios (not yet implemented).
	@bash dev/ok-capd/scripts/not-yet-implemented.sh no-replacement

ok-capd-report: ## Read-only report of CAPD workload clusters; does not stop or delete anything.
	@bash dev/ok-capd/scripts/report.sh

ok-capd-clean: ## Delete only the isolated CAPD Kind cluster after explicit confirmation.
	@bash dev/ok-capd/scripts/clean.sh


##@ Local CAPD workload cluster

.PHONY: ok-capd-workload-render ok-capd-workload-up ok-capd-workload-status ok-capd-workload-diagnose ok-capd-workload-down
ok-capd-workload-render: ## Render the pinned upstream CAPD workload template locally (no cluster writes).
	@bash dev/ok-capd/scripts/workload-render.sh

ok-capd-workload-up: ## Create or verify the disposable CAPD smoke workload cluster.
	@bash dev/ok-capd/scripts/workload-up.sh

ok-capd-workload-status: ## Read-only status of the CAPD smoke workload cluster.
	@bash dev/ok-capd/scripts/workload-status.sh

ok-capd-workload-diagnose: ## Collect read-only CAPD workload diagnostics and save a private report.
	@bash dev/ok-capd/scripts/workload-diagnose.sh

ok-capd-workload-down: ## Delete only the CAPD smoke workload cluster after explicit confirmation.
	@bash dev/ok-capd/scripts/workload-down.sh
