# Shared demo plumbing, sourced by every hack/demo-*.sh: one place for the
# state dir, external ffmpeg PATH, daemon boot, health and cleanup.
#
# Usage:
#   source "$(dirname "$0")/lib/demo.sh"
#   demo_init 18097                    # state dir, PRUKKA_* env, cleanup trap
#   demo_require_ffmpeg "my demo" ||  exit 0   # skip cleanly without ffmpeg
#   demo_start_daemon debug            # boot + wait healthy
#   …
#   demo_require_engine "my demo" || exit 0 # skip without a local engine

BIN=bin/prukka
DAEMON_PID=""
DAEMON_TREE_SNAPSHOT=""
DEMO_AUX_PIDS=()
DEMO_AUX_LABELS=()

demo_init() {
  demo_reset_daemon
  DEMO_AUX_PIDS=()
  DEMO_AUX_LABELS=()
  PRUKKA_STATE="$(mktemp -d)"
  export PRUKKA_STATE
  export PRUKKA_HTTP="127.0.0.1:$1"

  trap demo_exit_cleanup EXIT
}

demo_register_job() {
  local pid=$1 label=${2:-auxiliary-job}

  if ! demo_job_is_owned "$pid"; then
    echo "FAIL: cannot register unowned $label PID $pid" >&2
    return 1
  fi
  DEMO_AUX_PIDS+=("$pid")
  DEMO_AUX_LABELS+=("$label")
}

demo_forget_job() {
  local wanted=$1 i

  for i in "${!DEMO_AUX_PIDS[@]}"; do
    if [ "${DEMO_AUX_PIDS[$i]}" = "$wanted" ]; then
      unset 'DEMO_AUX_PIDS[i]'
      unset 'DEMO_AUX_LABELS[i]'
    fi
  done
}

demo_reset_daemon() {
  DAEMON_PID=""
  DAEMON_TREE_SNAPSHOT=""
}

demo_job_is_owned() {
  local wanted=$1 job

  case "$wanted" in
    ''|*[!0-9]*) return 1 ;;
  esac
  while read -r job; do
    [ "$job" = "$wanted" ] && return 0
  done < <({ jobs -r -p; jobs -s -p; } 2>/dev/null)
  return 1
}

# Output is "depth pid pgid state" for root and its recursive PPID tree.
demo_owned_tree_snapshot() {
  local root=$1 table

  table=$(LC_ALL=C ps -axo pid=,ppid=,pgid=,stat= 2>/dev/null) || return 2
  LC_ALL=C awk -v root="$root" '
    $1 ~ /^[0-9]+$/ && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ {
      parent[$1] = $2; group[$1] = $3; state[$1] = $4
    }
    function depth(pid, cursor, n, hops) {
      cursor = pid; n = 0
      for (hops = 0; hops < 4096; hops++) {
        if (cursor == root) return n
        if (!(cursor in parent) || parent[cursor] == cursor) return -1
        cursor = parent[cursor]; n++
      }
      return -1
    }
    END {
      if (!(root in parent)) exit 2
      for (pid in parent) {
        n = depth(pid)
        if (n >= 0) print n, pid, group[pid], state[pid]
      }
    }
  ' <<<"$table"
}

demo_present_snapshot_pids() {
  local snapshot=$1 table

  table=$(LC_ALL=C ps -axo pid= 2>/dev/null) || return 2
  {
    printf '%s\n' "$snapshot"
    printf '%s\n' __PRUKKA_PROCESS_TABLE__
    printf '%s\n' "$table"
  } | LC_ALL=C awk '
    $0 == "__PRUKKA_PROCESS_TABLE__" { reading_table = 1; next }
    !reading_table { if ($2 ~ /^[0-9]+$/) expected[$2] = 1; next }
    $1 in expected { present[$1] = 1 }
    END { for (pid in present) print pid }
  '
}

# demo_await_snapshot_exit polls until every recorded PID has left the
# process table or the deadline passes, then prints whatever remains. A
# TERM'd tree needs a scheduling beat to unwind on a loaded runner; an
# instant single-shot check misreads that beat as a leak, while a genuine
# survivor is still reported once the bounded wait runs out.
demo_await_snapshot_exit() {
  local snapshot=$1 wait_seconds=$2 deadline present
  deadline=$((SECONDS + wait_seconds))
  while :; do
    present=$(demo_present_snapshot_pids "$snapshot") || return 2
    [ -z "$present" ] && break
    [ "$SECONDS" -lt "$deadline" ] || break
    sleep 0.05
  done
  printf '%s' "$present"
}

