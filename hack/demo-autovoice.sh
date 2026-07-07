#!/usr/bin/env bash
# Auto-voice demo: two speakers with different
# registers talk in turn; the engine pitches each utterance, clusters the
# speakers and dubs each one with a distinct register-matched voice — no
# per-participant configuration. Verified from the dub log: two different
# voice ids.
# Needs an OpenRouter key, ffmpeg and macOS `say` for the two-speaker
# fixture; skips cleanly without.
set -eu
cd "$(dirname "$0")/.."

source hack/lib/demo.sh

command -v say >/dev/null || { echo "==> auto-voice demo SKIPPED (needs macOS say for the fixture)"; exit 0; }

demo_init "${PRUKKA_DEMO_PORT:-18091}"
demo_require_ffmpeg "auto-voice demo" || exit 0
S="$PRUKKA_STATE"

# Two-speaker fixture: a deep register (Grandpa) and a bright one (Alice)
# with a silence gap so the VAD endpoints two utterances.
say -v "Grandpa (Italiano (Italia))" -o "$S/a.aiff" "Buongiorno a tutti, oggi parliamo del progetto."
say -v "Alice" -o "$S/b.aiff" "Grazie mille, sono davvero felice di essere qui."
for f in a b; do
  "$FF" -hide_banner -loglevel error -i "$S/$f.aiff" -ar 16000 -ac 1 -y "$S/$f.wav"
done
"$FF" -hide_banner -loglevel error \
  -i "$S/a.wav" -f lavfi -t 1.2 -i "anullsrc=r=16000:cl=mono" -i "$S/b.wav" \
  -filter_complex "[0:a][1:a][2:a]concat=n=3:v=0:a=1" -ar 16000 -ac 1 -y "$S/duo.wav"
echo "==> generated two-speaker fixture"

demo_start_daemon debug

"$BIN" session add duo --in "file://$S/duo.wav" --langs en --source it
echo "==> session created (auto-voice is the default)"

for _ in $(seq 1 90); do
  if grep -q "lane unavailable" "$S/daemon.log"; then
    echo "==> auto-voice demo SKIPPED (no usable OpenRouter key)"
    exit 0
  fi

  [ "$(grep -c 'segment dubbed' "$S/daemon.log" || true)" -ge 2 ] && break
  sleep 0.5
done

voices=$(grep "segment dubbed" "$S/daemon.log" | grep -o '"voice":"[a-z]*"' | sort -u)
count=$(echo "$voices" | grep -c voice || true)
echo "==> dub voices used:"
echo "$voices"

if [ "$count" -lt 2 ]; then
  echo "FAIL: expected two distinct auto-assigned voices, got $count"
  grep "segment dubbed" "$S/daemon.log" | tail -4
  exit 1
fi

echo "==> auto-voice demo PASS: two speakers, two distinct register-matched voices"
