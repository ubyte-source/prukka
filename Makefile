SHELL := /bin/bash
include tools/versions.mk

TOOLS_BIN := $(CURDIR)/.tools/bin
GOLANGCI  := $(TOOLS_BIN)/golangci-lint
ACTIONLINT := $(TOOLS_BIN)/actionlint
BUF       := $(TOOLS_BIN)/buf

GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# -s -w strip the symbol table and DWARF; the daemon needs neither at
# runtime and it roughly halves the binary. -trimpath removes build-machine
# paths from compiler metadata. PGO applies automatically after `make pgo`
# creates a representative cmd/prukka/default.pgo (Go 1.21+).
LDFLAGS := -s -w -X main.commit=$(GIT_COMMIT)

# macOS: embed Info.plist into the binary (__TEXT __info_plist). TCC only
# prompts for microphone/camera access when the responsible executable
# carries usage descriptions; without this the daemon's capture is killed
# with SIGABRT before any permission dialog can appear.
ifeq ($(shell uname -s),Darwin)
LDFLAGS += -linkmode=external -extldflags "-Wl,-sectcreate,__TEXT,__info_plist,$(CURDIR)/cmd/prukka/Info.plist"
endif

export PATH := $(TOOLS_BIN):$(PATH)

.PHONY: all
all: build