demo_stop_daemon() {
  local pid=${DAEMON_PID:-} deadline snapshot present
  local stop_seconds=${PRUKKA_DEMO_STOP_SECONDS:-5}

  case "$stop_seconds" in
    ''|*[!0-9]*|0*) stop_seconds=5 ;;
  esac
  if [ "${#stop_seconds}" -gt 2 ] || [ "$stop_seconds" -gt 30 ]; then
    stop_seconds=30
  fi
  [ -n "$pid" ] || return 0

  if ! demo_job_is_owned "$pid"; then
    if kill -0 "$pid" 2>/dev/null; then
      echo "FAIL: daemon PID $pid is live but is not owned by this shell" >&2
      return 1
    fi
    if [ -n "$DAEMON_TREE_SNAPSHOT" ]; then
      present=$(demo_await_snapshot_exit "$DAEMON_TREE_SNAPSHOT" "$stop_seconds") || {
        echo "FAIL: cannot verify recorded daemon-tree exit" >&2
        return 1
      }
      if [ -n "$present" ]; then
        echo "FAIL: recorded daemon-tree PIDs remain: $(echo "$present" | tr '\n' ' ')" >&2
        return 1
      fi
    fi
    wait "$pid" 2>/dev/null || true
    demo_reset_daemon
    return 0
  fi

  snapshot=$(demo_owned_tree_snapshot "$pid") || {
    echo "FAIL: cannot snapshot daemon tree before TERM; refusing unsafe cleanup" >&2
    return 1
  }
  DAEMON_TREE_SNAPSHOT=$snapshot
  if demo_job_is_owned "$pid"; then
    if ! builtin kill -s TERM "$pid" 2>/dev/null && demo_job_is_owned "$pid"; then
      echo "FAIL: cannot send TERM to owned daemon root $pid" >&2
      return 1
    fi
  fi

  deadline=$((SECONDS + stop_seconds))
  while demo_job_is_owned "$pid" && [ "$SECONDS" -lt "$deadline" ]; do
    sleep 0.05
  done
  if demo_job_is_owned "$pid"; then
    echo "FAIL: daemon root $pid did not exit within ${stop_seconds}s; refusing unsafe KILL" >&2
    return 1
  fi
  wait "$pid" 2>/dev/null || true

  present=$(demo_await_snapshot_exit "$snapshot" "$stop_seconds") || {
    echo "FAIL: cannot verify daemon-tree exit after TERM" >&2
    return 1
  }
  if [ -n "$present" ]; then
    echo "FAIL: daemon-tree PIDs remain after TERM: $(echo "$present" | tr '\n' ' ')" >&2
    return 1
  fi
  demo_reset_daemon
}

demo_stop_owned_job() {
  local pid=$1 label=${2:-auxiliary-job}
  local stop_seconds=${PRUKKA_DEMO_STOP_SECONDS:-5}
  local deadline snapshot present descendants

  case "$pid" in
    ''|*[!0-9]*) return 0 ;;
  esac
  case "$stop_seconds" in
    ''|*[!0-9]*|0*) stop_seconds=5 ;;
  esac
  if [ "${#stop_seconds}" -gt 2 ] || [ "$stop_seconds" -gt 30 ]; then
    stop_seconds=30
  fi

  if ! demo_job_is_owned "$pid"; then
    if kill -0 "$pid" 2>/dev/null; then
      echo "FAIL: $label PID $pid is live but is not owned by this shell" >&2
      return 1
    fi
    wait "$pid" 2>/dev/null || true
    return 0
  fi

  snapshot=$(demo_owned_tree_snapshot "$pid") || {
    echo "FAIL: cannot snapshot $label tree before TERM; refusing unsafe cleanup" >&2
    return 1
  }
  if ! builtin kill -s TERM "$pid" 2>/dev/null && demo_job_is_owned "$pid"; then
    echo "FAIL: cannot send TERM to owned $label root $pid" >&2
    return 1
  fi

  deadline=$((SECONDS + stop_seconds))
  while demo_job_is_owned "$pid" && [ "$SECONDS" -lt "$deadline" ]; do
    sleep 0.05
  done
  if demo_job_is_owned "$pid"; then
    descendants=$(printf '%s\n' "$snapshot" | awk '$1 > 0 { p = p " " $2 } END { sub(/^ /, "", p); print p }')
    echo "FAIL: $label root $pid did not exit within ${stop_seconds}s; refusing unsafe KILL" >&2
    echo "FAIL: recovery identifiers: root=$pid descendants=${descendants:-<none>}" >&2
    return 1
  fi
  wait "$pid" 2>/dev/null || true

  present=$(demo_present_snapshot_pids "$snapshot") || {
    echo "FAIL: cannot verify $label tree exit after TERM" >&2
    return 1
  }
  if [ -n "$present" ]; then
    echo "FAIL: $label tree PIDs remain after TERM: $(echo "$present" | tr '\n' ' ')" >&2
    return 1
  fi
}

