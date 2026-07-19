#!/usr/bin/env bash
# bundled-gate.sh — prove the bundleddrivers build tag, the only
# configuration that ships (.goreleaser.yaml), compiles, lints and tests.
# The real driver archives exist only at release time, so a go overlay maps
# the five embedded payload paths onto one placeholder — the same trick
# third-party-notices.mjs uses. The overlay rides GOFLAGS for golangci-lint
# (it has no -overlay flag), and the tag rides the CLI, never .golangci.yml,
# which is byte-anchored by LINTER.sha256. The tag only alters
# internal/devices, so that package bounds the scope.
set -euo pipefail
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo_root"

temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT
printf 'placeholder\n' > "$temp/payload"
overlay=$temp/overlay.json
{
  printf '{"Replace":{'
  first=1
  for path in \
    internal/devices/assets/darwin/microphone.tar.gz \
    internal/devices/assets/darwin/speaker.tar.gz \
    internal/devices/assets/darwin/webcam.tar.gz \
    internal/devices/assets/linux/src.tar.gz \
    internal/devices/assets/windows/webcam.tar.gz; do
    ((first)) || printf ','
    first=0
    printf '"%s/%s":"%s"' "$repo_root" "$path" "$temp/payload"
  done
  printf '}}'
} > "$overlay"

for goos in darwin linux windows; do
  GOOS=$goos go build -overlay "$overlay" -tags bundleddrivers ./internal/devices/
  GOOS=$goos GOFLAGS="-overlay=$overlay" .tools/bin/golangci-lint run \
    --build-tags bundleddrivers ./internal/devices/...
done

# Only the tagged payload tests run: the untagged suite asserts the
# not-bundled contract (ErrNotBundled), which the tag makes unreachable.
go test -overlay "$overlay" -tags bundleddrivers -count=1 -run 'TestPayloads' ./internal/devices/

echo "bundled-gate: PASS"
