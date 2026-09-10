MODULE   := github.com/agnaldom/mcp-k8s
BINARY   := mcp-k8s
CMD      := ./cmd/mcp-k8s
BIN_DIR  := ./bin
IMAGE    ?= mcp-k8s

GO      ?= go
GOFMT   ?= gofmt
DOCKER  ?= docker

.PHONY: all build image test unit integration lint fmt vet tidy clean ci

all: build

## build: compile the binary into bin/
build:
	$(GO) build -ldflags "-X github.com/agnaldom/mcp-k8s/internal/version.Version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) -X github.com/agnaldom/mcp-k8s/internal/version.Commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none)" -o $(BIN_DIR)/$(BINARY) $(CMD)

## image: build the distroless container image (spec §2, §6.2)
image:
	$(DOCKER) build \
		--build-arg VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
		--build-arg COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) \
		-t $(IMAGE) .

## test: run unit and integration tests (integration requires kind)
test: unit integration

## unit: fast tests with fakes only (fake.Clientset, dynamic fake, fake discovery)
unit:
	$(GO) test -race -short ./...

## integration: kind-based tests, gated by -tags integration
integration:
	$(GO) test -race -tags integration ./...

## kind-up/kind-down: local integration environment
kind-up:
	kind create cluster --name mcp-k8s-integration
	integration

kind-down:
	kind delete cluster --name mcp-k8s-integration

## lint: go vet plus gofmt check plus layer-direction check
lint: vet fmt-check layers

# Permanent rule (spec §15): internal/kubernetes must not depend on
# internal/services or internal/tools. Enforce it mechanically.
layers:
	@if go list -deps ./internal/kubernetes | grep -q '$(MODULE)/internal/\(services\|tools\)'; then \
		echo "layer violation: internal/kubernetes imports internal/services or internal/tools"; \
		exit 1; \
	fi

vet:
	$(GO) vet ./...

fmt-check:
	@out=$$($(GOFMT) -l .); \
	if [ -n "$$out" ]; then \
		echo "gofmt: these files need formatting:"; echo "$$out"; exit 1; \
	fi

fmt:
	$(GOFMT) -w .

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)

## ci: everything CI runs
ci: tidy fmt-check vet unit