demo_cleanup() {
  local state=${PRUKKA_STATE:-} stop_status=0 aux_status=0
  local root=${DAEMON_PID:-} descendants i

  for i in "${!DEMO_AUX_PIDS[@]}"; do
    demo_stop_owned_job "${DEMO_AUX_PIDS[$i]}" "${DEMO_AUX_LABELS[$i]}" || aux_status=$?
  done
  demo_stop_daemon || stop_status=$?
  if [ "$stop_status" -ne 0 ] || [ "$aux_status" -ne 0 ]; then
    descendants=$(printf '%s\n' "$DAEMON_TREE_SNAPSHOT" | awk '$1 > 0 { p = p " " $2 } END { sub(/^ /, "", p); print p }')
    echo "FAIL: demo cleanup incomplete; state preserved at ${state:-<unset>}" >&2
    echo "FAIL: recovery identifiers: root=${root:-<unset>} descendants=${descendants:-<unavailable>}" >&2
    [ "$stop_status" -ne 0 ] && return "$stop_status"
    return "$aux_status"
  fi
  DEMO_AUX_PIDS=()
  DEMO_AUX_LABELS=()
  [ -z "$state" ] || rm -rf -- "$state"
}

demo_exit_cleanup() {
  local original_status=$? cleanup_status=0

  trap - EXIT
  demo_cleanup || cleanup_status=$?
  if [ "$original_status" -ne 0 ]; then
    exit "$original_status"
  fi
  exit "$cleanup_status"
}

# demo_require_ffmpeg resolves ffmpeg (PATH or PRUKKA_DEMO_FFMPEG), exposes it
# through a demo-only PATH directory and exports FF; false means skip.
demo_require_ffmpeg() {
  local mode=${2:-skip}
  FF="${PRUKKA_DEMO_FFMPEG:-$(command -v ffmpeg || true)}"

  if [ -z "$FF" ]; then
    if [ "$mode" = fail ]; then
      echo "FAIL: $1 requires ffmpeg; run \`prukka setup\` and set PRUKKA_DEMO_FFMPEG" >&2
    else
      echo "==> $1 SKIPPED (no ffmpeg; run \`prukka setup\` and set PRUKKA_DEMO_FFMPEG)"
    fi
    return 1
  fi

  demo_expose_ffmpeg "$FF"
}

demo_expose_ffmpeg() {
  FF=$1
  demo_bin="$PRUKKA_STATE/demo-bin"
  mkdir -p "$demo_bin"
  ln -sf "$FF" "$demo_bin/ffmpeg"
  PATH="$demo_bin:$PATH"
  export PATH
}

# demo_require_engine exposes the explicit local speech-engine executable to
# the daemon. The CI protocol double and a real engine both speak this contract.
demo_require_engine() {
  local mode=${2:-skip}
  if [ -z "${PRUKKA_DEMO_ENGINE:-}" ]; then
    if [ "$mode" = fail ]; then
      echo "FAIL: $1 requires PRUKKA_DEMO_ENGINE to name a local speech-engine executable" >&2
    else
      echo "==> $1 SKIPPED (set PRUKKA_DEMO_ENGINE to a local speech-engine executable)"
    fi
    return 1
  fi
  if [ ! -x "$PRUKKA_DEMO_ENGINE" ]; then
    echo "FAIL: PRUKKA_DEMO_ENGINE is not executable: $PRUKKA_DEMO_ENGINE" >&2
    return 1
  fi

  PRUKKA_ENGINE_BIN="$PRUKKA_DEMO_ENGINE"
  export PRUKKA_ENGINE_BIN
}

demo_start_daemon() {
  local level="${1:-warn}" deadline
  if [ "$#" -gt 0 ]; then shift; fi

  "$BIN" daemon --log-level "$level" "$@" >"$PRUKKA_STATE/daemon.log" 2>&1 &
  DAEMON_PID=$!

  deadline=$((SECONDS + 10))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if ! demo_job_is_owned "$DAEMON_PID"; then
      echo "FAIL: daemon exited before becoming healthy" >&2
      tail -20 "$PRUKKA_STATE/daemon.log" >&2
      return 1
    fi
    if curl -fsS --connect-timeout 1 --max-time 1 \
      "http://$PRUKKA_HTTP/healthz" >/dev/null 2>&1; then
      echo "==> daemon healthy on $PRUKKA_HTTP"
      return 0
    fi
    sleep 0.2
  done

  echo "FAIL: daemon did not become healthy within 10 seconds" >&2
  tail -20 "$PRUKKA_STATE/daemon.log" >&2
  return 1
}
