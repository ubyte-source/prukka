#!/usr/bin/env bash
# Install the pinned Node.js toolchain into .tools/node (repo-local, no
# global installs) and link node/npm/npx into .tools/bin. Node is a
# BUILD-time dependency only — the dashboard SPA compiles to static files
# embedded in the Go binary; end users never need it.
#
# The tarball is fetched from nodejs.org and verified against the checksum
# committed in tools/versions.mk before unpacking.
set -euo pipefail

VERSION="${1:?usage: install-node.sh <version> <tools-dir>}"
TOOLS="${2:?usage: install-node.sh <version> <tools-dir>}"

case "$(uname -s)/$(uname -m)" in
  Darwin/arm64)  PLATFORM="darwin-arm64"; EXPECTED="${3:?missing darwin-arm64 checksum}" ;;
  Darwin/x86_64) PLATFORM="darwin-x64"; EXPECTED="${4:?missing darwin-x64 checksum}" ;;
  Linux/aarch64) PLATFORM="linux-arm64"; EXPECTED="${5:?missing linux-arm64 checksum}" ;;
  Linux/x86_64)  PLATFORM="linux-x64"; EXPECTED="${6:?missing linux-x64 checksum}" ;;
  *) echo "unsupported platform: $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac

DEST="$TOOLS/node"
STAMP="$DEST/.version"

if [ -f "$STAMP" ] && [ "$(cat "$STAMP")" = "$VERSION-$PLATFORM" ] &&
  [ -x "$DEST/bin/node" ] && [ -x "$DEST/bin/npm" ] && [ -x "$DEST/bin/npx" ] &&
  [ "$("$DEST/bin/node" --version 2>/dev/null || true)" = "$VERSION" ]; then
  mkdir -p "$TOOLS/bin"
  for bin in node npm npx; do
    ln -sf "$DEST/bin/$bin" "$TOOLS/bin/$bin"
  done
  echo "node $VERSION already installed"
  exit 0
fi

NAME="node-$VERSION-$PLATFORM"
BASE="https://nodejs.org/dist/$VERSION"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "downloading node $VERSION ($PLATFORM)…"
curl -fsSL "$BASE/$NAME.tar.gz" -o "$TMP/$NAME.tar.gz"

echo "verifying checksum…"
printf '%s  %s\n' "$EXPECTED" "$TMP/$NAME.tar.gz" | shasum -a 256 -c -

rm -rf "$DEST"
mkdir -p "$DEST"
tar -xzf "$TMP/$NAME.tar.gz" -C "$DEST" --strip-components=1
echo "$VERSION-$PLATFORM" > "$STAMP"

mkdir -p "$TOOLS/bin"
for bin in node npm npx; do
  ln -sf "$DEST/bin/$bin" "$TOOLS/bin/$bin"
done

echo "node installed: $("$TOOLS/bin/node" --version)"
