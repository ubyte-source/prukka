#!/usr/bin/env bash
# Dubbing demo: live dubbing — the Italian speech fixture
# streams in, and /{session}/{lang}/audio.ts serves the English dub mixed
# over the ducked original bed. The result lands in /tmp for listening.
# Needs a local speech engine and ffmpeg (prukka setup); skips cleanly without
# either prerequisite, but never converts a runtime failure into a skip.
set -euo pipefail
cd "$(dirname "$0")/.."

source hack/lib/demo.sh

FIXTURE="$PWD/hack/fixtures/speech-it.wav"
OUT="${PRUKKA_DEMO_OUT:-/tmp/prukka-dub-en.ts}"

demo_init "${PRUKKA_DEMO_PORT:-18097}"
demo_require_engine "dubbing demo" || exit 0
demo_require_ffmpeg "dubbing demo" || exit 0
demo_start_daemon debug

"$BIN" session add dub-demo --in "file://$FIXTURE" --langs en --source it
echo "==> session created (dubbing on by default)"

# Pull the live transport stream from the start: the dub lands at the session
# delay (D=8s) and then drains, so capturing only after the first caption would
# miss it. 25 s of capture covers the fixture plus D plus provider latency.
echo "==> capturing the dubbed English stream (25 s)…"
curl -sS --connect-timeout 2 --max-time 25 \
  "http://$PRUKKA_HTTP/dub-demo/en/audio.ts" -o "$OUT" &
capture_pid=$!

# Fail fast on a provider or pipeline failure while the capture runs.
for _ in $(seq 1 20); do
  if grep -q "lane unavailable" "$PRUKKA_STATE/daemon.log"; then
    reason=$(grep "lane unavailable" "$PRUKKA_STATE/daemon.log" | tail -1)
    kill "$capture_pid" 2>/dev/null || true
    wait "$capture_pid" 2>/dev/null || true
    echo "FAIL: dubbing lane unavailable: $reason"
    exit 1
  fi

  grep -q '"msg":"caption"' "$PRUKKA_STATE/daemon.log" && break
  sleep 0.5
done

wait "$capture_pid" || true

size=$(wc -c <"$OUT" | tr -d ' ')
if [ "$size" -lt 20000 ]; then
  echo "FAIL: transport stream too small ($size bytes); daemon log tail:"
  tail -20 "$PRUKKA_STATE/daemon.log"
  exit 1
fi

# 0x47 is the MPEG-TS sync byte.
head -c1 "$OUT" | od -An -tx1 | grep -q 47 || { echo "FAIL: not an MPEG-TS"; exit 1; }

grep -q '"msg":"voice take synthesized"' "$PRUKKA_STATE/daemon.log" || {
  echo "FAIL: no dubbed segment in the log"; exit 1; }

echo "==> dubbed stream: $OUT ($size bytes) — listen with: ffplay $OUT"
"$BIN" stats
echo "==> dubbing demo PASS"
