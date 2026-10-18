GO ?= go
UV ?= uv
export GOTOOLCHAIN = go1.26.8
STATICCHECK_VERSION := v0.8.2
RUFF_VERSION := 0.17.1
STATICCHECK := $(CURDIR)/bin/staticcheck-$(STATICCHECK_VERSION)/staticcheck
RUFF := $(UV) tool run --from 'ruff==$(RUFF_VERSION)' ruff
GO_SOURCE_DIRS := api cmd internal tools
CONTROLLER_GEN_VERSION := v0.21.0
ENVTEST_VERSION := v0.24.1
ENVTEST_K8S_VERSION ?= 1.36.0
ENVTEST_BIN := $(CURDIR)/bin/k8s
IMAGE ?= arprt:dev
VERSION ?= dev
REVISION ?= $(shell git rev-parse HEAD)
GO_BUILD_TAGS ?=
KIND_VERSION := v0.33.0
KIND_NODE_IMAGE := kindest/node@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed
HELM_VERSION ?= v4.3.0
CHART_OUTPUT ?= $(CURDIR)/dist/charts
HELM := $(CURDIR)/bin/helm-$(HELM_VERSION)

.PHONY: generate manifests tools build test vet fmt lint lint-go lint-python format format-go format-python format-check format-check-go format-check-python verify integration docker-build cluster-smoke release-check disposable-cluster-smoke crd-chart-check chart-dependencies chart-lint chart-package chart-check helm-smoke helm-release-smoke helm-published-smoke image-benchmark release-chart-metadata release-chart-prepare release-chart-login release-chart-publish release-chart-verify

tools: bin/controller-gen

bin/controller-gen:
	GOBIN=$(CURDIR)/bin $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

bin/setup-envtest:
	GOBIN=$(CURDIR)/bin $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

