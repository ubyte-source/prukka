# Shared CI gates for the macOS native builds, sourced by the driver,
# capture-helper, webcam and engine build scripts so a gate fix lands
# everywhere at once.
#
# This file also owns the deployment floor: every published macOS binary is
# compiled against MACOSX_DEPLOYMENT_TARGET and gated by check_minos, so a CI
# runner's own floor can never leak into a shipped artifact. A caller that
# needs a higher floor exports it before sourcing (the camera extension does:
# CMIOExtension is macOS 12.3+ API); everything else accepts the default.
export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-12.0}"

# vtool prints minos with a fractional part; normalize a bare "12"
# override so the checks compare like with like.
case "$MACOSX_DEPLOYMENT_TARGET" in
  *.*) ;;
  *) export MACOSX_DEPLOYMENT_TARGET="$MACOSX_DEPLOYMENT_TARGET.0" ;;
esac

# check_minos <binary> <label> <arch...> — every slice must carry exactly
# the declared deployment floor; a CI runner's own floor silently leaking
# in would produce a binary that refuses to load on supported Macs.
check_minos() {
  local binary="$1" label="$2"
  shift 2
  local arch actual
  for arch in "$@"; do
    actual=$(xcrun vtool -show-build -arch "$arch" "$binary" |
      awk '$1 == "minos" { print $2; exit }')
    if [ "$actual" != "$MACOSX_DEPLOYMENT_TARGET" ]; then
      echo "FAIL: $label/$arch minOS is ${actual:-missing}, want $MACOSX_DEPLOYMENT_TARGET"
      exit 1
    fi
  done
}

# require_universal <binary> <label> — both Mac architectures in one file:
# a single-arch binary looks installed but half the lineup cannot load it.
require_universal() {
  local binary="$1" label="$2" archs
  archs=$(xcrun lipo -archs "$binary")
  case "$archs" in
    *x86_64*arm64* | *arm64*x86_64*) ;;
    *) echo "FAIL: $label is not universal (archs: $archs)"; exit 1 ;;
  esac
}
