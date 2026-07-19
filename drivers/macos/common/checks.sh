# Shared CI gates for the macOS native builds, sourced by the driver,
# capture-helper and webcam build scripts so a gate fix lands everywhere
# at once. Callers set MACOSX_DEPLOYMENT_TARGET before sourcing (or accept
# the caller's default); require_universal needs no configuration.

# vtool prints minos with a fractional part; normalize a bare "12"
# override so the checks compare like with like.
case "${MACOSX_DEPLOYMENT_TARGET:-}" in
  "" | *.*) ;;
  *) MACOSX_DEPLOYMENT_TARGET="$MACOSX_DEPLOYMENT_TARGET.0" ;;
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
