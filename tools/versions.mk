# Pinned developer-tool versions (CONTRIBUTING.md, Engineering
# Constitution, "The linter contract").
# Changing the golangci-lint pin requires maintainer review (see CODEOWNERS);
# CI fails any pull request that alters it without that review.
GOLANGCI_LINT_VERSION := v2.12.2
BUF_VERSION := v1.71.0
PROTOC_GEN_GO_VERSION := v1.36.11
PROTOC_GEN_GO_GRPC_VERSION := v1.6.2
GRPC_GATEWAY_VERSION := v2.29.0
GOVULNCHECK_VERSION := v1.5.0
# Node is a BUILD-time dependency only (the dashboard SPA); users always
# install a single Go binary with the built app embedded.
NODE_VERSION := v24.18.0
