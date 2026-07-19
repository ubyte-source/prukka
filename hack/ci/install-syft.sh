#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:?usage: install-syft.sh <version> <tools-dir> <checksums...>}"
TOOLS="${2:?usage: install-syft.sh <version> <tools-dir> <checksums...>}"

[[ "$VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
  echo "invalid Syft version: $VERSION" >&2
  exit 1
}

case "$(uname -s)/$(uname -m)" in
  Darwin/arm64)  PLATFORM="darwin_arm64"; EXPECTED="${3:?missing darwin-arm64 checksum}" ;;
  Darwin/x86_64) PLATFORM="darwin_amd64"; EXPECTED="${4:?missing darwin-amd64 checksum}" ;;
  Linux/aarch64) PLATFORM="linux_arm64"; EXPECTED="${5:?missing linux-arm64 checksum}" ;;
  Linux/x86_64)  PLATFORM="linux_amd64"; EXPECTED="${6:?missing linux-amd64 checksum}" ;;
  *) echo "unsupported platform: $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac

[[ "$EXPECTED" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid Syft checksum" >&2; exit 1; }

NUMBER="${VERSION#v}"
NAME="syft_${NUMBER}_${PLATFORM}.tar.gz"
DEST="$TOOLS/bin/syft"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl --proto '=https' --tlsv1.2 -fsSL \
  "https://github.com/anchore/syft/releases/download/$VERSION/$NAME" \
  -o "$TMP/$NAME"
printf '%s  %s\n' "$EXPECTED" "$TMP/$NAME" | shasum -a 256 -c -
tar -xzf "$TMP/$NAME" -C "$TMP" syft

mkdir -p "$TOOLS/bin"
install -m 0755 "$TMP/syft" "$DEST"
"$DEST" version -o json | grep -Fq "\"version\": \"$NUMBER\""
