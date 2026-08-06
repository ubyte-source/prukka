#!/usr/bin/env bash
# Install the pinned ShellCheck into .tools/bin. ShellCheck is a Haskell
# binary, so unlike the other pinned linters it cannot be `go install`-ed: the
# archive is verified against the checksum in tools/versions.mk before unpacking.
set -euo pipefail

VERSION="${1:?usage: install-shellcheck.sh <version> <tools-dir> <checksums...>}"
TOOLS="${2:?usage: install-shellcheck.sh <version> <tools-dir> <checksums...>}"

[[ "$VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
  echo "invalid ShellCheck version: $VERSION" >&2
  exit 1
}

case "$(uname -s)/$(uname -m)" in
  Darwin/arm64)  PLATFORM="darwin.aarch64"; EXPECTED="${3:?missing darwin-arm64 checksum}" ;;
  Darwin/x86_64) PLATFORM="darwin.x86_64"; EXPECTED="${4:?missing darwin-amd64 checksum}" ;;
  Linux/aarch64) PLATFORM="linux.aarch64"; EXPECTED="${5:?missing linux-arm64 checksum}" ;;
  Linux/x86_64)  PLATFORM="linux.x86_64"; EXPECTED="${6:?missing linux-amd64 checksum}" ;;
  *) echo "unsupported platform: $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac

[[ "$EXPECTED" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid ShellCheck checksum" >&2; exit 1; }

NAME="shellcheck-$VERSION.$PLATFORM.tar.gz"
DEST="$TOOLS/bin/shellcheck"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl --proto '=https' --tlsv1.2 -fsSL --retry 4 --retry-delay 5 \
  "https://github.com/koalaman/shellcheck/releases/download/$VERSION/$NAME" \
  -o "$TMP/$NAME"
printf '%s  %s\n' "$EXPECTED" "$TMP/$NAME" | shasum -a 256 -c -
tar -xzf "$TMP/$NAME" -C "$TMP" "shellcheck-$VERSION/shellcheck"

mkdir -p "$TOOLS/bin"
install -m 0755 "$TMP/shellcheck-$VERSION/shellcheck" "$DEST"
"$DEST" --version | grep -Fqx "version: ${VERSION#v}"
