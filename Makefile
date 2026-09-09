MODULE   := github.com/agnaldom/mcp-k8s
BINARY   := mcp-k8s
CMD      := ./cmd/mcp-k8s
BIN_DIR  := ./bin

GO      ?= go
GOFMT   ?= gofmt

.PHONY: all build test unit integration lint fmt vet tidy clean ci

all: build

## build: compile the binary into bin/
build:
	$(GO) build -o $(BIN_DIR)/$(BINARY) $(CMD)

## test: run unit and integration tests (integration requires kind)
test: unit integration

## unit: fast tests with fakes only (fake.Clientset, dynamic fake, fake discovery)
unit:
	$(GO) test -race -short ./...

## integration: kind-based tests, gated by -tags integration
integration:
	$(GO) test -race -tags integration ./...

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
