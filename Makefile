SHELL := /bin/bash
include tools/versions.mk

TOOLS_BIN := $(CURDIR)/.tools/bin
GOLANGCI  := $(TOOLS_BIN)/golangci-lint
BUF       := $(TOOLS_BIN)/buf

GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# -s -w strip the symbol table and DWARF; the daemon needs neither at
# runtime and it roughly halves the binary. Combined with -trimpath below
# for reproducible builds. PGO applies automatically when a
# cmd/prukka/default.pgo profile is present (Go 1.21+).
LDFLAGS := -s -w -X main.commit=$(GIT_COMMIT)

export PATH := $(TOOLS_BIN):$(PATH)

.PHONY: all
all: build

.PHONY: tools
tools: ## Install the pinned Go developer tools into .tools/bin.
	GOBIN=$(TOOLS_BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	GOBIN=$(TOOLS_BIN) go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	GOBIN=$(TOOLS_BIN) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOBIN=$(TOOLS_BIN) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	GOBIN=$(TOOLS_BIN) go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@$(GRPC_GATEWAY_VERSION)

.PHONY: tools-node
tools-node: ## Install the pinned Node.js (build-time only, checksum-verified) into .tools/node.
	hack/ci/install-node.sh $(NODE_VERSION) $(CURDIR)/.tools

NPM := PATH="$(TOOLS_BIN):$$PATH" npm

.PHONY: web
web: ## Rebuild the embedded dashboard (internal/webui/dist) from its Svelte source.
	cd web && $(NPM) ci --no-fund --no-audit && $(NPM) run check && $(NPM) run build

.PHONY: web-e2e
web-e2e: ## Run the dashboard cross-browser e2e suite (Playwright, real daemon).
	cd web && PATH="$(TOOLS_BIN):$$PATH" npx playwright test

.PHONY: lint
lint: lint-integrity ## Run the maintainer lint gate (blocking, zero-nolint policy).
	$(GOLANGCI) run ./...

.PHONY: lint-integrity
lint-integrity: ## Verify the maintainer linter config is byte-identical to its anchor.
	shasum -a 256 -c LINTER.sha256

.PHONY: fmt
fmt: ## Format the tree with the linter's own formatters (gofmt + goimports).
	$(GOLANGCI) fmt ./...

.PHONY: gen
gen: ## Regenerate protobuf/gRPC/gateway code from proto/.
	cd proto && $(BUF) generate

.PHONY: build
build: ## Build the prukka binary into bin/ (stripped, trimmed, PGO if present).
	go build ./...
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/prukka ./cmd/prukka

.PHONY: test
test: ## Run all tests with the race detector (and the test-mapping gate).
	hack/ci/test-mapping-gate.sh
	go test -race ./...

.PHONY: bench
bench: ## Run the hot-path benchmarks with the zero-alloc gate.
	hack/ci/bench-gate.sh

.PHONY: load
load: build ## Run the load gate: 10 sessions × 3 languages under the CPU budget (needs key + ffmpeg).
	hack/loadgen.sh

.PHONY: webcam
webcam: ## Build the native macOS virtual webcam (no Xcode needed; see drivers/macos/webcam/README.md).
	drivers/macos/webcam/build.sh

.PHONY: mic
mic: ## Build the native macOS virtual microphone (contract-harness gated; see drivers/macos/microphone/README.md).
	drivers/macos/microphone/build.sh

.PHONY: speaker
speaker: ## Build the native macOS virtual speaker (contract-harness gated; see drivers/macos/audio/README.md).
	drivers/macos/audio/build.sh

.PHONY: drivers
drivers: webcam mic speaker ## Build every native driver this host can (the full per-OS matrix runs in CI).

.PHONY: cover
cover: ## Run tests with coverage for the core packages.
	go test -race -coverprofile=coverage.out ./internal/core/...
	go tool cover -func=coverage.out | tail -1

.PHONY: dev
dev: build ## Run the daemon in the foreground with a local dev config.
	./bin/prukka up --config hack/dev/config.yaml

.PHONY: demo-control
demo-control: build ## Run the control-plane demo, end to end.
	hack/demo-control.sh

.PHONY: demo-captions
demo-captions: build ## Run the live-caption demo (needs an OpenRouter key).
	hack/demo-captions.sh

.PHONY: demo-dubbing
demo-dubbing: build ## Run the live-dubbing demo (needs key + ffmpeg).
	hack/demo-dubbing.sh

.PHONY: demo-video
demo-video: build ## Run the video HLS demo: passthrough + dub + live subs + burn-in push (needs key + ffmpeg).
	hack/demo-video.sh

.PHONY: demo-autovoice
demo-autovoice: build ## Run the two-speaker auto-voice demo (needs key + ffmpeg + macOS say).
	hack/demo-autovoice.sh

.PHONY: clean
clean: ## Remove build outputs (keeps installed tools).
	rm -rf bin dist coverage.out

.PHONY: help
help: ## Show this help.
	@grep -hE '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "} {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
