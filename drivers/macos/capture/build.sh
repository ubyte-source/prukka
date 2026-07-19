#!/usr/bin/env bash
# Build the native microphone-capture helper: a universal (x86_64 + arm64)
# Swift binary with the Info.plist embedded in __TEXT,__info_plist so TCC can
# read its microphone usage description, signed with the shared local identity
# so the grant survives rebuilds. Command Line Tools alone; no Xcode.
#
# Usage: build.sh [output-dir]   (defaults to ./build)
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
OUT="${1:-$DIR/build}"
BIN="$OUT/prukka-miccapture"
PLIST="$DIR/Info.plist"

# Keep the deployment floor aligned with the HAL drivers so a helper built on
# either CI runner loads on every supported Mac.
export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-12.0}"

# The shared local identity keeps the TCC microphone grant alive across
# rebuilds; absent it, ad-hoc still produces a runnable binary.
IDENTITY="${PRUKKA_CODESIGN_IDENTITY:--}"

mkdir -p "$OUT"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "==> compiling prukka-miccapture (universal, macOS >= $MACOSX_DEPLOYMENT_TARGET)"
for arch in arm64 x86_64; do
  swiftc -O -target "$arch-apple-macosx$MACOSX_DEPLOYMENT_TARGET" \
    -framework AVFoundation -framework CoreMedia \
    -Xlinker -sectcreate -Xlinker __TEXT -Xlinker __info_plist -Xlinker "$PLIST" \
    -o "$tmp/prukka-miccapture-$arch" "$DIR/miccapture.swift"
done
lipo -create "$tmp/prukka-miccapture-arm64" "$tmp/prukka-miccapture-x86_64" -output "$BIN"

for arch in arm64 x86_64; do
  actual=$(vtool -show-build -arch "$arch" "$BIN" | awk '$1 == "minos" { print $2; exit }')
  if [ "$actual" != "$MACOSX_DEPLOYMENT_TARGET" ]; then
    echo "FAIL: $arch minOS is ${actual:-missing}, want $MACOSX_DEPLOYMENT_TARGET"
    exit 1
  fi
done

echo "==> signing with '$IDENTITY'"
codesign --force --sign "$IDENTITY" "$BIN"
codesign --verify --strict "$BIN"

echo "==> built $BIN"
