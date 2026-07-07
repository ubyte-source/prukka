#!/usr/bin/env bash
# Build Prukka Camera WITHOUT Xcode: the Command Line Tools' swiftc and SDK
# compile everything, the app and system-extension bundles are assembled by
# hand, and codesign signs them ad-hoc for local (developer-mode) use.
# Distribution signing arrives with a Developer ID; nothing here needs one.
set -euo pipefail
cd "$(dirname "$0")"

SDK="$(xcrun --show-sdk-path)"
ARCH="$(uname -m)"
TARGET="${ARCH}-apple-macos13.0"
OUT=build
APP="$OUT/Prukka Camera.app"
EXT_ID=it.ubyte.prukka.camera.extension
SYSEXT="$APP/Contents/Library/SystemExtensions/$EXT_ID.systemextension"

rm -rf "$OUT"
mkdir -p "$APP/Contents/MacOS" "$SYSEXT/Contents/MacOS"

echo "==> compiling extension"
xcrun swiftc -O -sdk "$SDK" -target "$TARGET" \
  -framework CoreMediaIO -framework IOKit \
  Extension/main.swift Extension/Provider.swift \
  -o "$SYSEXT/Contents/MacOS/$EXT_ID"

echo "==> compiling host app"
xcrun swiftc -O -parse-as-library -sdk "$SDK" -target "$TARGET" \
  -framework SwiftUI -framework SystemExtensions \
  App/PrukkaCameraApp.swift \
  -o "$APP/Contents/MacOS/PrukkaCamera"

echo "==> compiling feeder"
xcrun swiftc -O -sdk "$SDK" -target "$TARGET" \
  -framework AVFoundation -framework CoreMediaIO \
  Feeder/main.swift \
  -o "$OUT/prukka-camfeed"

cp Extension/Info.plist "$SYSEXT/Contents/Info.plist"
cp App/Info.plist "$APP/Contents/Info.plist"

echo "==> signing (ad-hoc, local use)"
codesign --force --sign - \
  --entitlements Extension/Extension.entitlements "$SYSEXT"
codesign --force --sign - \
  --entitlements App/App.entitlements "$APP"

# Gate the bundles like the mic driver gates its contract: a plist that
# does not parse, an extension id that drifted from EXT_ID or a signature
# that fails verification would each burn an activation attempt with an
# opaque system error.
echo "==> verifying bundles"
plutil -lint "$SYSEXT/Contents/Info.plist" "$APP/Contents/Info.plist" >/dev/null

ext_id=$(plutil -extract CFBundleIdentifier raw "$SYSEXT/Contents/Info.plist")
[ "$ext_id" = "$EXT_ID" ] || {
  echo "FAIL: extension bundle id $ext_id != $EXT_ID (activation would not find it)"
  exit 1
}

codesign --verify --strict "$SYSEXT"
codesign --verify --deep --strict "$APP"
echo "verify: plists parse, extension id matches, signatures hold"

echo "==> built: $APP"
echo
echo "Activate (one-time, no developer account):"
echo "  1. systemextensionsctl developer on        # allows running from any folder"
echo "  2. open '$APP' → Activate → approve in System Settings"
echo "  3. ./$OUT/prukka-camfeed http://127.0.0.1:8080/<session>/master.m3u8"
