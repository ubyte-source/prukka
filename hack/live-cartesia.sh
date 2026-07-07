#!/usr/bin/env bash
# Live Cartesia conformance: drives the production client against the real
# API — preset synthesis on any plan, full timbre cloning where the plan
# allows. Paid and opt-in: never wired into CI.
#
#   prukka key set cartesia   # once: the key lives in the OS keychain
#   hack/live-cartesia.sh
#
# The tests resolve the key in-process from the keychain (the daemon's own
# seam); it never crosses this shell. The spoken clone reference is
# synthesized here (macOS `say` + ffmpeg), so the Go tests execute no
# subprocesses — exec stays in the sanctioned scope.
set -euo pipefail
cd "$(dirname "$0")/.."

FF="${PRUKKA_TEST_FFMPEG:-$(command -v ffmpeg || true)}"
REF=""

if command -v say >/dev/null && [ -n "$FF" ]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  say -o "$tmp/ref.aiff" \
    "This is a live conformance reference for Prukka. \
     The quick brown fox jumps over the lazy dog, \
     and the engine keeps every speaker's own voice across languages."
  "$FF" -hide_banner -loglevel error -i "$tmp/ref.aiff" \
    -f s16le -ar 16000 -ac 1 "$tmp/ref.s16"
  REF="$tmp/ref.s16"
else
  echo "==> no \`say\`/ffmpeg: preset synthesis only (clone test will skip)"
fi

PRUKKA_LIVE_CARTESIA=1 PRUKKA_LIVE_REFERENCE="$REF" \
  go test -race -run "TestLive" -v ./internal/providers/cartesia/
