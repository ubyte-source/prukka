#!/usr/bin/env bash
# Video demo: a real audio+video source streams in, and
# /{session}/master.m3u8 serves the passthrough video rendition alongside
# the dubbed-audio and live-subtitle renditions; a `subs=burn` push draws
# the live captions onto the pushed video and the pixels
# prove it. The fixture is generated on the fly (test pattern + the
# Italian speech fixture).
# Needs a local speech engine and ffmpeg (prukka setup); skips cleanly without
# either prerequisite, but never converts a runtime failure into a skip.
set -euo pipefail
cd "$(dirname "$0")/.."

source hack/lib/demo.sh

WAV="$PWD/hack/fixtures/speech-it.wav"

demo_init "${PRUKKA_DEMO_PORT:-18092}"
demo_require_engine "video demo" || exit 0
demo_require_ffmpeg "video demo" || exit 0
demo_start_daemon debug

# A real AV fixture: H.264 test pattern with the Italian speech as audio.
# The bottom 80 px are a solid black band, so the burn-in check below can
# attribute any pixel difference there to drawn text alone.
FIXTURE="$PRUKKA_STATE/speech-it.mp4"
"$FF" -hide_banner -loglevel error \
  -f lavfi -i "testsrc2=size=640x280:rate=25" -i "$WAV" \
  -map 0:v -map 1:a -vf "pad=640:360:0:0:black" \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -g 25 -keyint_min 25 \
  -c:a aac -b:a 128k -shortest "$FIXTURE"
echo "==> generated AV fixture"

# The burn-in push listener starts before the session so the push can begin
# the moment the video rendition exists — the live cue window (PTS+D) must
# fall inside the push.
FLV="$PRUKKA_STATE/pushed.flv"
"$FF" -hide_banner -loglevel error -listen 1 -i rtmp://127.0.0.1:19351/live/burn \
  -c copy -t 30 -y "$FLV" &
LISTENER=$!
demo_register_job "$LISTENER" burn-listener
sleep 1

"$BIN" session add video-demo --in "file://$FIXTURE" --langs en --source it
echo "==> session created over the AV fixture"

for _ in $(seq 1 20); do
  if grep -q "lane unavailable" "$PRUKKA_STATE/daemon.log"; then
    reason=$(grep "lane unavailable" "$PRUKKA_STATE/daemon.log" | tail -1)
    echo "FAIL: video lane unavailable: $reason"
    exit 1
  fi

  grep -q "ffmpeg started" "$PRUKKA_STATE/daemon.log" && break
  sleep 0.5
done

# The video rendition appears within the first segment (~4 s of source).
echo "==> waiting for the passthrough video rendition…"
found=""
for _ in $(seq 1 40); do
  if curl -fsS --connect-timeout 2 --max-time 5 \
    "http://$PRUKKA_HTTP/video-demo/master.m3u8" 2>/dev/null | grep -q "video/index.m3u8"; then
    found=yes
    break
  fi
  sleep 0.5
done
[ -n "$found" ] || { echo "FAIL: master never advertised video"; tail -20 "$PRUKKA_STATE/daemon.log"; exit 1; }

echo "==> master playlist:"
curl -fsS --connect-timeout 2 --max-time 5 "http://$PRUKKA_HTTP/video-demo/master.m3u8"

# Start the burn-in push right away: the cue window (source 0–4.4 shown at
# output 8–12.4 on the wall clock) must land while the push encodes.
TOKEN=$(cat "$PRUKKA_STATE/control.token")
curl -fsS --connect-timeout 2 --max-time 5 -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"lang":"en","targetUrl":"rtmp://127.0.0.1:19351/live/burn","subs":"burn"}' \
  "http://$PRUKKA_HTTP/api/v1/sessions/video-demo/push" >/dev/null
echo "==> burn-in push started"

# The video rendition must be a real H.264 transport stream.
seg=$(curl -fsS --connect-timeout 2 --max-time 5 \
  "http://$PRUKKA_HTTP/video-demo/video/index.m3u8" | grep -m1 '\.ts$')
