#!/usr/bin/env bash
# Shared build for the Prukka HAL audio drivers: the caller's folder
# supplies identity.h + Info.plist over the common plugin core. Flow:
# compile → contract harness (a driver that fails it cannot be produced)
# → ad-hoc sign. Command Line Tools alone; no Xcode.
set -euo pipefail

[ $# -eq 1 ] || { echo "usage: $0 <driver-folder>" >&2; exit 2; }

DIR="$(cd "$1" && pwd)"
COMMON="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR"

EXE=$(plutil -extract CFBundleExecutable raw Info.plist)
OUT=build
DRIVER="$OUT/$EXE.driver"

# Published drivers must not silently inherit the CI runner's deployment
# floor. Keep this aligned with engine/build.sh while allowing maintainers to
# raise it explicitly for a specialized build.
export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-12.0}"
. "$COMMON/checks.sh"
DEPLOYMENT_FLAG="-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"

rm -rf "$OUT"
mkdir -p "$DRIVER/Contents/MacOS"

echo "==> compiling $EXE (universal, macOS >= $MACOSX_DEPLOYMENT_TARGET)"
# Both architectures in one bundle: coreaudiod loads its native slice, so a
# driver built on either CI runner works on every Mac.
# CoreAudio fixes callback signatures, including parameters a device does not
# consume. Keep that single ABI warning disabled; every other warning is fatal.
xcrun clang -bundle -O2 -Wall -Wextra -Werror -Wno-unused-parameter \
  -arch x86_64 -arch arm64 \
  "$DEPLOYMENT_FLAG" \
  -I "$DIR" \
  -framework CoreAudio -framework CoreFoundation \
  -o "$DRIVER/Contents/MacOS/$EXE" "$COMMON/plugin.c"

cp Info.plist "$DRIVER/Contents/Info.plist"

require_universal "$DRIVER/Contents/MacOS/$EXE" "$EXE"
check_minos "$DRIVER/Contents/MacOS/$EXE" "$EXE" x86_64 arm64

echo "==> contract harness"
xcrun clang -O2 -Wall -Wextra -Werror -I "$DIR" \
  "$DEPLOYMENT_FLAG" \
  -framework CoreAudio -framework CoreFoundation \
  -o "$OUT/harness" "$COMMON/harness.c"
check_minos "$OUT/harness" harness "$(uname -m)"
"$OUT/harness" "$DRIVER/Contents/MacOS/$EXE"

echo "==> signing (ad-hoc, local use)"
codesign --force --sign - "$DRIVER"

echo "==> built: $DRIVER"
echo "Install/upgrade: use the staged same-filesystem procedure in $COMMON/README.md"
