GO ?= go
export GOTOOLCHAIN = go1.26.8
CONTROLLER_GEN_VERSION := v0.21.0
ENVTEST_VERSION := v0.24.1
ENVTEST_K8S_VERSION ?= 1.36.2
ENVTEST_BIN := $(CURDIR)/bin/k8s
IMAGE ?= arprt:dev

.PHONY: generate manifests tools build test vet fmt verify integration docker-build

tools: bin/controller-gen

bin/controller-gen:
	GOBIN=$(CURDIR)/bin $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

bin/setup-envtest:
	GOBIN=$(CURDIR)/bin $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

generate: bin/controller-gen
	bin/controller-gen object paths=./api/...

manifests: generate
	bin/controller-gen crd:allowDangerousTypes=true paths=./api/... output:crd:artifacts:config=config/crd/bases

fmt:
	gofmt -w api cmd internal

vet:
	$(GO) vet ./...

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/arprt ./cmd/controller

test:
	$(GO) test -race ./...

verify: manifests
	git diff --exit-code -- api/v1alpha1/zz_generated.deepcopy.go config/crd/bases
	$(GO) mod tidy -diff
	@test -z "$$(gofmt -l api cmd internal)"
	$(MAKE) vet test build

integration: manifests bin/setup-envtest
	KUBEBUILDER_ASSETS="$$($(CURDIR)/bin/setup-envtest use $(ENVTEST_K8S_VERSION) --bin-dir $(ENVTEST_BIN) -p path)" $(GO) test -tags=integration -count=1 -v ./internal/integration

docker-build:
	docker build -t $(IMAGE) .
