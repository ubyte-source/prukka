#!/usr/bin/env bash
# Build Prukka Camera WITHOUT Xcode: the Command Line Tools' swiftc and SDK
# compile everything, the app and system-extension bundles are assembled by
# hand, and codesign signs them ad-hoc for local (developer-mode) use.
# Distribution signing arrives with a Developer ID; nothing here needs one.
set -euo pipefail
cd "$(dirname "$0")"

SDK="$(xcrun --show-sdk-path)"
OUT=build
APP="$OUT/Prukka Camera.app"
EXT_ID=it.ubyte.prukka.camera.extension
SYSEXT="$APP/Contents/Library/SystemExtensions/$EXT_ID.systemextension"

rm -rf "$OUT"
mkdir -p "$APP/Contents/MacOS" "$SYSEXT/Contents/MacOS"

# universal compiles one Swift binary for both architectures and joins the
# slices, so a build from either CI runner runs on every Mac.
universal() {
  out="$1"; shift
  xcrun swiftc -O -sdk "$SDK" -target x86_64-apple-macos13.0 "$@" -o "$out.x86_64"
  xcrun swiftc -O -sdk "$SDK" -target arm64-apple-macos13.0 "$@" -o "$out.arm64"
  xcrun lipo -create "$out.x86_64" "$out.arm64" -output "$out"
  rm "$out.x86_64" "$out.arm64"
}

echo "==> compiling extension (universal)"
universal "$SYSEXT/Contents/MacOS/$EXT_ID" \
  -framework CoreMediaIO -framework IOKit \
  Extension/main.swift Extension/Provider.swift

echo "==> compiling host app (universal)"
universal "$APP/Contents/MacOS/PrukkaCamera" -parse-as-library \
  -framework SwiftUI -framework SystemExtensions \
  App/PrukkaCameraApp.swift

echo "==> compiling feeder (universal, shipped inside the app bundle)"
universal "$APP/Contents/MacOS/prukka-camfeed" \
  -framework AVFoundation -framework CoreImage -framework CoreMediaIO \
  Feeder/main.swift

cp Extension/Info.plist "$SYSEXT/Contents/Info.plist"
cp App/Info.plist "$APP/Contents/Info.plist"

echo "==> rendering the app icon (brand mark, no binary assets in the repo)"
mkdir -p "$APP/Contents/Resources" "$OUT/icon.iconset"
xcrun swiftc -O -sdk "$SDK" App/icongen.swift -o "$OUT/icongen"
"$OUT/icongen" "$OUT/icon1024.png"
for s in 16 32 128 256 512; do
  sips -z "$s" "$s" "$OUT/icon1024.png" --out "$OUT/icon.iconset/icon_${s}x${s}.png" >/dev/null
  d=$((s * 2))
  sips -z "$d" "$d" "$OUT/icon1024.png" --out "$OUT/icon.iconset/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$OUT/icon.iconset" -o "$APP/Contents/Resources/PrukkaCamera.icns"

# PRUKKA_CODESIGN_IDENTITY selects a real signing identity (Developer ID);
# the default "-" is ad-hoc. The system-extension entitlements are
# restricted: claimed under an ad-hoc signature, AMFI kills the app at
# launch ("Code Signature Invalid") — so they are applied only when a real
# identity signs, and the ad-hoc app opens and can explain itself.
IDENTITY="${PRUKKA_CODESIGN_IDENTITY:--}"

echo "==> signing (identity: $IDENTITY)"
if [ "$IDENTITY" = "-" ]; then
  codesign --force --sign - "$SYSEXT"
else
  codesign --force --sign "$IDENTITY" \
    --entitlements Extension/Extension.entitlements "$SYSEXT"
fi

# The feeder is a second executable in the bundle: nested code must be
# signed before the enclosing app or the app signature fails verification.
codesign --force --sign "$IDENTITY" "$APP/Contents/MacOS/prukka-camfeed"

if [ "$IDENTITY" = "-" ]; then
  codesign --force --sign - "$APP"
else
  codesign --force --sign "$IDENTITY" \
    --entitlements App/App.entitlements "$APP"
fi

# Gate the bundles like the mic driver gates its contract: a plist that
# does not parse, an extension id that drifted from EXT_ID or a signature
# that fails verification would each burn an activation attempt with an
# opaque system error.
echo "==> verifying bundles (universal + integrity)"
. ../common/checks.sh
for bin in "$SYSEXT/Contents/MacOS/$EXT_ID" \
           "$APP/Contents/MacOS/PrukkaCamera" \
           "$APP/Contents/MacOS/prukka-camfeed"; do
  require_universal "$bin" "$(basename "$bin")"
done

plutil -lint "$SYSEXT/Contents/Info.plist" "$APP/Contents/Info.plist" >/dev/null

ext_id=$(plutil -extract CFBundleIdentifier raw "$SYSEXT/Contents/Info.plist")
[ "$ext_id" = "$EXT_ID" ] || {
  echo "FAIL: extension bundle id $ext_id != $EXT_ID (activation would not find it)"
  exit 1
}

codesign --verify --strict "$SYSEXT"
codesign --verify --deep --strict "$APP"
"$APP/Contents/MacOS/prukka-camfeed" --self-test
echo "verify: plists parse, extension id matches, signatures hold, scaler contract passes"

echo "==> built: $APP"
echo
if [ "$IDENTITY" = "-" ]; then
  echo "Ad-hoc build: the app opens and explains that activating the camera"
  echo "extension needs a Developer-ID-signed build. Produce one with:"
  echo "  PRUKKA_CODESIGN_IDENTITY='Developer ID Application: …' $0"
else
  echo "Activate (one-time):"
  echo "  1. open '$APP' → Activate → approve in System Settings"
  echo "  2. '$APP/Contents/MacOS/prukka-camfeed' http://127.0.0.1:8080/<session>/master.m3u8"
fi