curl -fsS --connect-timeout 2 --max-time 10 \
  "http://$PRUKKA_HTTP/video-demo/video/$seg" -o "$PRUKKA_STATE/video-seg.ts"
"$FF" -hide_banner -v error -i "$PRUKKA_STATE/video-seg.ts" -f null - 2>"$PRUKKA_STATE/probe.err" || {
  echo "FAIL: video segment does not decode:"; cat "$PRUKKA_STATE/probe.err"; exit 1; }
echo "==> video segment decodes ($(wc -c <"$PRUKKA_STATE/video-seg.ts" | tr -d ' ') bytes)"

# Live subtitles: a part with translated text must appear (captions lag the
# video by provider latency plus the session delay D=8s).
echo "==> waiting for the English subtitle rendition…"
subs=""
for _ in $(seq 1 60); do
  for part in $(curl -fsS --connect-timeout 2 --max-time 5 \
    "http://$PRUKKA_HTTP/video-demo/subs/en/index.m3u8" 2>/dev/null | grep '\.vtt$' || true); do
    body=$(curl -fsS --connect-timeout 2 --max-time 5 \
      "http://$PRUKKA_HTTP/video-demo/subs/en/$part" 2>/dev/null || true)
    if echo "$body" | grep -q " --> "; then
      subs=yes
      break 2
    fi
  done
  sleep 0.5
done
[ -n "$subs" ] || { echo "FAIL: no subtitle cues in the HLS rendition"; tail -20 "$PRUKKA_STATE/daemon.log"; exit 1; }
echo "==> live subtitle part:"
echo "$body"

# Dubbed audio rendition: the playlist must list real segments.
audio=""
for _ in $(seq 1 40); do
  if curl -fsS --connect-timeout 2 --max-time 5 \
    "http://$PRUKKA_HTTP/video-demo/audio/en/index.m3u8" 2>/dev/null | grep -q '\.ts$'; then
    audio=yes
    break
  fi
  sleep 0.5
done
[ -n "$audio" ] || { echo "FAIL: no dubbed audio rendition"; tail -20 "$PRUKKA_STATE/daemon.log"; exit 1; }
echo "==> dubbed audio rendition is rolling"

# Wait for the push capture (bounded: the push ends with the video via
# -shortest), then prove the burn with pixels: the caption band (bottom
# 80 px) during the cue window must differ from before it.
for _ in $(seq 1 60); do
  kill -0 "$LISTENER" 2>/dev/null || break
  sleep 1
done
if demo_job_is_owned "$LISTENER"; then
  if ! demo_stop_owned_job "$LISTENER" burn-listener; then
    echo "FAIL: burn listener cleanup failed; state retained for recovery" >&2
    exit 1
  fi
  demo_forget_job "$LISTENER"
  echo "FAIL: burn listener exceeded 60 seconds" >&2
  exit 1
fi
if wait "$LISTENER"; then listener_status=0; else listener_status=$?; fi
demo_forget_job "$LISTENER"
LISTENER=""
[ "$listener_status" -eq 0 ] || { echo "FAIL: burn listener exited with status $listener_status"; exit 1; }
size=$(wc -c <"$FLV" | tr -d ' ')
[ "$size" -gt 100000 ] || { echo "FAIL: pushed flv too small ($size)"; tail -15 "$PRUKKA_STATE/daemon.log"; exit 1; }

"$FF" -hide_banner -v error -ss 5 -i "$FLV" -frames:v 1 -vf "crop=iw:80:0:ih-80" -y "$PRUKKA_STATE/in-cue.png"
"$FF" -hide_banner -v error -ss 1 -i "$FLV" -frames:v 1 -vf "crop=iw:80:0:ih-80" -y "$PRUKKA_STATE/no-cue.png"
if cmp -s "$PRUKKA_STATE/in-cue.png" "$PRUKKA_STATE/no-cue.png"; then
  echo "FAIL: caption band identical with and without a cue — burn-in did not draw"
  exit 1
fi
echo "==> burn-in verified: caption band differs during the cue window ($size bytes pushed)"

"$BIN" stats
echo "==> video demo PASS (play it: ffplay http://$PRUKKA_HTTP/video-demo/master.m3u8)"
