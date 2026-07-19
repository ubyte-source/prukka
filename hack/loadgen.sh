#!/usr/bin/env bash
# Load gate: simultaneously active Italian-to-English sessions must produce
# source and translated captions plus English dubbed audio. The daemon process
# tree must stay within a configurable share of the host's online CPU capacity.
set -euo pipefail
cd "$(dirname "$0")/.."

source hack/lib/demo.sh
source hack/lib/loadgen.sh

SESSIONS="${PRUKKA_LOAD_SESSIONS:-10}"
TIMEOUT_SECONDS="${PRUKKA_LOAD_TIMEOUT_SECONDS:-180}"
CPU_BUDGET_PERCENT="${PRUKKA_LOAD_CPU_BUDGET_PERCENT:-100}"
WAV="$PWD/hack/fixtures/speech-it.wav"

load_validate_sessions PRUKKA_LOAD_SESSIONS "$SESSIONS"
load_validate_maximum_integer PRUKKA_LOAD_TIMEOUT_SECONDS "$TIMEOUT_SECONDS" "$LOAD_MAX_SECONDS"
load_validate_percentage PRUKKA_LOAD_CPU_BUDGET_PERCENT "$CPU_BUDGET_PERCENT"
demo_init "${PRUKKA_DEMO_PORT:-18089}"
demo_require_engine "load gate" fail || exit 1
demo_require_ffmpeg "load gate" fail || exit 1
LOAD_CONFIG="$PRUKKA_STATE/load.yaml"
load_write_config "$LOAD_CONFIG" "$SESSIONS"
VOICE_PROBE_DIR="$PRUKKA_STATE/load-voice-probes"
mkdir -p "$VOICE_PROBE_DIR"
demo_start_daemon debug --config "$LOAD_CONFIG"

for i in $(seq 1 "$SESSIONS"); do
  "$BIN" --config "$LOAD_CONFIG" session add "load$i" --in "file://$WAV" --langs it,en \
    --dub-langs en --source it >/dev/null
done
echo "==> $SESSIONS sessions admitted with max_lanes=$SESSIONS (captions it/en, dubbed audio en)"

online_cores() {
  detected=$(getconf _NPROCESSORS_ONLN 2>/dev/null || true)
  case "$detected" in
    ''|*[!0-9]*|0) detected=$(sysctl -n hw.logicalcpu 2>/dev/null || true) ;;
  esac
  case "$detected" in
    ''|*[!0-9]*|0) detected=1 ;;
  esac
  echo "$detected"
}

# ps reports process-lifetime average CPU, where 100 is one fully used core.
# Each sample sums the daemon and its current descendants from one snapshot.
process_tree_cpu() {
  LC_ALL=C ps -axo pid=,ppid=,%cpu= | awk -v root="$DAEMON_PID" '
    { parent[$1] = $2; cpu[$1] = $3 }
    function below_root(pid, cursor, hops) {
      cursor = pid
      for (hops = 0; hops < 1024; hops++) {
        if (cursor == root) return 1
        if (!(cursor in parent) || parent[cursor] == cursor) return 0
        cursor = parent[cursor]
      }
      return 0
    }
    END {
      total = 0
      for (pid in cpu) if (below_root(pid)) total += cpu[pid]
      print int(total + 0.999999)
    }
  '
}

session_outputs_ready() {
  load_lane_outputs_ready "http://$PRUKKA_HTTP" "$PRUKKA_STATE/daemon.log" \
    "$1" it en "$FF" "$VOICE_PROBE_DIR"
}

missing_outputs() {
  load_missing_lane_outputs "http://$PRUKKA_HTTP" "$PRUKKA_STATE/daemon.log" \
    "$1" it en "$FF" "$VOICE_PROBE_DIR"
}

peak=0
peak_running=0
ready=0
deadline=$((SECONDS + TIMEOUT_SECONDS))
while [ "$SECONDS" -lt "$deadline" ]; do
  if ! demo_job_is_owned "$DAEMON_PID"; then
    echo "FAIL: daemon exited during the load gate"
    tail -20 "$PRUKKA_STATE/daemon.log"
    exit 1
  fi

  cpu=$(process_tree_cpu)
  [ "${cpu:-0}" -gt "$peak" ] && peak=$cpu

  running=$(curl -fsS --connect-timeout 1 --max-time 1 \
    "http://$PRUKKA_HTTP/metrics" 2>/dev/null | \
    load_session_state_count running) || running=0
  [ "${running:-0}" -gt "$peak_running" ] && peak_running=$running

  if grep -q "lane unavailable" "$PRUKKA_STATE/daemon.log"; then
    reason=$(grep "lane unavailable" "$PRUKKA_STATE/daemon.log" | tail -1)
    echo "FAIL: load lane unavailable: $reason"
    exit 1
  fi

  errors=$(grep -cE '"level":"ERROR"' "$PRUKKA_STATE/daemon.log" || true)
  if [ "$errors" -ne 0 ]; then
    echo "FAIL: $errors daemon errors under load"
    tail -10 "$PRUKKA_STATE/daemon.log"
    exit 1
  fi

  ready=0
  for i in $(seq 1 "$SESSIONS"); do
    [ "$SECONDS" -ge "$deadline" ] && break
    session_outputs_ready "load$i" && ready=$((ready + 1))
  done
  [ "$ready" -eq "$SESSIONS" ] && break

  [ "$SECONDS" -ge "$deadline" ] || sleep 1
done

cores=$(online_cores)
budget=$((cores * CPU_BUDGET_PERCENT))
echo "==> highest sampled process-lifetime average daemon-tree CPU: ${peak}% (host: $cores cores; budget: ${budget}%)"
echo "==> highest observed simultaneous running sessions: $peak_running/$SESSIONS"

if [ "$ready" -ne "$SESSIONS" ]; then
  echo "FAIL: only $ready/$SESSIONS sessions produced both caption tracks and dubbed audio"
  for i in $(seq 1 "$SESSIONS"); do
    session_outputs_ready "load$i" || echo "  load$i: $(missing_outputs "load$i")"
  done
  tail -20 "$PRUKKA_STATE/daemon.log"
  exit 1
fi

if [ "$peak_running" -lt "$SESSIONS" ]; then
  echo "FAIL: only $peak_running/$SESSIONS sessions were observed running simultaneously"
  tail -20 "$PRUKKA_STATE/daemon.log"
  exit 1
fi

if [ "$peak" -gt "$budget" ]; then
  echo "FAIL: sampled process-lifetime average process-tree CPU ${peak}% exceeds the budget of ${budget}%"
  exit 1
fi

"$BIN" --config "$LOAD_CONFIG" stats
echo "==> load gate PASS ($ready/$SESSIONS outputs; $peak_running/$SESSIONS simultaneous lanes)"
