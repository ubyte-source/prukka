#!/usr/bin/env bash
# Captions demo: live captions end to end — a real speech
# fixture streams through the file:// ingress at real time, OpenRouter
# transcribes and translates it, and the rolling WebVTT appears on the data
# plane with a real cost rate.
#
# Needs an OpenRouter key: either OPENROUTER_API_KEY in the environment or
# the keychain entry behind providers.openrouter.key. Without one the demo
# reports SKIPPED and exits 0, so CI stays green until secrets exist.
set -euo pipefail
cd "$(dirname "$0")/.."

source hack/lib/demo.sh

FIXTURE="$PWD/hack/fixtures/speech-it.wav"

demo_init "${PRUKKA_DEMO_PORT:-18098}"
demo_start_daemon debug

"$BIN" session add live-demo --in "file://$FIXTURE" --langs it,en --source it
echo "==> session created over $FIXTURE"

# The fixture is ~8 s and streams at real time; allow provider latency on
# top before giving up.
found=""
for _ in $(seq 1 60); do
  if curl -fs "http://$PRUKKA_HTTP/live-demo/en/subs.vtt" 2>/dev/null | grep -q -- "-->"; then
    found=yes
    break
  fi

  if grep -q "lane unavailable" "$PRUKKA_STATE/daemon.log"; then
    reason=$(grep "lane unavailable" "$PRUKKA_STATE/daemon.log" | tail -1)
    echo "==> captions demo SKIPPED (no usable OpenRouter key): $reason"
    exit 0
  fi

  sleep 0.5
done

if [ -z "$found" ]; then
  echo "FAIL: no captions appeared; daemon log tail:"
  tail -20 "$PRUKKA_STATE/daemon.log"
  exit 1
fi

echo "==> Italian captions (transcript, no MT cost):"
curl -fs "http://$PRUKKA_HTTP/live-demo/it/subs.vtt"
echo "==> English captions (translated):"
curl -fs "http://$PRUKKA_HTTP/live-demo/en/subs.vtt"

echo "==> cost meter:"
"$BIN" stats

# Phase 2 — a real RTMP stream. Needs an encoder-
# side ffmpeg: PATH, or PRUKKA_DEMO_FFMPEG, or the managed install.
PUSHER="${PRUKKA_DEMO_FFMPEG:-$(command -v ffmpeg || true)}"
[ -z "$PUSHER" ] && [ -x "$PRUKKA_STATE/bin/ffmpeg" ] && PUSHER="$PRUKKA_STATE/bin/ffmpeg"

if [ -z "$PUSHER" ]; then
  echo "==> RTMP phase skipped (no encoder-side ffmpeg; run \`prukka setup\` or install ffmpeg)"
  echo "==> captions demo PASS (file source)"
  exit 0
fi

echo "==> RTMP phase: pushing the fixture as a live AAC/FLV stream"

# The daemon's lane needs ffmpeg too: expose the encoder binary through the
# managed install path of this demo's fresh state dir.
mkdir -p "$PRUKKA_STATE/bin"
ln -sf "$PUSHER" "$PRUKKA_STATE/bin/ffmpeg"

"$BIN" session add rtmp-demo --in rtmp://127.0.0.1:19350/live/demo --langs it,en --source it
sleep 2
"$PUSHER" -hide_banner -loglevel error -re -i "$FIXTURE" -c:a aac -b:a 128k -f flv \
  rtmp://127.0.0.1:19350/live/demo

for _ in $(seq 1 40); do
  if curl -fs "http://$PRUKKA_HTTP/rtmp-demo/en/subs.vtt" 2>/dev/null | grep -q -- "-->"; then
    echo "==> RTMP captions:"
    curl -fs "http://$PRUKKA_HTTP/rtmp-demo/en/subs.vtt"
    echo "==> captions demo PASS (file + RTMP)"
    exit 0
  fi

  sleep 0.5
done

echo "FAIL: no RTMP captions; daemon log tail:"
tail -20 "$PRUKKA_STATE/daemon.log"
exit 1
