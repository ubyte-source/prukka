#!/usr/bin/env bash
# Shared build for the Prukka HAL audio drivers: the caller's folder
# supplies identity.h + Info.plist over the common plugin core. Flow:
# compile → contract harness (a driver that fails it cannot be produced)
# → ad-hoc sign. Command Line Tools alone; no Xcode.
set -euo pipefail

DIR="$(cd "$1" && pwd)"
COMMON="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR"

EXE=$(plutil -extract CFBundleExecutable raw Info.plist)
OUT=build
DRIVER="$OUT/$EXE.driver"

rm -rf "$OUT"
mkdir -p "$DRIVER/Contents/MacOS"

echo "==> compiling $EXE (universal)"
# Both architectures in one bundle: coreaudiod loads its native slice, so a
# driver built on either CI runner works on every Mac.
# CoreAudio fixes callback signatures, including parameters a device does not
# consume. Keep that single ABI warning disabled; every other warning is fatal.
xcrun clang -bundle -O2 -Wall -Wextra -Werror -Wno-unused-parameter \
  -arch x86_64 -arch arm64 \
  -I "$DIR" \
  -framework CoreAudio -framework CoreFoundation \
  -o "$DRIVER/Contents/MacOS/$EXE" "$COMMON/plugin.c"

cp Info.plist "$DRIVER/Contents/Info.plist"

# The bundle must carry both Mac architectures: a single-arch driver
# looks installed but coreaudiod on the other half of the lineup cannot
# load it. Gate it here so CI proves universality on every run.
archs=$(xcrun lipo -archs "$DRIVER/Contents/MacOS/$EXE")
case "$archs" in
  *x86_64*arm64* | *arm64*x86_64*) ;;
  *) echo "FAIL: driver is not universal (archs: $archs)"; exit 1 ;;
esac

echo "==> contract harness"
xcrun clang -O2 -Wall -Wextra -Werror -I "$DIR" \
  -framework CoreAudio -framework CoreFoundation \
  -o "$OUT/harness" "$COMMON/harness.c"
"$OUT/harness" "$DRIVER/Contents/MacOS/$EXE"

echo "==> signing (ad-hoc, local use)"
codesign --force --sign - "$DRIVER"

echo "==> built: $DRIVER"
echo "Install: sudo cp -R '$DIR/$DRIVER' /Library/Audio/Plug-Ins/HAL/ && sudo launchctl kickstart -kp system/com.apple.audio.coreaudiod"
