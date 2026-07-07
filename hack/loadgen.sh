#!/usr/bin/env bash
# Load gate: ten concurrent sessions × three target languages
# through the full pipeline (captions + dubbing) must keep the daemon's CPU
# under 60% of an 8-core budget. Needs an OpenRouter key and ffmpeg; skips
# cleanly without.
set -eu
cd "$(dirname "$0")/.."

source hack/lib/demo.sh

SESSIONS="${PRUKKA_LOAD_SESSIONS:-10}"
WAV="$PWD/hack/fixtures/speech-it.wav"

demo_init "${PRUKKA_DEMO_PORT:-18089}"
demo_require_ffmpeg "load gate" || exit 0
demo_start_daemon warn

for i in $(seq 1 "$SESSIONS"); do
  "$BIN" session add "load$i" --in "file://$WAV" --langs it,en,de --source it >/dev/null
done
echo "==> $SESSIONS sessions × 3 languages started"

if grep -q "lane unavailable" "$PRUKKA_STATE/daemon.log"; then
  echo "==> load gate SKIPPED (no usable OpenRouter key)"
  exit 0
fi

# Sample the DAEMON's CPU while the fixtures stream (8s) plus the provider
# tail. %cpu from ps is per-core: 100 = one full core.
peak=0
for _ in $(seq 1 20); do
  cpu=$(ps -o %cpu= -p "$DAEMON_PID" | tr -d ' ')
  cpu=${cpu%%.*}
  [ "${cpu:-0}" -gt "$peak" ] && peak=$cpu
  sleep 1
done

cores=$(getconf _NPROCESSORS_ONLN)
budget=$((8 * 60))   # 60% of eight cores, in per-core percent
echo "==> peak daemon CPU: ${peak}% of one core (host has $cores cores; budget ${budget}%)"

errors=$(grep -cE '"level":"ERROR"' "$PRUKKA_STATE/daemon.log" || true)
[ "$errors" -eq 0 ] || { echo "FAIL: $errors daemon errors under load"; tail -10 "$PRUKKA_STATE/daemon.log"; exit 1; }

if [ "$peak" -ge "$budget" ]; then
  echo "FAIL: peak CPU ${peak}% exceeds the budget of ${budget}%"
  exit 1
fi

"$BIN" stats
echo "==> load gate PASS"
