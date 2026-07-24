.SHELLFLAGS := -eu -c
SHELL := /bin/sh
.DEFAULT_GOAL := build
.PHONY: all build run test race fmt fmt-check vet lint tidy check clean install version

GO ?= go
BINARY := forge
PKG := ./...
BINARY_PKG := ./cmd/forge

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X 'neuroforge/internal/version.Version=$(VERSION)' \
           -X 'neuroforge/internal/version.Commit=$(COMMIT)'  \
           -X 'neuroforge/internal/version.Date=$(DATE)'

all: build

build: ## Build the forge binary with version metadata injected via -ldflags
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) $(BINARY_PKG)

run: build ## Run the built binary (pass extra args via ARGS="...")
	./$(BINARY) $(ARGS)

test: ## Run all unit tests
	$(GO) test $(PKG)

race: ## Run tests with the race detector
	$(GO) test -race $(PKG)

vet: ## Run go vet
	$(GO) vet $(PKG)

fmt: ## Apply gofmt to all Go sources
	gofmt -w .

fmt-check: ## Fail if any Go source is not gofmt-clean
	@unformatted=$$(gofmt -l . 2>/dev/null); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt would reformat:"; echo "$$unformatted"; exit 1; \
	else \
		echo "gofmt: clean"; \
	fi

lint: ## Run golangci-lint if available, otherwise fall back to go vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo ">> golangci-lint run $(PKG)"; golangci-lint run $(PKG); \
	else \
		echo ">> golangci-lint not installed; falling back to 'go vet'"; \
		$(GO) vet $(PKG); \
	fi

tidy: ## Tidy module files
	$(GO) mod tidy

check: fmt-check vet test ## Run formatting, vet and tests (the default CI gate)

clean: ## Remove built artifacts
	rm -f $(BINARY)

install: build ## Install the binary into GOBIN
	$(GO) install -ldflags "$(LDFLAGS)" $(BINARY_PKG)

version: ## Print the version string that would be baked into the binary
	@echo "$(VERSION)"
