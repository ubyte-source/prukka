#!/usr/bin/env bash
# Deterministic fixtures for the isolated load configuration and metric probe.
set -euo pipefail
cd "$(dirname "$0")/../.."

source hack/lib/loadgen.sh

load_validate_positive_integer VALUE 1
if load_validate_positive_integer VALUE 01 2>/dev/null; then
  echo "positive integer validation accepted a leading zero" >&2
  exit 1
fi
load_validate_maximum_integer TIMEOUT 86400 86400
if load_validate_maximum_integer TIMEOUT 86401 86400 2>/dev/null; then
  echo "maximum duration validation accepted an unbounded deadline" >&2
  exit 1
fi
load_validate_minimum_integer WAV_SECONDS 240 240
if load_validate_minimum_integer WAV_SECONDS 239 240 2>/dev/null; then
  echo "minimum duration validation accepted a short workload" >&2
  exit 1
fi
load_validate_percentage PRUKKA_LOAD_CPU_BUDGET_PERCENT 100
if load_validate_percentage PRUKKA_LOAD_CPU_BUDGET_PERCENT 101 2>/dev/null; then
  echo "CPU budget validation accepted a percentage above 100" >&2
  exit 1
fi
load_validate_sessions PRUKKA_LOAD_SESSIONS 10
if load_validate_sessions PRUKKA_LOAD_SESSIONS 0 2>/dev/null; then
  echo "load session validation accepted zero" >&2
  exit 1
fi
if load_validate_sessions PRUKKA_LOAD_SESSIONS 65 2>/dev/null; then
  echo "load session validation exceeded the daemon limit" >&2
  exit 1
fi

fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
config=$fixture/load.yaml
load_write_config "$config" 10

expected=$(printf 'providers:\n  dispatch:\n    max_lanes: 10\n    max_sessions: 10\ndefaults:\n  bed: off\n  delay: 0s')
[[ $(cat "$config") == "$expected" ]]

metrics='# HELP prukka_sessions_by_state Number of sessions.
prukka_sessions_by_state{state="starting"} 0
prukka_sessions_by_state{state="running"} 10
prukka_sessions_by_state{state="finished"} 0'
[[ $(printf '%s\n' "$metrics" | load_session_state_count running) == 10 ]]
[[ $(printf '%s\n' "$metrics" | load_session_state_count failed) == 0 ]]

mkdir -p "$fixture/bin"
cat >"$fixture/bin/curl" <<'EOF'
#!/usr/bin/env bash
url="" connect_timeout="" max_time=""
for arg in "$@"; do
  case "$arg" in http://*) url=$arg ;; esac
done
for ((i = 1; i <= $#; i++)); do
  case "${!i}" in
    --connect-timeout) next=$((i + 1)); connect_timeout=${!next} ;;
    --max-time) next=$((i + 1)); max_time=${!next} ;;
  esac
done
[ "$connect_timeout" = 1 ] || exit 64
case "$url" in
  *.ts) [ "$max_time" = 2 ] || exit 64 ;;
  *) [ "$max_time" = 1 ] || exit 64 ;;
esac
case "$url" in
  */lane1/it/subs.vtt|*/lane1/en/subs.vtt|*/lane2/it/subs.vtt|*/lane3/it/subs.vtt|*/lane3/en/subs.vtt)
    printf 'WEBVTT\n\n00:00.000 --> 00:01.000\ntext\n'
    ;;
  */lane1/audio/en/index.m3u8|*/lane3/audio/en/index.m3u8)
    printf '#EXTM3U\nseg00001.ts\n'
    ;;
  */lane1/audio/en/seg00001.ts) printf 'VOICE' ;;
  */lane3/audio/en/seg00001.ts) printf 'SILENCE' ;;
  *) exit 22 ;;
esac
EOF
chmod 700 "$fixture/bin/curl"
cat >"$fixture/bin/ffmpeg" <<'EOF'
#!/usr/bin/env bash
input=$(cat)
if [ "$input" = VOICE ]; then
  printf '\240\017'
else
  printf '\000\000'
fi
EOF
chmod 700 "$fixture/bin/ffmpeg"
PATH="$fixture/bin:$PATH"

log=$fixture/daemon.log
printf '%s\n' \
  '{"msg":"voiced","session":"lane1","target":"en"}' \
  '{"msg":"voiced","session":"lane3","target":"en"}' >"$log"
probe_dir=$fixture/probes
mkdir -p "$probe_dir"
load_lane_outputs_ready http://127.0.0.1:8080 "$log" lane1 it en \
  "$fixture/bin/ffmpeg" "$probe_dir"
if load_lane_outputs_ready http://127.0.0.1:8080 "$log" lane2 it en \
  "$fixture/bin/ffmpeg" "$probe_dir"; then
  echo "lane readiness accepted incomplete per-lane output" >&2
  exit 1
fi
[[ $(load_missing_lane_outputs http://127.0.0.1:8080 "$log" lane2 it en \
  "$fixture/bin/ffmpeg" "$probe_dir") == \
  "en-caption en-audio-segment en-voiced-take en-voiced-audio" ]]
if load_lane_outputs_ready http://127.0.0.1:8080 "$log" lane3 it en \
  "$fixture/bin/ffmpeg" "$probe_dir"; then
  echo "lane readiness accepted a silent segment after the voiced event" >&2
  exit 1
fi
[[ $(load_missing_lane_outputs http://127.0.0.1:8080 "$log" lane3 it en \
  "$fixture/bin/ffmpeg" "$probe_dir") == "en-voiced-audio" ]]

set +e
pgo_error=$(PRUKKA_PGO_ENGINE=/usr/bin/true PRUKKA_DEMO_FFMPEG=/usr/bin/true \
  PRUKKA_PGO_SECONDS=2 PRUKKA_PGO_READY_SECONDS=3 PRUKKA_PGO_WAV_SECONDS=34 \
  hack/pgo.sh 2>&1)
pgo_status=$?
set -e
if [ "$pgo_status" -ne 2 ] ||
  ! grep -Fq 'PRUKKA_PGO_WAV_SECONDS must be at least 35' <<<"$pgo_error"; then
  echo "pgo gate did not reject a workload shorter than readiness plus capture" >&2
  printf '%s\n' "$pgo_error" >&2
  exit 1
fi

echo "load gate fixture: PASS"
