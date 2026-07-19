#!/usr/bin/env bash
# PGO capture: profile the daemon under the load-gate workload and refresh
# cmd/prukka/default.pgo, which `make build` and the release pick up
# automatically. Needs a real local speech engine and ffmpeg; the deterministic
# protocol double is intentionally not a representative profiling workload.
set -euo pipefail
cd "$(dirname "$0")/.."

source hack/lib/demo.sh
source hack/lib/loadgen.sh
source hack/lib/pgo-provenance.sh

SESSIONS="${PRUKKA_PGO_SESSIONS:-4}"
PROFILE_SECS="${PRUKKA_PGO_SECONDS:-30}"
READY_SECONDS="${PRUKKA_PGO_READY_SECONDS:-180}"
SOURCE_WAV="$PWD/hack/fixtures/speech-it.wav"
PPROF_REQUEST="${PRUKKA_PGO_PPROF:-127.0.0.1:0}"

load_validate_sessions PRUKKA_PGO_SESSIONS "$SESSIONS"
load_validate_maximum_integer PRUKKA_PGO_SECONDS "$PROFILE_SECS" "$LOAD_MAX_SECONDS"
load_validate_maximum_integer PRUKKA_PGO_READY_SECONDS "$READY_SECONDS" "$LOAD_MAX_SECONDS"

demo_init "${PRUKKA_DEMO_PORT:-18091}"
staged=""
staged_provenance=""
profile_backup=""
provenance_backup=""
profile_activated=0
provenance_activated=0
pgo_committed=0
pgo_exit_cleanup() {
  local original_status=$? cleanup_status=0 status

  trap - EXIT
  if [ -n "$staged" ]; then
    rm -f "$staged" || cleanup_status=$?
  fi
  if [ -n "$staged_provenance" ]; then
    rm -f "$staged_provenance" || cleanup_status=$?
  fi
  if [ "$pgo_committed" -eq 0 ]; then
    if [ "$profile_activated" -eq 1 ]; then
      rm -f cmd/prukka/default.pgo || cleanup_status=$?
    fi
    if [ "$provenance_activated" -eq 1 ]; then
      rm -f cmd/prukka/default.pgo.provenance || cleanup_status=$?
    fi
    if [ -n "$profile_backup" ]; then
      mv "$profile_backup" cmd/prukka/default.pgo || cleanup_status=$?
      profile_backup=""
    fi
    if [ -n "$provenance_backup" ]; then
      mv "$provenance_backup" cmd/prukka/default.pgo.provenance || cleanup_status=$?
      provenance_backup=""
    fi
  fi
  [ -z "$profile_backup" ] || rm -f "$profile_backup" || cleanup_status=$?
  [ -z "$provenance_backup" ] || rm -f "$provenance_backup" || cleanup_status=$?
  demo_cleanup || {
    status=$?
    [ "$cleanup_status" -ne 0 ] || cleanup_status=$status
  }
  if [ "$original_status" -ne 0 ]; then
    exit "$original_status"
  fi
  exit "$cleanup_status"
}
trap pgo_exit_cleanup EXIT

if [ -z "${PRUKKA_PGO_ENGINE:-}" ]; then
  echo "FAIL: set PRUKKA_PGO_ENGINE to the real speech-engine executable" >&2
  exit 1
fi
PRUKKA_DEMO_ENGINE="$PRUKKA_PGO_ENGINE"
export PRUKKA_DEMO_ENGINE
demo_require_engine "pgo capture" fail || exit 1
if LC_ALL=C grep -a -q 'usage: protocol-engine' "$PRUKKA_ENGINE_BIN"; then
  echo "FAIL: pgo capture requires the real speech engine, not protocol-engine"
  exit 1
fi
demo_require_ffmpeg "pgo capture" fail || exit 1
MIN_WAV_SECONDS=$((READY_SECONDS + PROFILE_SECS + 30))
WAV_SECONDS="${PRUKKA_PGO_WAV_SECONDS:-$MIN_WAV_SECONDS}"
load_validate_minimum_integer PRUKKA_PGO_WAV_SECONDS "$WAV_SECONDS" "$MIN_WAV_SECONDS"
WAV="$PRUKKA_STATE/pgo-workload.wav"
"$FF" -nostdin -hide_banner -loglevel error -stream_loop -1 -i "$SOURCE_WAV" \
  -t "$WAV_SECONDS" -ar 16000 -ac 1 -c:a pcm_s16le "$WAV"
