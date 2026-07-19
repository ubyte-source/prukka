# Pinned tools

Every developer tool is pinned in [`versions.mk`](versions.mk) and installed
under the repo-local `.tools/` directory. `make tools` installs the core Go
toolchain; the named build and release targets below install their exact pins.

| Tool | Purpose |
|---|---|
| `golangci-lint` | The maintainer's lint gate. The configuration (`/.golangci.yml`) and its integrity anchor (`/LINTER.sha256`) are **read-only** for contributors and coding agents alike — code adapts to the linter, never the other way around. |
| `actionlint` | GitHub Actions syntax, expression and embedded-shell validation. |
| `buf` | Protobuf compiler and lint for the control-plane API (`proto/`). |
| `protoc-gen-go`, `protoc-gen-go-grpc`, `protoc-gen-grpc-gateway` | Go, gRPC and REST-gateway code generators invoked by `buf generate`. |
| `govulncheck` | Reachability-aware Go vulnerability scan used by CI and release gates. |
| `Node.js` | Checksum-verified dashboard build toolchain installed by `make tools-node`. |
| `Syft` | Checksum-verified SPDX generator installed by `make tools-syft`. |
| `GoReleaser` | Checksum-verified release packager installed by `make tools-goreleaser`. |

This directory is owned by the maintainers (see `/.github/CODEOWNERS`). Pull
requests that change a pin without maintainer review fail CI.
