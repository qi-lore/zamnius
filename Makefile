# Makefile for Zamnius.
#
# `make verify` mirrors .github/workflows/ci.yml exactly and is the gate a
# change must pass before review. Targets are self-documented: run `make`
# (or `make help`) to list them.
#
# The verify gate is order-sensitive — a broken module must fail before any
# feature test runs — so execution is forced serial even under `make -j`.

.NOTPARALLEL:
.DEFAULT_GOAL := help
SHELL := /bin/sh

GO            ?= go
GOLANGCI_LINT ?= golangci-lint
COVERAGE      ?= coverage.out
BIN           ?= bin/zamnius

.PHONY: help build build-pure bin run vet fmt fmt-check lint \
        test test-race test-recoverability verify check \
        coverage coverage-html tidy clean

help: ## List available targets
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile every package (go build ./...)
	$(GO) build ./...

build-pure: ## Compile with cgo disabled (CGO_ENABLED=0 go build ./...)
	CGO_ENABLED=0 $(GO) build ./...

bin: ## Build the CLI into $(BIN)
	@mkdir -p $(dir $(BIN))
	$(GO) build -o $(BIN) ./cmd/zamnius

run: ## Run the CLI from source (pass flags via ARGS="...")
	$(GO) run ./cmd/zamnius $(ARGS)

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format all Go files with gofmt
	gofmt -w .

fmt-check: ## Fail if any Go file is not gofmt-clean
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt needed for:"; echo "$$out"; exit 1; \
	fi

lint: ## Run golangci-lint (v2; must be installed)
	@if ! command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		echo "$(GOLANGCI_LINT) not found; install golangci-lint v2: https://golangci-lint.run/welcome/install/"; \
		exit 1; \
	fi
	$(GOLANGCI_LINT) run

test: ## Run every test once
	$(GO) test ./... -count=1

test-race: ## Run every test under the race detector
	$(GO) test ./... -race -count=1

test-recoverability: ## Run the recoverability suite ten times, no flakes tolerated
	$(GO) test ./test/recoverability -count=10

verify: build vet test test-race test-recoverability build-pure ## Full CI gate, in order (mirrors ci.yml)

check: fmt-check lint verify ## Local gate: gofmt check, lint, then the full CI gate

coverage: ## Whole-module coverage using -coverpkg=./...
	$(GO) test ./... -coverpkg=./... -coverprofile=$(COVERAGE)
	@$(GO) tool cover -func=$(COVERAGE) | tail -1

coverage-html: coverage ## Write coverage.html from the coverage profile
	$(GO) tool cover -html=$(COVERAGE) -o coverage.html
	@echo "wrote coverage.html"

tidy: ## Tidy go.mod and go.sum
	$(GO) mod tidy

clean: ## Remove coverage and build artifacts
	rm -rf $(COVERAGE) coverage.html bin