LOAD_CONFIG="$PRUKKA_STATE/pgo.yaml"
load_write_config "$LOAD_CONFIG" "$SESSIONS"
VOICE_PROBE_DIR="$PRUKKA_STATE/pgo-voice-probes"
mkdir -p "$VOICE_PROBE_DIR"
demo_start_daemon debug --pprof "$PPROF_REQUEST" --config "$LOAD_CONFIG"

pprof_addr=""
pprof_deadline=$((SECONDS + 5))
while [ "$SECONDS" -lt "$pprof_deadline" ]; do
  pprof_addr=$(sed -n \
    's/.*"msg":"pprof profiling server started (loopback only)".*"addr":"\([^"]*\)".*/\1/p' \
    "$PRUKKA_STATE/daemon.log" | tail -1)
  [ -z "$pprof_addr" ] || break
  sleep 0.1
done
if [ -z "$pprof_addr" ]; then
  echo "FAIL: pprof did not report its bound loopback address" >&2
  tail -20 "$PRUKKA_STATE/daemon.log" >&2
  exit 1
fi

pprof_cmdline="$PRUKKA_STATE/pprof-cmdline"
curl -fsS --connect-timeout 1 --max-time 2 \
  "http://$pprof_addr/debug/pprof/cmdline" -o "$pprof_cmdline"
if ! tr '\0' '\n' <"$pprof_cmdline" | grep -Fx -- "$LOAD_CONFIG" >/dev/null; then
  echo "FAIL: pprof endpoint does not belong to this daemon" >&2
  exit 1
fi

for i in $(seq 1 "$SESSIONS"); do
  "$BIN" --config "$LOAD_CONFIG" session add "pgo$i" --in "file://$WAV" \
    --langs it,en --dub-langs en --source it >/dev/null
done
echo "==> $SESSIONS sessions admitted with max_lanes=$SESSIONS"

ready=0
peak_running=0
ready_lanes=0
ready_deadline=$((SECONDS + READY_SECONDS))
while [ "$SECONDS" -lt "$ready_deadline" ]; do
  if ! demo_job_is_owned "$DAEMON_PID"; then
    echo "FAIL: daemon exited while preparing the pgo workload"
    tail -20 "$PRUKKA_STATE/daemon.log"
    exit 1
  fi

  if grep -q "lane unavailable" "$PRUKKA_STATE/daemon.log"; then
    reason=$(grep "lane unavailable" "$PRUKKA_STATE/daemon.log" | tail -1)
    echo "FAIL: pgo lane unavailable: $reason"
    exit 1
  fi

  running=$(curl -fsS --connect-timeout 1 --max-time 1 \
    "http://$PRUKKA_HTTP/metrics" 2>/dev/null | \
    load_session_state_count running) || running=0
  [ "${running:-0}" -gt "$peak_running" ] && peak_running=$running

  ready_lanes=0
  for i in $(seq 1 "$SESSIONS"); do
    [ "$SECONDS" -ge "$ready_deadline" ] && break
    if load_lane_outputs_ready "http://$PRUKKA_HTTP" "$PRUKKA_STATE/daemon.log" \
      "pgo$i" it en "$FF" "$VOICE_PROBE_DIR"; then
      ready_lanes=$((ready_lanes + 1))
    fi
  done

  if [ "${running:-0}" -eq "$SESSIONS" ] && [ "$ready_lanes" -eq "$SESSIONS" ]; then
    ready=1
    break
  fi
  [ "$SECONDS" -ge "$ready_deadline" ] || sleep 0.5
done

if [ "$ready" -ne 1 ]; then
  echo "FAIL: pgo workload reached $peak_running/$SESSIONS simultaneous lanes and $ready_lanes/$SESSIONS complete outputs"
  for i in $(seq 1 "$SESSIONS"); do
    if ! load_lane_outputs_ready "http://$PRUKKA_HTTP" "$PRUKKA_STATE/daemon.log" \
      "pgo$i" it en "$FF" "$VOICE_PROBE_DIR"; then
      missing=$(load_missing_lane_outputs "http://$PRUKKA_HTTP" \
        "$PRUKKA_STATE/daemon.log" "pgo$i" it en "$FF" "$VOICE_PROBE_DIR")
      echo "  pgo$i: $missing"
    fi
  done
  tail -20 "$PRUKKA_STATE/daemon.log"
  exit 1
fi

