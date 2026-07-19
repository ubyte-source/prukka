#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
umask 022

ROOT=$(git rev-parse --show-toplevel)
GORELEASER=${GORELEASER:-$ROOT/.tools/bin/goreleaser}
GO_BIN=${PRUKKA_GO:-$(go env GOROOT)/bin/go}
COMMIT=$(git -C "$ROOT" rev-parse HEAD)
EPOCH=$(git -C "$ROOT" show -s --format=%ct "$COMMIT")
TAG=${GORELEASER_CURRENT_TAG:-}
if [[ -z "$TAG" ]]; then
  TAG=$(git -C "$ROOT" describe --tags --exact-match HEAD 2>/dev/null || true)
fi

[[ $(uname -s) == Darwin ]] || {
  echo "release reproducibility requires macOS for the cgo/AppKit builds" >&2
  exit 1
}
[[ -n "$TAG" ]] || { echo "HEAD is not an exact release tag" >&2; exit 1; }
[[ -x "$GORELEASER" ]] || { echo "GoReleaser not found: $GORELEASER" >&2; exit 1; }
[[ -x "$GO_BIN" ]] || { echo "Go toolchain not found: $GO_BIN" >&2; exit 1; }
[[ $(git -C "$ROOT" rev-parse "refs/tags/$TAG^{commit}") == "$COMMIT" ]] || {
  echo "release tag $TAG does not resolve to HEAD" >&2
  exit 1
}
git -C "$ROOT" diff --quiet || { echo "tracked worktree changes are not reproducible" >&2; exit 1; }
git -C "$ROOT" diff --cached --quiet || { echo "staged changes are not reproducible" >&2; exit 1; }

expected_version=$(awk '/^GORELEASER_VERSION/ {print $3}' "$ROOT/tools/versions.mk")
actual_version=$("$GORELEASER" --version | awk '$1 == "GitVersion:" {print $2}')
[[ "$actual_version" == "${expected_version#v}" ]] || {
  echo "GoReleaser version $actual_version does not match $expected_version" >&2
  exit 1
}
(cd "$ROOT" && "$GORELEASER" check)
required_go=$(awk '$1 == "go" {print "go" $2}' "$ROOT/go.mod")
actual_go=$("$GO_BIN" version | awk '{print $3}')
[[ "$actual_go" == "$required_go" ]] || {
  echo "Go version $actual_go does not match $required_go" >&2
  exit 1
}

assets=(
  internal/devices/assets/darwin/microphone.tar.gz
  internal/devices/assets/darwin/speaker.tar.gz
  internal/devices/assets/darwin/webcam.tar.gz
  internal/devices/assets/linux/src.tar.gz
  internal/devices/assets/windows/webcam.tar.gz
)
archives=(
  prukka_darwin_amd64.tar.gz
  prukka_darwin_arm64.tar.gz
  prukka_linux_amd64.tar.gz
  prukka_linux_arm64.tar.gz
  prukka_windows_amd64.zip
)

for asset in "${assets[@]}"; do
  [[ -s "$ROOT/$asset" ]] || { echo "missing driver payload: $asset" >&2; exit 1; }
done

STAGE=$(mktemp -d "${TMPDIR:-/tmp}/prukka-release-repro.XXXXXX")
OUTPUT_STAGE=
cleanup() {
  chmod -R u+w "$STAGE" 2>/dev/null || true
  rm -rf "$STAGE"
  if [[ -n "$OUTPUT_STAGE" ]]; then
    rm -rf "$OUTPUT_STAGE"
  fi
}
trap cleanup EXIT

build_once() {
  local number=$1
  local run=$STAGE/run-$number
  local checkout=$run/repo

  mkdir -p "$run/cache" "$run/home" "$run/modcache" "$run/tmp"
  git clone --quiet --no-local --no-checkout "$ROOT" "$checkout"
  git -C "$checkout" checkout --quiet --detach "$COMMIT"
  for asset in "${assets[@]}"; do
    mkdir -p "$checkout/$(dirname "$asset")"
    cp "$ROOT/$asset" "$checkout/$asset"
  done

  (
    cd "$checkout"
    PATH="$(dirname "$GO_BIN"):$PATH" \
      HOME="$run/home" \
      GOCACHE="$run/cache" \
      GOMODCACHE="$run/modcache" \
      GOENV=off \
      GOTOOLCHAIN=local \
      SOURCE_DATE_EPOCH="$EPOCH" \
      TMPDIR="$run/tmp" \
      TZ=UTC \
      ZERO_AR_DATE=1 \
      GORELEASER_CURRENT_TAG="$TAG" \
      "$GORELEASER" release --clean --skip=publish
  )

  for archive in "${archives[@]}"; do
    [[ -s "$checkout/dist/$archive" ]] || {
      echo "missing release archive: $archive" >&2
      exit 1
    }
  done
  [[ -s "$checkout/dist/metadata.json" ]] || { echo "missing GoReleaser metadata" >&2; exit 1; }
  (cd "$checkout/dist" && shasum -a 256 "${archives[@]}") > "$run/archives.sha256"
}

build_once 1
build_once 2
diff -u "$STAGE/run-1/archives.sha256" "$STAGE/run-2/archives.sha256"
OUTPUT_STAGE=$(mktemp -d "$ROOT/.dist-repro.XXXXXX")
cp -R "$STAGE/run-1/repo/dist/." "$OUTPUT_STAGE/"
rm -rf "$ROOT/dist"
mv "$OUTPUT_STAGE" "$ROOT/dist"
OUTPUT_STAGE=
echo "release archives are bit-for-bit reproducible for the fixed, attested driver payload set"
