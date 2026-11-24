# EduIDE control plane. Every target that talks to a cluster uses the isolated
# kind kubeconfig in .e2e/, never the default context of the machine.

SHELL := /bin/bash
BINARIES := operator service authz agent usage-export
REGISTRY ?= ghcr.io/eduide/eduide-control-plane
TAG ?= dev
ENVTEST_K8S ?= 1.37.0
E2E_CLUSTER ?= eduide-e2e
KUBECONFIG_E2E := $(CURDIR)/.e2e/kubeconfig
GENERATED := config api internal/httpapi internal/agentapi clients/typescript/src charts/eduide-control-plane-crds charts/eduide-control-plane/files/operator-rules.yaml

.PHONY: all generate verify-generate test test-go test-pool-scaler test-ts lint build images e2e e2e-down helm-lint fmt

all: generate lint test build

## generate: CRD YAML, deepcopy, RBAC, REST server stubs, agent types, TypeScript client. Never edit the output.
generate:
	go tool controller-gen object paths=./api/...
	go tool controller-gen crd paths=./api/... output:crd:artifacts:config=config/crd
	go tool controller-gen rbac:roleName=eduide-operator paths=./internal/controller/... output:rbac:artifacts:config=config/rbac
	if [ -d charts/eduide-control-plane-crds/templates ]; then cp config/crd/*.yaml charts/eduide-control-plane-crds/templates/; fi
	cp config/rbac/role.yaml charts/eduide-control-plane/files/operator-rules.yaml
	go tool oapi-codegen -config api/openapi/oapi-codegen.yaml api/openapi/v1.yaml
	go tool oapi-codegen -config api/agent/oapi-codegen.yaml api/agent/v1.yaml
	cd clients/typescript && npm ci --silent && npm run --silent generate

## verify-generate: fails when generated files are not up to date.
verify-generate: generate
	@git diff --exit-code -- $(GENERATED) || (echo "generated files are stale: run make generate and commit" && exit 1)
	@test -z "$$(git status --porcelain -- $(GENERATED))" || (echo "untracked generated files: run make generate and commit" && git status --porcelain && exit 1)

## test: unit and envtest tests of all Go modules and the TypeScript packages.
test: test-go test-pool-scaler test-ts

test-go:
	KUBEBUILDER_ASSETS="$$(go tool setup-envtest use $(ENVTEST_K8S) -p path --bin-dir $(CURDIR)/bin/envtest)" go test -race -count=1 ./...

test-pool-scaler:
	cd pool-scaler && go test -race -count=1 ./...

test-ts:
	cd clients/typescript && npm ci --silent && npm run --silent build && npm test --silent
	if [ -f extensions/eduide-session/package.json ]; then cd extensions/eduide-session && npm ci --silent && npm test --silent; fi

lint:
	golangci-lint run ./...
	cd pool-scaler && golangci-lint run ./...

fmt:
	gofmt -w $$(git ls-files '*.go' | grep -v '.gen.go')

build:
	mkdir -p bin
	for b in $(BINARIES); do CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$$b ./cmd/$$b || exit 1; done

## images: builds all images with the tag $(TAG).
images:
	for b in $(BINARIES); do docker build --build-arg CMD=$$b -t $(REGISTRY)/$$b:$(TAG) -f build/Dockerfile . || exit 1; done
	docker build -t $(REGISTRY)/pool-scaler:$(TAG) pool-scaler
	docker build -t $(REGISTRY)/landing-page:$(TAG) landing-page

helm-lint:
	charts/test.sh

## e2e: kind cluster with Envoy Gateway, Calico and Keycloak, full matrix (see test/e2e/README.md).
e2e:
	KUBECONFIG=$(KUBECONFIG_E2E) E2E_CLUSTER=$(E2E_CLUSTER) TAG=$(TAG) ./test/e2e/run.sh

e2e-down:
	kind delete cluster --name $(E2E_CLUSTER) --kubeconfig $(KUBECONFIG_E2E)
