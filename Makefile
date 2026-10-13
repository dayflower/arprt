GO ?= go
export GOTOOLCHAIN = go1.26.8
CONTROLLER_GEN_VERSION := v0.21.0
ENVTEST_VERSION := v0.24.1
ENVTEST_K8S_VERSION ?= 1.36.0
ENVTEST_BIN := $(CURDIR)/bin/k8s
IMAGE ?= arprt:dev
VERSION ?= dev
REVISION ?= $(shell git rev-parse HEAD)
KIND_VERSION := v0.33.0
KIND_NODE_IMAGE := kindest/node@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed
HELM_VERSION := v4.3.0
HELM := $(CURDIR)/bin/helm-$(HELM_VERSION)

.PHONY: generate manifests tools build test vet fmt verify integration docker-build cluster-smoke release-check disposable-cluster-smoke crd-chart-check

tools: bin/controller-gen

bin/controller-gen:
	GOBIN=$(CURDIR)/bin $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

bin/setup-envtest:
	GOBIN=$(CURDIR)/bin $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

generate: bin/controller-gen
	bin/controller-gen object paths=./api/...

manifests: generate
	bin/controller-gen crd:allowDangerousTypes=true paths=./api/... output:crd:artifacts:config=config/crd/bases
	$(GO) run ./hack/crd-chart

$(HELM): hack/install-helm.py
	python3 hack/install-helm.py $(HELM_VERSION) $@

# Read-only: catches embedded drift before regeneration can repair it.
crd-chart-check: $(HELM)
	$(HELM) lint --strict charts/arprt-crds --kube-version 1.36.0
	$(GO) run ./hack/crd-chart --check --helm $(HELM)

fmt:
	gofmt -w api cmd internal hack/crd-chart

vet:
	$(GO) vet ./...

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/arprt ./cmd/controller

test:
	$(GO) test -race ./...

verify: manifests release-check
	git diff --exit-code -- api/v1alpha1/zz_generated.deepcopy.go config/crd/bases charts/arprt-crds/files/crds
	$(MAKE) crd-chart-check
	$(GO) mod tidy -diff
	@test -z "$$(gofmt -l api cmd internal hack/crd-chart)"
	$(MAKE) vet test build

integration: manifests bin/setup-envtest $(HELM)
	ARPRT_HELM="$(HELM)" KUBEBUILDER_ASSETS="$$($(CURDIR)/bin/setup-envtest use $(ENVTEST_K8S_VERSION) --bin-dir $(ENVTEST_BIN) -p path)" $(GO) test -tags=integration -count=1 -v ./internal/integration

docker-build:
	docker build --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) -t $(IMAGE) .

# Run against an explicitly selected disposable cluster and loaded/pushed IMAGE.
cluster-smoke:
	python3 hack/cluster-smoke.py --image $(IMAGE)
	python3 hack/job-smoke.py --image $(IMAGE)
	python3 hack/playlist-smoke.py --image $(IMAGE)
	python3 hack/operations-smoke.py --image $(IMAGE)

# Release helpers never use the operator's selected kubeconfig context.
bin/kind:
	GOBIN=$(CURDIR)/bin $(GO) install sigs.k8s.io/kind@$(KIND_VERSION)

disposable-cluster-smoke: bin/kind
	PATH="$(CURDIR)/bin:$$PATH" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" bash hack/disposable-cluster-smoke.sh "$(IMAGE)" $(if $(filter true,$(LOAD_IMAGE)),--load,)

release-check:
	python3 -m unittest discover -s hack -p 'test_*.py'
