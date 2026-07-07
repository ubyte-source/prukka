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

echo "==> compiling $EXE"
xcrun clang -bundle -O2 -Wall -Wextra -Wno-unused-parameter \
  -I "$DIR" \
  -framework CoreAudio -framework CoreFoundation \
  -o "$DRIVER/Contents/MacOS/$EXE" "$COMMON/plugin.c"

cp Info.plist "$DRIVER/Contents/Info.plist"

echo "==> contract harness"
xcrun clang -O2 -Wall -Wextra -I "$DIR" \
  -framework CoreAudio -framework CoreFoundation \
  -o "$OUT/harness" "$COMMON/harness.c"
"$OUT/harness" "$DRIVER/Contents/MacOS/$EXE"

echo "==> signing (ad-hoc, local use)"
codesign --force --sign - "$DRIVER"

echo "==> built: $DRIVER"
echo "Install: sudo cp -R '$DIR/$DRIVER' /Library/Audio/Plug-Ins/HAL/ && sudo launchctl kickstart -kp system/com.apple.audio.coreaudiod"
