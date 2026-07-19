#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

VERSION="${1:?usage: install-goreleaser.sh <version> <tools-dir> <checksums...>}"
TOOLS="${2:?usage: install-goreleaser.sh <version> <tools-dir> <checksums...>}"

[[ "$VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
  echo "invalid GoReleaser version: $VERSION" >&2
  exit 1
}

case "$(uname -s)/$(uname -m)" in
  Darwin/arm64)  PLATFORM="Darwin_arm64"; EXPECTED="${3:?missing darwin-arm64 checksum}" ;;
  Darwin/x86_64) PLATFORM="Darwin_x86_64"; EXPECTED="${4:?missing darwin-amd64 checksum}" ;;
  Linux/aarch64) PLATFORM="Linux_arm64"; EXPECTED="${5:?missing linux-arm64 checksum}" ;;
  Linux/x86_64)  PLATFORM="Linux_x86_64"; EXPECTED="${6:?missing linux-amd64 checksum}" ;;
  *) echo "unsupported platform: $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac

[[ "$EXPECTED" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid GoReleaser checksum" >&2; exit 1; }

NAME="goreleaser_${PLATFORM}.tar.gz"
DEST="$TOOLS/bin/goreleaser"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl --proto '=https' --tlsv1.2 -fsSL \
  "https://github.com/goreleaser/goreleaser/releases/download/$VERSION/$NAME" \
  -o "$TMP/$NAME"
printf '%s  %s\n' "$EXPECTED" "$TMP/$NAME" | shasum -a 256 -c -
tar -xzf "$TMP/$NAME" -C "$TMP" goreleaser

mkdir -p "$TOOLS/bin"
install -m 0755 "$TMP/goreleaser" "$DEST"
INSTALLED_VERSION="$("$DEST" --version | awk '$1 == "GitVersion:" { print $2 }')"
[[ "$INSTALLED_VERSION" == "${VERSION#v}" ]] || {
  echo "unexpected GoReleaser version: $INSTALLED_VERSION" >&2
  exit 1
}