echo "==> capturing a ${PROFILE_SECS}s CPU profile after observing $peak_running/$SESSIONS simultaneous lanes"
tmp="$PRUKKA_STATE/cpu.pgo"
curl -fsS --connect-timeout 2 --max-time "$((PROFILE_SECS + 10))" \
  "http://$pprof_addr/debug/pprof/profile?seconds=$PROFILE_SECS" -o "$tmp"

if ! demo_job_is_owned "$DAEMON_PID"; then
  echo "FAIL: daemon exited during pgo capture"
  tail -20 "$PRUKKA_STATE/daemon.log"
  exit 1
fi

running_after=$(curl -fsS --connect-timeout 1 --max-time 1 \
  "http://$PRUKKA_HTTP/metrics" 2>/dev/null | \
  load_session_state_count running) || running_after=-1
if [ "$running_after" -ne "$SESSIONS" ]; then
  echo "FAIL: running lanes changed during pgo capture ($SESSIONS before, $running_after after)"
  tail -20 "$PRUKKA_STATE/daemon.log"
  exit 1
fi

if grep -q "lane unavailable" "$PRUKKA_STATE/daemon.log"; then
  reason=$(grep "lane unavailable" "$PRUKKA_STATE/daemon.log" | tail -1)
  echo "FAIL: pgo lane unavailable during capture: $reason"
  exit 1
fi

# A truncated or empty capture must never replace the committed profile.
profile_top=$(go tool pprof -top -nodecount=0 -nodefraction=0 "$tmp")
if ! grep -Eq 'github.com/ubyte-source/prukka/internal/(core/pipeline|media/egress/audio)' <<<"$profile_top"; then
  echo "FAIL: pgo profile contains no frame-path samples"
  exit 1
fi
if ! grep -Eq 'pipeline\.\(\*Mixer\)\.PullInto|pipeline\.AppendS16LE' <<<"$profile_top"; then
  echo "FAIL: pgo profile contains no current zero-allocation frame-path symbols"
  exit 1
fi
if grep -Eq 'providers/(cartesia|openrouter)|providers/helpers/(retry|breaker)' <<<"$profile_top"; then
  echo "FAIL: pgo profile contains retired provider symbols"
  exit 1
fi

staged=$(mktemp cmd/prukka/default.pgo.XXXXXX)
staged_provenance=$(mktemp cmd/prukka/default.pgo.provenance.XXXXXX)
cp "$tmp" "$staged"
chmod 0644 "$staged"
{
  printf 'format=1\n'
  printf 'source_sha256=%s\n' "$(pgo_source_fingerprint)"
  printf 'profile_sha256=%s\n' "$(pgo_sha256_file "$tmp")"
  printf 'engine_sha256=%s\n' "$(pgo_sha256_file "$PRUKKA_ENGINE_BIN")"
  printf 'ffmpeg_sha256=%s\n' "$(pgo_sha256_file "$FF")"
  printf 'fixture_sha256=%s\n' "$(pgo_sha256_file "$SOURCE_WAV")"
  printf 'go_version=%s\n' "$(go env GOVERSION)"
  printf 'sessions=%s\n' "$SESSIONS"
  printf 'profile_seconds=%s\n' "$PROFILE_SECS"
} >"$staged_provenance"
chmod 0644 "$staged_provenance"
if [ -e cmd/prukka/default.pgo ]; then
  profile_backup=$(mktemp cmd/prukka/default.pgo.backup.XXXXXX)
  rm -f "$profile_backup"
  mv cmd/prukka/default.pgo "$profile_backup"
fi
if [ -e cmd/prukka/default.pgo.provenance ]; then
  provenance_backup=$(mktemp cmd/prukka/default.pgo.provenance.backup.XXXXXX)
  rm -f "$provenance_backup"
  mv cmd/prukka/default.pgo.provenance "$provenance_backup"
fi
mv "$staged" cmd/prukka/default.pgo
staged=""
profile_activated=1
mv "$staged_provenance" cmd/prukka/default.pgo.provenance
staged_provenance=""
provenance_activated=1
hack/ci/pgo-profile-gate.sh
pgo_committed=1
[ -z "$profile_backup" ] || rm -f "$profile_backup"
[ -z "$provenance_backup" ] || rm -f "$provenance_backup"
profile_backup=""
provenance_backup=""
echo "==> wrote cmd/prukka/default.pgo ($(wc -c <cmd/prukka/default.pgo | tr -d ' ') bytes) with provenance"