$(STATICCHECK):
	GOBIN=$(dir $(STATICCHECK)) $(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

generate: bin/controller-gen
	bin/controller-gen object paths=./api/...

manifests: generate
	bin/controller-gen crd:allowDangerousTypes=true paths=./api/... output:crd:artifacts:config=config/crd/bases
	$(GO) run ./tools/crd-chart

$(HELM): tools/install-helm.py
	python3 tools/install-helm.py $(HELM_VERSION) $@

# Read-only: catches embedded drift before regeneration can repair it.
crd-chart-check: $(HELM)
	$(HELM) lint --strict charts/arprt-crds --kube-version 1.36.0
	$(GO) run ./tools/crd-chart --check --helm $(HELM)

chart-dependencies: $(HELM)
	rm -rf charts/arprt/charts
	$(HELM) dependency build charts/arprt --skip-refresh
	git diff --exit-code -- charts/arprt/Chart.lock

chart-lint: chart-dependencies
	$(HELM) lint --strict charts/arprt-crds --kube-version 1.36.0
	$(HELM) lint --strict charts/arprt --kube-version 1.36.0 --set byos.publicBaseURL=https://byos.example.test/arprt

chart-package: chart-dependencies
	mkdir -p "$(CHART_OUTPUT)"
	$(HELM) package charts/arprt-crds --destination "$(CHART_OUTPUT)"
	$(HELM) package charts/arprt --destination "$(CHART_OUTPUT)"

chart-check: crd-chart-check chart-lint chart-package
	$(GO) run ./tools/chart-check --helm "$(HELM)" --packages "$(CHART_OUTPUT)"

# Both entrypoints own their kind cluster and kubeconfig, including failure cleanup.
helm-smoke: chart-package
	$(MAKE) helm-release-smoke

# Test already assembled release archives without rebuilding them.
helm-release-smoke: bin/kind $(HELM)
	PATH="$(CURDIR)/bin:$$PATH" python3 tools/helm-smoke.py --helm "$(HELM)" --image "$(IMAGE)" --node-image "$(KIND_NODE_IMAGE)" --packages "$(CHART_OUTPUT)" $(if $(filter true,$(LOAD_IMAGE)),--load-image,) $(if $(filter true,$(USE_CHART_IMAGE)),--use-chart-image,)

# Available once the OCI charts are published; never falls back to local sources.
helm-published-smoke: bin/kind $(HELM)
	PATH="$(CURDIR)/bin:$$PATH" python3 tools/helm-smoke.py --helm "$(HELM)" --image "$(IMAGE)" --node-image "$(KIND_NODE_IMAGE)" --oci-repository "$(OCI_CHART_REPOSITORY)" --chart-version "$(CHART_VERSION)" --crd-chart-version "$(CRD_CHART_VERSION)" $(if $(DISTRIBUTION_RECORD),--distribution-record "$(DISTRIBUTION_RECORD)",)

# Release jobs use the same pinned Helm client as local checks.
release-chart-metadata: $(HELM)
	python3 tools/release-charts.py metadata --helm "$(HELM)" --version "$(VERSION)"

release-chart-prepare: $(HELM)
	python3 tools/release-charts.py prepare --helm "$(HELM)" --version "$(VERSION)" --repository "$(REPOSITORY)" --digest "$(INDEX_DIGEST)" --packages "$(CHART_OUTPUT)"

release-chart-login: $(HELM)
	@printf '%s' "$$GH_TOKEN" | "$(HELM)" registry login ghcr.io --username "$$GITHUB_ACTOR" --password-stdin

release-chart-publish: $(HELM)
	python3 tools/release-charts.py publish --helm "$(HELM)" --version "$(VERSION)" --repository "$(REPOSITORY)" --packages "$(CHART_OUTPUT)"

release-chart-verify: $(HELM)
	python3 tools/release-charts.py verify --helm "$(HELM)" --version "$(VERSION)" --repository "$(REPOSITORY)" --packages "$(CHART_OUTPUT)" --image-evidence "$(IMAGE_EVIDENCE)" --output "$(DISTRIBUTION_RECORD)"

fmt: format-go

format: format-go format-python

format-go:
	gofmt -w $(GO_SOURCE_DIRS)

format-python:
	$(RUFF) check --select I --fix .
	$(RUFF) format .

format-check: format-check-go format-check-python

format-check-go:
	@files="$$(gofmt -l $(GO_SOURCE_DIRS))" && \
	if [ -n "$$files" ]; then printf 'Run make format-go to format:\n%s\n' "$$files"; exit 1; fi

format-check-python:
	$(RUFF) format --check .

lint: lint-go lint-python

lint-go: vet $(STATICCHECK)
	$(STATICCHECK) ./...
	$(STATICCHECK) -tags=integration,gray4_native4 ./...

lint-python:
	$(RUFF) check .

vet:
	$(GO) vet ./...
	$(GO) vet -tags=integration,gray4_native4 ./...

build:
	CGO_ENABLED=0 $(GO) build -trimpath -tags="$(GO_BUILD_TAGS)" -o bin/arprt ./cmd/controller

test:
	$(GO) test -race ./...
	$(GO) test -race -tags=gray4_native4 ./...

verify: manifests release-check
	git diff --exit-code -- api/v1alpha1/zz_generated.deepcopy.go config/crd/bases charts/arprt-crds/files/crds
	$(MAKE) chart-check
	$(GO) mod tidy -diff
	$(MAKE) lint format-check test build

integration: manifests bin/setup-envtest $(HELM)
	ARPRT_HELM="$(HELM)" KUBEBUILDER_ASSETS="$$($(CURDIR)/bin/setup-envtest use $(ENVTEST_K8S_VERSION) --bin-dir $(ENVTEST_BIN) -p path)" $(GO) test -tags=integration -count=1 -v ./internal/integration

docker-build:
	docker build --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) --build-arg GO_BUILD_TAGS="$(GO_BUILD_TAGS)" -t $(IMAGE) .

# Run against an explicitly selected disposable cluster and loaded/pushed IMAGE.
cluster-smoke: chart-package
	python3 tools/cluster-smoke.py --image "$(IMAGE)" --helm "$(HELM)" --packages "$(CHART_OUTPUT)"
	python3 tools/job-smoke.py --image "$(IMAGE)" --helm "$(HELM)" --packages "$(CHART_OUTPUT)"
	python3 tools/playlist-smoke.py --image "$(IMAGE)" --helm "$(HELM)" --packages "$(CHART_OUTPUT)"
	python3 tools/operations-smoke.py --image "$(IMAGE)" --helm "$(HELM)" --packages "$(CHART_OUTPUT)"
	python3 tools/image-smoke.py --image "$(IMAGE)" --helm "$(HELM)" --packages "$(CHART_OUTPUT)"

# Release helpers never use the operator's selected kubeconfig context.
bin/kind:
	GOBIN=$(CURDIR)/bin $(GO) install sigs.k8s.io/kind@$(KIND_VERSION)

disposable-cluster-smoke: bin/kind chart-package
	PATH="$(CURDIR)/bin:$$PATH" ARPRT_HELM="$(HELM)" ARPRT_CHART_PACKAGES="$(CHART_OUTPUT)" KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" bash tools/disposable-cluster-smoke.sh "$(IMAGE)" $(if $(filter true,$(LOAD_IMAGE)),--load,)

# Measures production processing in an isolated 500m CPU / 512 MiB cgroup.
image-benchmark:
	python3 tools/image-benchmark.py

release-check:
	python3 -m unittest discover -s tools -p 'test_*.py'
