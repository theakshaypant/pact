# pact — development harness. Run `make help` to list targets.

BIN     := bin/pact
PKG     := github.com/theakshaypant/pact/cmd/pact
MODULE  := $(shell GOFLAGS= go list -m)
GOFLAGS := -mod=vendor
# Pinned so CI runs the analyzer everyone else ran, like golangci-lint
# and gotestsum are pinned.
NILAWAY_VERSION := v0.0.0-20261112030514-ccd005690d1e

.PHONY: help build test test-verbose lint lint-go lint-fmt lint-nilaway fmt fmt-check vet tidy check clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Build the pact binary into bin/
	go build $(GOFLAGS) -o $(BIN) $(PKG)

install: ## Install the pact binary into GOBIN (~/go/bin)
	go install $(GOFLAGS) ./cmd/pact

test: ## Run all tests (unit + e2e) with the race detector
	go test $(GOFLAGS) -race ./...

test-verbose: ## Run tests verbosely, with pact's own matcher logs enabled
	PACT_E2E_VERBOSE=1 go test $(GOFLAGS) -v ./...

lint: ## Run golangci-lint
	golangci-lint run

lint-go: ## Run golangci-lint (target name used by .tekton/lint.yaml)
	golangci-lint run

lint-fmt: fmt-check ## Fail if any Go source is unformatted (target name used by .tekton/lint.yaml)

lint-nilaway: ## Run nilaway nil-error static analysis (target name used by .tekton/lint.yaml)
	GOFLAGS= go run go.uber.org/nilaway/cmd/nilaway@$(NILAWAY_VERSION) -include-pkgs $(MODULE) ./...

fmt: ## Format all Go sources
	go fmt ./...

fmt-check: ## Fail if any Go source is unformatted
	@out=$$(gofmt -l cmd pkg test 2>/dev/null || true); \
	if [ -n "$$out" ]; then \
		echo "gofmt needed on:"; echo "$$out"; \
		exit 1; \
	fi

vet: ## Run go vet
	go vet $(GOFLAGS) ./...

tidy: ## Run go mod tidy and refresh vendor/
	go mod tidy && go mod vendor

check: fmt-check lint vet test ## Run everything a PR must pass

clean: ## Remove build artifacts
	rm -rf bin
