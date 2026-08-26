.PHONY: help setup build install test test-race lint vet vuln check clean tidy fmt bench fuzz \
        image image-base image-builder image-daemon image-distroless image-lite \
        images quadlet-install quadlet-status container-check cover cover-html cover-gate

BIN         := bin/rousseau
PKG         := ./...
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Container engine. Defaults to podman because the Quadlet units, the
# rootless UserNS=keep-id mapping and the pasta network stack are all
# podman features. Docker works for plain builds -- override with
# `make images ENGINE=docker` -- but the quadlet-* targets are
# podman-only and will refuse to run under anything else.
ENGINE      ?= podman
IMAGE_PREFIX ?= localhost
QUADLET_DIR ?= $(HOME)/.config/containers/systemd
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

# -- coverage ---------------------------------------------------------

cover: ## Run tests with coverage and print the total
	@go test $(PKG) -coverprofile=coverage.out -covermode=atomic
	@go tool cover -func=coverage.out | tail -1

cover-gate: ## Enforce the 95% total and per-package coverage floors
	@go test $(PKG) -coverprofile=coverage.out -covermode=atomic >/dev/null
	@bash scripts/coverage-gate.sh coverage.out 95 95

cover-html: cover ## Write an HTML coverage report
	@go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

# -- container images -------------------------------------------------
#
# Every target uses $(ENGINE), which defaults to podman. Build order
# matters for the builder image: it derives FROM agent-base, so
# `image-builder` depends on `image-base` rather than assuming a
# previously built layer is lying around.

image-base: ## Build agent-base (shared foundation)
	@$(ENGINE) build -f docker/Dockerfile.base -t $(IMAGE_PREFIX)/agent-base:local .

image-builder: image-base ## Build agent-builder (polyglot build env)
	@$(ENGINE) build -f docker/Dockerfile.builder \
		--build-arg BASE_IMAGE=$(IMAGE_PREFIX)/agent-base:local \
		-t $(IMAGE_PREFIX)/agent-builder:local .

image-daemon: ## Build the full runtime daemon
	@$(ENGINE) build -f docker/Dockerfile \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(DATE) \
		-t $(IMAGE_PREFIX)/rousseau-agent:local .

image-distroless: ## Build the distroless runtime
	@$(ENGINE) build -f docker/Dockerfile.distroless -t $(IMAGE_PREFIX)/rousseau-agent:distroless .

image-lite: ## Build the lite runtime (no whatsmeow)
	@$(ENGINE) build -f docker/Dockerfile.lite -t $(IMAGE_PREFIX)/rousseau-agent:lite .

image: image-daemon ## Alias for image-daemon

images: image-base image-builder image-daemon image-distroless image-lite ## Build every image

# -- quadlet (podman only) --------------------------------------------

quadlet-install: ## Install Quadlet units into ~/.config/containers/systemd
	@if [ "$(ENGINE)" != "podman" ]; then \
		echo "quadlet-install requires podman (ENGINE=$(ENGINE))"; exit 1; fi
	@mkdir -p $(QUADLET_DIR)
	@cp docker/rousseau-agent.container docker/agent-builder.container $(QUADLET_DIR)/
	@systemctl --user daemon-reload
	@echo "installed to $(QUADLET_DIR); start with:"
	@echo "  systemctl --user start rousseau-agent"
	@echo "  systemctl --user start agent-builder"

quadlet-status: ## Show status of both Quadlet units
	@systemctl --user status rousseau-agent agent-builder --no-pager || true

container-check: ## Verify the host can run the hardened containers rootlessly
	@bash docker/preflight.sh
