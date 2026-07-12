.PHONY: help setup build install test test-race lint vet vuln check clean tidy fmt bench fuzz

BIN         := bin/rousseau
PKG         := ./...
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
               -X 'github.com/sebastienrousseau/rousseau-agent/internal/cli.version=$(VERSION)' \
               -X 'github.com/sebastienrousseau/rousseau-agent/internal/cli.commit=$(COMMIT)' \
               -X 'github.com/sebastienrousseau/rousseau-agent/internal/cli.buildDate=$(DATE)'

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: ## Install dev tools (golangci-lint, govulncheck)
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	@go install golang.org/x/vuln/cmd/govulncheck@latest

tidy: ## Sync go.mod / go.sum
	@go mod tidy

fmt: ## Format code
	@go fmt $(PKG)

vet: ## go vet
	@go vet $(PKG)

lint: ## Run golangci-lint
	@golangci-lint run

test: ## Run tests
	@go test -count=1 $(PKG)

test-race: ## Run tests with race detector
	@go test -race -count=1 $(PKG)

vuln: ## Scan for known vulnerabilities
	@govulncheck $(PKG)

bench: ## Run all Go benchmarks
	@go test -run=^$$ -bench=. -benchmem $(PKG)

fuzz: ## Run every Fuzz function for 10s each
	@for pkg in $$(go list $(PKG) | xargs -I{} sh -c 'go test -list=Fuzz {} 2>/dev/null | grep -q Fuzz && echo {}'); do \
	    echo "== fuzzing $$pkg =="; \
	    for fn in $$(go test -list=Fuzz $$pkg | grep -E '^Fuzz'); do \
	        go test -run=^$$ -fuzz=^$$fn$$ -fuzztime=10s $$pkg; \
	    done; \
	done

check: vet lint test-race vuln ## Full quality gate

build: ## Build the binary
	@mkdir -p bin
	@go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/rousseau

install: ## Install the binary to $GOBIN
	@go install -trimpath -ldflags="$(LDFLAGS)" ./cmd/rousseau

clean: ## Remove build artifacts
	@rm -rf bin/ dist/ coverage.out coverage.html
