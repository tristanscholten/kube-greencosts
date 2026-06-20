SHELL := /usr/bin/env bash

VERSION_FILE ?= VERSION
VERSION := $(shell tr -d '[:space:]' < $(VERSION_FILE))
IMAGE_REPOSITORY ?= docker.io/tristanscholten/kube-greencosts-controller
IMG ?= $(IMAGE_REPOSITORY):v$(VERSION)
IMAGE_TAGS ?= $(IMG) $(IMAGE_REPOSITORY):latest
CONTAINER_TOOL ?= podman
KUSTOMIZE ?= kubectl kustomize
KUBECTL ?= kubectl
ENVTEST_K8S_VERSION ?= 1.33.0
SETUP_ENVTEST ?= $(HOME)/go/bin/setup-envtest

.PHONY: all version bump-major bump-minor bump-patch test test-unit test-e2e setup-envtest docker-build docker-push install uninstall deploy undeploy manifests generate fmt vet

all: docker-build

version:
	@printf '%s\n' "$(VERSION)"

bump-major:
	bash hack/bump-version.sh major $(VERSION_FILE)

bump-minor:
	bash hack/bump-version.sh minor $(VERSION_FILE)

bump-patch:
	bash hack/bump-version.sh patch $(VERSION_FILE)

test: test-unit

test-unit:
	go test ./api/... ./cmd/... ./internal/... ./test/utils

test-e2e:
	go test ./test/e2e

setup-envtest:
	go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
	$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir ./bin/k8s -p path

docker-build:
	$(CONTAINER_TOOL) build $(foreach tag,$(IMAGE_TAGS),-t $(tag)) .

docker-push:
	$(foreach tag,$(IMAGE_TAGS),$(CONTAINER_TOOL) push $(tag);)

install:
	$(KUBECTL) apply --server-side --force-conflicts -f config/crd/bases

uninstall:
	$(KUBECTL) delete -f config/crd/bases --ignore-not-found

deploy: install
	tmp_overlay=$$(mktemp -d config/.deploy-overlay.XXXXXX); \
	trap 'rm -rf "$$tmp_overlay"' EXIT; \
	printf '%s\n' \
		'resources:' \
		'  - ../default' \
		'patches:' \
		'  - target:' \
		'      kind: Deployment' \
		'      name: kube-greencosts-controller-manager' \
		'    patch: |-' \
		'      - op: replace' \
		'        path: /spec/template/spec/containers/0/image' \
		'        value: $(IMG)' \
		> "$$tmp_overlay/kustomization.yaml"; \
	$(KUBECTL) apply --server-side --force-conflicts -k "$$tmp_overlay"

undeploy:
	$(KUBECTL) delete -k config/default --ignore-not-found

manifests:
	@echo "CRD and RBAC manifests are committed under config/."

generate:
	@echo "Generated Go files are committed under api/."

fmt:
	go fmt ./...

vet:
	go vet ./...