.PHONY: tools
tools: ## Install the pinned Go developer tools into .tools/bin.
	GOBIN=$(TOOLS_BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	GOBIN=$(TOOLS_BIN) go install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	GOBIN=$(TOOLS_BIN) go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	GOBIN=$(TOOLS_BIN) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOBIN=$(TOOLS_BIN) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	GOBIN=$(TOOLS_BIN) go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@$(GRPC_GATEWAY_VERSION)

.PHONY: tools-node
tools-node: ## Install the pinned Node.js (build-time only, checksum-verified) into .tools/node.
	hack/ci/install-node.sh $(NODE_VERSION) $(CURDIR)/.tools \
	  $(NODE_SHA256_DARWIN_ARM64) $(NODE_SHA256_DARWIN_X64) \
	  $(NODE_SHA256_LINUX_ARM64) $(NODE_SHA256_LINUX_X64)

.PHONY: tools-syft
tools-syft: ## Install the pinned, checksum-verified release SBOM generator.
	hack/ci/install-syft.sh $(SYFT_VERSION) $(CURDIR)/.tools \
	  $(SYFT_SHA256_DARWIN_ARM64) $(SYFT_SHA256_DARWIN_AMD64) \
	  $(SYFT_SHA256_LINUX_ARM64) $(SYFT_SHA256_LINUX_AMD64)

.PHONY: tools-goreleaser
tools-goreleaser: ## Install the pinned, checksum-verified release packager.
	hack/ci/install-goreleaser.sh $(GORELEASER_VERSION) $(CURDIR)/.tools \
	  $(GORELEASER_SHA256_DARWIN_ARM64) $(GORELEASER_SHA256_DARWIN_AMD64) \
	  $(GORELEASER_SHA256_LINUX_ARM64) $(GORELEASER_SHA256_LINUX_AMD64)

NPM := PATH="$(TOOLS_BIN):$$PATH" npm
NODE := $(TOOLS_BIN)/node

.PHONY: web
web: tools-node ## Rebuild the embedded dashboard (internal/webui/dist) from its Svelte source.
	cd web && $(NPM) ci --no-fund --no-audit && $(NPM) run check && $(NPM) run build

.PHONY: web-audit
web-audit: tools-node ## Fail on known vulnerabilities in the locked dashboard dependency graph.
	cd web && $(NPM) audit --audit-level=low

.PHONY: licenses
licenses: tools-node ## Regenerate the third-party notices from locked dependencies.
	cd web && $(NPM) ci --no-fund --no-audit
	$(NODE) --test hack/ci/third-party-notices.test.mjs
	$(NODE) hack/ci/third-party-notices.mjs

.PHONY: licenses-check
licenses-check: tools-node ## Verify that the committed third-party notices match all release inputs.
	cd web && $(NPM) ci --no-fund --no-audit
	$(NODE) --test hack/ci/third-party-notices.test.mjs
	@git ls-files --error-unmatch -- NOTICE.txt >/dev/null || { \
	  echo "NOTICE.txt is not tracked" >&2; exit 1; }
	@tmp=$$(mktemp); trap 'rm -f "$$tmp"' EXIT; \
	  $(NODE) hack/ci/third-party-notices.mjs "$$tmp"; \
	  diff -u NOTICE.txt "$$tmp"

.PHONY: web-e2e
web-e2e: web ## Rebuild, embed and run the dashboard cross-browser e2e suite.
	$(MAKE) build
	cd web && PATH="$(TOOLS_BIN):$$PATH" npx --no-install playwright test

.PHONY: lint
lint: lint-integrity lint-workflows modernize-check pgo-check ## Run the blocking maintainer lint gate.
	$(GOLANGCI) run ./...
	cd engine && ../.tools/bin/golangci-lint run ./...

.PHONY: lint-all
lint-all: lint-integrity lint-workflows modernize-check pgo-check ## Run the linter for every supported target OS.
	GOOS=darwin $(GOLANGCI) run ./...
	GOOS=linux $(GOLANGCI) run ./...
	GOOS=windows $(GOLANGCI) run ./...
	cd engine && ../.tools/bin/golangci-lint run ./...

.PHONY: modernize-check
modernize-check: ## Require gofmt/goimports output and current Go source rewrites.
	@diff=$$($(GOLANGCI) fmt --diff ./...) || exit $$?; \
	  [ -z "$$diff" ] || { printf '%s\n' "$$diff"; exit 1; }
	@diff=$$(cd engine && ../.tools/bin/golangci-lint fmt --diff ./...) || exit $$?; \
	  [ -z "$$diff" ] || { printf '%s\n' "$$diff"; exit 1; }
	go fix -diff ./...
	cd engine && go fix -diff ./...

.PHONY: lint-workflows
lint-workflows: ## Validate GitHub Actions syntax, expressions and shell fragments.
	$(ACTIONLINT)

.PHONY: lint-integrity
lint-integrity: ## Verify the maintainer linter config is byte-identical to its anchor.
	shasum -a 256 -c LINTER.sha256

.PHONY: fmt
fmt: ## Format the tree with the linter's own formatters (gofmt + goimports).
	$(GOLANGCI) fmt ./...
	cd engine && ../.tools/bin/golangci-lint fmt ./...

.PHONY: gen
gen: ## Regenerate protobuf/gRPC/gateway code from proto/.
	cd proto && $(BUF) generate

.PHONY: proto-breaking
proto-breaking: ## Check prukka.v1 compatibility against the appropriate prior release.
	hack/ci/proto-breaking-gate-test.sh
	hack/ci/proto-breaking-gate.sh

.PHONY: pgo-check
pgo-check: ## Verify any committed PGO profile against its source and provenance.
	hack/ci/pgo-profile-gate.sh

.PHONY: build
build: pgo-check ## Build the prukka binary into bin/ (stripped, trimmed, PGO if present).
	go build ./...
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/prukka ./cmd/prukka
ifeq ($(shell uname -s),Darwin)
	codesign -f -s - bin/prukka
endif

.PHONY: test
test: ## Run all tests with the race detector (and the test-mapping gate).
	hack/ci/test-mapping-gate.sh
	go test -race ./...
	cd engine && go test -race ./...

.PHONY: bench
bench: ## Run the hot-path benchmarks with the zero-alloc gate.
	hack/ci/bench-gate.sh

.PHONY: cover-gate
cover-gate: ## Enforce statement-coverage floors for critical portable packages.
	hack/ci/coverage-gate.sh

.PHONY: loadgen-test
loadgen-test: ## Test runtime cleanup, load configuration, deadlines and per-lane probes.
	hack/ci/demo-ffmpeg-path-gate.sh
	hack/ci/loadgen-gate-test.sh

.PHONY: load
load: build loadgen-test ## Verify 10 simultaneous it→en sessions (needs a local speech engine + ffmpeg).
	hack/loadgen.sh

.PHONY: pgo
pgo: build ## Refresh cmd/prukka/default.pgo under real local-engine load (needs ffmpeg).
	hack/pgo.sh
	$(MAKE) build

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
cover: ## Report statement coverage for the daemon and speech-engine modules.
	go test -race -coverprofile=coverage.out $$(go list ./... | grep -v '/internal/gen/prukka/v1$$')
	go tool cover -func=coverage.out | tail -1
	cd engine && go test -race -coverprofile=coverage.out ./...
	cd engine && go tool cover -func=coverage.out | tail -1

.PHONY: dev
dev: build ## Run the daemon in the foreground with a local dev config.
	./bin/prukka up --config hack/dev/config.yaml

.PHONY: demo-control
demo-control: build ## Run the control-plane demo, end to end.
	hack/demo-control.sh

.PHONY: demo-captions
demo-captions: build ## Run the live-caption demo (needs a local speech engine).
	hack/demo-captions.sh

.PHONY: demo-dubbing
demo-dubbing: build ## Run the live-dubbing demo (needs a local engine + ffmpeg).
	hack/demo-dubbing.sh

.PHONY: demo-video
demo-video: build ## Run the video HLS demo (needs a local engine + ffmpeg).
	hack/demo-video.sh

.PHONY: clean
clean: ## Remove build outputs (keeps installed tools).
	rm -rf bin dist coverage.out

.PHONY: help
help: ## Show this help.
	@grep -hE '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "} {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
