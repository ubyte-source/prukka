# Pinned tools

Every developer tool is pinned in [`versions.mk`](versions.mk) and installed into
the repo-local `.tools/bin` directory by `make tools`. CI installs the exact same
pins, so `make lint` behaves identically everywhere.

| Tool | Purpose |
|---|---|
| `golangci-lint` | The maintainer's lint gate. The configuration (`/.golangci.yml`) and its integrity anchor (`/LINTER.sha256`) are **read-only** for contributors and coding agents alike — code adapts to the linter, never the other way around. |
| `buf` | Protobuf compiler and lint for the control-plane API (`proto/`). |
| `protoc-gen-go`, `protoc-gen-go-grpc`, `protoc-gen-grpc-gateway` | Go, gRPC and REST-gateway code generators invoked by `buf generate`. |

This directory is owned by the maintainers (see `/.github/CODEOWNERS`). Pull
requests that change a pin without maintainer review fail CI.
