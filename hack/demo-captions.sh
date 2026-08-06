#!/usr/bin/env bash
# Captions demo: a real speech fixture streams through the file:// ingress at
# real time and rolling WebVTT appears. Needs PRUKKA_DEMO_ENGINE; every engine
# or pipeline error fails the gate.
set -euo pipefail
cd "$(dirname "$0")/.."

source hack/lib/demo.sh

FIXTURE="$PWD/hack/fixtures/speech-it.wav"

demo_init "${PRUKKA_DEMO_PORT:-18098}"
demo_require_engine "captions demo" || exit 0

# The RTMP phase needs the same external ffmpeg in the daemon and encoder
# processes, so expose it before the daemon inherits PATH.
PUSHER="${PRUKKA_DEMO_FFMPEG:-$(command -v ffmpeg || true)}"
[ -z "$PUSHER" ] || demo_expose_ffmpeg "$PUSHER"

demo_start_daemon debug

"$BIN" session add live-demo --in "file://$FIXTURE" --langs it,en --source it
echo "==> session created over $FIXTURE"

found=""
for _ in $(seq 1 60); do
  if curl -fsS --connect-timeout 2 --max-time 5 \
    "http://$PRUKKA_HTTP/live-demo/en/subs.vtt" 2>/dev/null | grep -q -- "-->"; then
    found=yes
    break
  fi

  if grep -q "lane unavailable" "$PRUKKA_STATE/daemon.log"; then
    reason=$(grep "lane unavailable" "$PRUKKA_STATE/daemon.log" | tail -1)
    echo "FAIL: captions lane unavailable: $reason"
    exit 1
  fi

  sleep 0.5
done

if [ -z "$found" ]; then
  echo "FAIL: no captions appeared; daemon log tail:"
  tail -20 "$PRUKKA_STATE/daemon.log"
  exit 1
fi

echo "==> Italian captions (transcript, no MT cost):"
curl -fsS --connect-timeout 2 --max-time 5 "http://$PRUKKA_HTTP/live-demo/it/subs.vtt"
echo "==> English captions (translated):"
curl -fsS --connect-timeout 2 --max-time 5 "http://$PRUKKA_HTTP/live-demo/en/subs.vtt"

echo "==> daemon stats:"
"$BIN" stats

# Phase 2 — a real RTMP stream.

if [ -z "$PUSHER" ]; then
  echo "==> RTMP phase skipped (no encoder-side ffmpeg; run \`prukka setup\` or install ffmpeg)"
  echo "==> captions demo PASS (file source)"
  exit 0
fi

echo "==> RTMP phase: pushing the fixture as a live AAC/FLV stream"

# A clean live-source end keeps its captions, so they are read after the push.
"$BIN" session add rtmp-demo --in rtmp://127.0.0.1:19350/live/demo --langs it,en --source it
sleep 2
"$PUSHER" -nostdin -hide_banner -loglevel error -re -i "$FIXTURE" \
  -c:a aac -b:a 128k -f flv rtmp://127.0.0.1:19350/live/demo &
PUSHER_PID=$!
demo_register_job "$PUSHER_PID" captions-pusher
pusher_deadline=$((SECONDS + 45))
while demo_job_is_owned "$PUSHER_PID" && [ "$SECONDS" -lt "$pusher_deadline" ]; do
  sleep 0.2
done
if demo_job_is_owned "$PUSHER_PID"; then
  if ! demo_stop_owned_job "$PUSHER_PID" captions-pusher; then
    echo "FAIL: RTMP pusher cleanup failed; state retained for recovery" >&2
    exit 1
  fi
  demo_forget_job "$PUSHER_PID"
  echo "FAIL: RTMP pusher exceeded 45 seconds" >&2
  exit 1
fi
if wait "$PUSHER_PID"; then pusher_status=0; else pusher_status=$?; fi
demo_forget_job "$PUSHER_PID"
if [ "$pusher_status" -ne 0 ]; then
  echo "FAIL: RTMP pusher exited with status $pusher_status" >&2
  exit 1
fi

for _ in $(seq 1 40); do
  if curl -fsS --connect-timeout 2 --max-time 5 \
    "http://$PRUKKA_HTTP/rtmp-demo/en/subs.vtt" 2>/dev/null | grep -q -- "-->"; then
    echo "==> RTMP captions:"
    curl -fsS --connect-timeout 2 --max-time 5 \
      "http://$PRUKKA_HTTP/rtmp-demo/en/subs.vtt"
    echo "==> captions demo PASS (file + RTMP)"
    exit 0
  fi

  sleep 0.5
done

echo "FAIL: no RTMP captions; daemon log tail:"
tail -20 "$PRUKKA_STATE/daemon.log"
exit 1
