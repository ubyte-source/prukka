#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  cleanup-orphan-parent)
    trap 'printf "term\n" >"$2/root-term"; exit 0' TERM
    printf '%s\n' "$$" >"$2/parent.pid"
    "$0" cleanup-tree-child "$2" &
    wait
    exit 0
    ;;
  cleanup-tree-parent)
    trap 'printf "term\n" >"$2/parent-term"' TERM
    printf '%s\n' "$$" >"$2/parent.pid"
    "$0" cleanup-tree-child "$2" &
    while :; do wait || true; done
    ;;
  cleanup-tree-child)
    trap 'printf "term\n" >"$2/child-term"' TERM
    printf '%s\n' "$$" >"$2/child.pid"
    set -m
    "$0" cleanup-tree-grandchild "$2" &
    set +m
    while :; do wait || true; done
    ;;
  cleanup-tree-grandchild)
    sleeper=""
    trap 'printf "term\n" >"$2/grandchild-term"; kill "$sleeper" 2>/dev/null || true' TERM
    printf '%s\n' "$$" >"$2/grandchild.pid"
    while :; do sleep 30 & sleeper=$!; wait "$sleeper" || true; done
    ;;
  cleanup-graceful-parent)
    child=""
    trap 'printf "term\n" >"$2/parent-term"; kill -TERM "$child"; wait "$child" 2>/dev/null || true; exit 0' TERM
    printf '%s\n' "$$" >"$2/parent.pid"
    "$0" cleanup-graceful-child "$2" &
    child=$!
    while :; do wait "$child" || true; done
    ;;
  cleanup-graceful-child)
    child=""
    trap 'printf "term\n" >"$2/child-term"; kill -TERM "$child"; wait "$child" 2>/dev/null || true; exit 0' TERM
    printf '%s\n' "$$" >"$2/child.pid"
    "$0" cleanup-graceful-grandchild "$2" &
    child=$!
    while :; do wait "$child" || true; done
    ;;
  cleanup-graceful-grandchild)
    sleeper=""
    trap 'printf "term\n" >"$2/grandchild-term"; kill "$sleeper" 2>/dev/null || true; exit 0' TERM
    printf '%s\n' "$$" >"$2/grandchild.pid"
    while :; do sleep 30 & sleeper=$!; wait "$sleeper" || true; done
    ;;
  exit-cleanup-proof)
    cd "$(dirname "$0")/../.."
    source hack/lib/demo.sh
    demo_init 18098
    printf '%s\n' "$PRUKKA_STATE" >"$2/state"
    bash -c 'trap "" TERM; exec sleep 30' &
    DAEMON_PID=$!
    printf '%s\n' "$DAEMON_PID" >"$2/pid"
    ps() { return 1; }
    exit 0
    ;;
esac

cd "$(dirname "$0")/../.."

source hack/lib/demo.sh

test_root=$(mktemp -d)
mkdir -p "$test_root/external"
touch "$test_root/external/ffmpeg"
chmod 700 "$test_root/external/ffmpeg"
export PRUKKA_DEMO_FFMPEG="$test_root/external/ffmpeg"

demo_init 18097
test_force_snapshot() {
  local snapshot=${1:-${DAEMON_TREE_SNAPSHOT:-}} root=${2:-${DAEMON_PID:-}}
  local ordered _ pid _group _state deadline present

  {
    if [ -n "$snapshot" ]; then
      ordered=$(printf '%s\n' "$snapshot" | LC_ALL=C sort -k1,1nr)
      while read -r _ pid _group _state; do
        kill -KILL "$pid" 2>/dev/null || true
      done <<<"$ordered"
    elif [ -n "$root" ]; then
      kill -KILL "$root" 2>/dev/null || true
    fi
    [ -z "$root" ] || wait "$root" 2>/dev/null || true
  } 2>/dev/null

  [ -z "$snapshot" ] && return 0
  deadline=$((SECONDS + 3))
  while [ "$SECONDS" -lt "$deadline" ]; do
    present=$(demo_present_snapshot_pids "$snapshot") || return 1
    [ -z "$present" ] && return 0
    sleep 0.05
  done
  return 1
}

test_cleanup() {
  local original_status=$? cleanup_status=0

  trap - EXIT
  demo_cleanup || cleanup_status=$?
  if [ "$cleanup_status" -ne 0 ]; then
    test_force_snapshot || true
    demo_cleanup >/dev/null 2>&1 || true
  fi
  rm -rf "$test_root"
  if [ "$original_status" -ne 0 ]; then
    exit "$original_status"
  fi
  exit "$cleanup_status"
}
trap test_cleanup EXIT
demo_require_ffmpeg "path gate"

resolved=$(command -v ffmpeg)
expected="$PRUKKA_STATE/demo-bin/ffmpeg"
if [ "$resolved" != "$expected" ] || [ ! -L "$expected" ]; then
  echo "demo ffmpeg resolved to $resolved, want $expected" >&2
  exit 1
fi
if [ -e "$PRUKKA_STATE/bin/ffmpeg" ]; then
  echo "demo created an unverified managed ffmpeg" >&2
  exit 1
fi

managed_state=$PRUKKA_STATE
demo_cleanup
if [ -e "$managed_state" ]; then
  echo "demo cleanup retained its state directory" >&2
  exit 1
fi

# Registered auxiliary jobs are stopped and reaped before state is removed.
PRUKKA_STATE=$(mktemp -d)
aux_state=$PRUKKA_STATE
aux_tree=$test_root/aux-graceful
mkdir -p "$aux_tree"
"$0" cleanup-graceful-parent "$aux_tree" >"$test_root/aux-graceful.log" 2>&1 &
aux_pid=$!
for _ in $(seq 1 100); do
  [ -e "$aux_tree/grandchild.pid" ] && break
  sleep 0.01
done
[ -e "$aux_tree/grandchild.pid" ] || { echo "auxiliary tree did not start" >&2; exit 1; }
demo_register_job "$aux_pid" auxiliary-fixture
demo_cleanup
if [ -e "$aux_state" ] || kill -0 "$aux_pid" 2>/dev/null; then
  echo "registered auxiliary job was not stopped, reaped and cleaned" >&2
  exit 1
fi
for marker in parent-term child-term grandchild-term; do
  [ -e "$aux_tree/$marker" ] || { echo "auxiliary cleanup missed $marker" >&2; exit 1; }
done

# A resistant auxiliary job fails closed and keeps its recovery state.
PRUKKA_STATE=$(mktemp -d)
aux_resistant_state=$PRUKKA_STATE
bash -c 'trap "" TERM; exec sleep 30' &
aux_resistant_pid=$!
demo_register_job "$aux_resistant_pid" resistant-auxiliary
PRUKKA_DEMO_STOP_SECONDS=1
set +e
demo_cleanup 2>"$test_root/aux-resistant-error"
aux_resistant_status=$?
set -e
if [ "$aux_resistant_status" -eq 0 ] || [ ! -e "$aux_resistant_state" ] ||
  ! kill -0 "$aux_resistant_pid" 2>/dev/null ||
  ! grep -Fq 'refusing unsafe KILL' "$test_root/aux-resistant-error"; then
  echo "resistant auxiliary cleanup was not fail-safe" >&2
  exit 1
fi
kill -KILL "$aux_resistant_pid"
wait "$aux_resistant_pid" 2>/dev/null || true
demo_forget_job "$aux_resistant_pid"
demo_cleanup
unset PRUKKA_DEMO_STOP_SECONDS

# An unowned live PID is untouched and its state is retained for recovery.
sleep 30 &
foreign_pid=$!
disown "$foreign_pid"
PRUKKA_STATE=$(mktemp -d)
foreign_state=$PRUKKA_STATE
DAEMON_PID=$foreign_pid
foreign_error_file=$test_root/foreign-error
set +e
demo_cleanup 2>"$foreign_error_file"
foreign_status=$?
set -e
foreign_error=$(<"$foreign_error_file")
if [ "$foreign_status" -eq 0 ] || [ ! -e "$foreign_state" ] ||
  ! kill -0 "$foreign_pid" 2>/dev/null ||
  ! grep -Fq 'is live but is not owned by this shell' <<<"$foreign_error"; then
  echo "demo cleanup did not fail safely for an unowned PID" >&2
  exit 1
fi
kill "$foreign_pid"
wait "$foreign_pid" 2>/dev/null || true
demo_cleanup

# A failed process-table proof must preserve both the owned root and state.
PRUKKA_STATE=$(mktemp -d)
proof_state=$PRUKKA_STATE
proof_ready=$test_root/proof-ready
bash -c 'trap "" TERM; : >"$1"; exec sleep 30' _ "$proof_ready" &
DAEMON_PID=$!
proof_pid=$DAEMON_PID
for _ in $(seq 1 100); do
  [ -e "$proof_ready" ] && break
  sleep 0.01
done
[ -e "$proof_ready" ] || { echo "ownership-proof fixture did not start" >&2; exit 1; }
ps() { return 1; }
proof_error_file=$test_root/proof-error
set +e
demo_cleanup 2>"$proof_error_file"
proof_status=$?
set -e
unset -f ps
proof_error=$(<"$proof_error_file")
if [ "$proof_status" -eq 0 ] ||
  ! grep -Fq 'cannot snapshot daemon tree before TERM' <<<"$proof_error" ||
  ! grep -Fq "state preserved at $proof_state" <<<"$proof_error" ||
  ! grep -Fq "root=$proof_pid" <<<"$proof_error"; then
  echo "demo cleanup did not report a recoverable ownership-proof failure" >&2
  exit 1
fi
if [ ! -e "$proof_state" ] || ! kill -0 "$proof_pid" 2>/dev/null; then
  echo "demo cleanup mutated state after ownership proof failed" >&2
  exit 1
fi
kill -KILL "$proof_pid"
wait "$proof_pid" 2>/dev/null || true
demo_cleanup

# EXIT must surface cleanup failure when the script itself would exit zero.
exit_case=$test_root/exit-case
mkdir -p "$exit_case"
set +e
"$0" exit-cleanup-proof "$exit_case" 2>"$exit_case/error"
exit_status=$?
set -e
if [ "$exit_status" -eq 0 ]; then
  echo "EXIT cleanup failure did not change a successful script status" >&2
  exit 1
fi
exit_pid=$(<"$exit_case/pid")
exit_state=$(<"$exit_case/state")
if [ ! -e "$exit_state" ] || ! kill -0 "$exit_pid" 2>/dev/null; then
  echo "EXIT cleanup failure did not preserve recoverable state" >&2
  exit 1
fi
kill -KILL "$exit_pid"
rm -rf -- "$exit_state"

# A graceful root must propagate TERM and leave no recorded descendant.
PRUKKA_STATE=$(mktemp -d)
graceful_state=$test_root/graceful-tree
mkdir -p "$graceful_state"
"$0" cleanup-graceful-parent "$graceful_state" \
  >"$test_root/graceful-fixture.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 100); do
  [ -e "$graceful_state/grandchild.pid" ] && break
  sleep 0.01
done
[ -e "$graceful_state/grandchild.pid" ] || { echo "graceful tree did not start" >&2; exit 1; }
graceful_snapshot=$(demo_owned_tree_snapshot "$DAEMON_PID")
graceful_runtime_state=$PRUKKA_STATE
demo_cleanup
if [ -e "$graceful_runtime_state" ]; then
  echo "graceful cleanup retained runtime state" >&2
  exit 1
fi
for marker in parent-term child-term grandchild-term; do
  [ -e "$graceful_state/$marker" ] || { echo "graceful cleanup missed $marker" >&2; exit 1; }
done
if [ -n "$(demo_present_snapshot_pids "$graceful_snapshot")" ]; then
  echo "graceful cleanup retained a recorded daemon-tree PID" >&2
  exit 1
fi

# TERM resistance fails closed; a separate-PGID descendant remains untouched.
PRUKKA_STATE=$(mktemp -d)
tree_state=$test_root/resistant-tree
mkdir -p "$tree_state"
"$0" cleanup-tree-parent "$tree_state" >"$test_root/resistant-fixture.log" 2>&1 &
DAEMON_PID=$!
term_pid=$DAEMON_PID
for _ in $(seq 1 100); do
  [ -e "$tree_state/parent.pid" ] && [ -e "$tree_state/child.pid" ] &&
    [ -e "$tree_state/grandchild.pid" ] && break
  sleep 0.01
done
[ -e "$tree_state/grandchild.pid" ] || { echo "TERM-resistant tree did not start" >&2; exit 1; }
tree_snapshot=$(demo_owned_tree_snapshot "$term_pid") || {
  echo "TERM-resistant tree ownership was not demonstrable" >&2
  exit 1
}
tree_pids=$(printf '%s\n' "$tree_snapshot" | awk '{print $2}')
tree_child=$(<"$tree_state/child.pid")
tree_grandchild=$(<"$tree_state/grandchild.pid")
for expected in "$term_pid" "$tree_child" "$tree_grandchild"; do
  if ! printf '%s\n' "$tree_pids" | tr ' ' '\n' | grep -Fx "$expected" >/dev/null; then
    echo "TERM-resistant member $expected missing from tree snapshot" >&2
    exit 1
  fi
done
child_group=$(printf '%s\n' "$tree_snapshot" | awk -v pid="$tree_child" '$2 == pid { print $3 }')
grandchild_group=$(printf '%s\n' "$tree_snapshot" | awk -v pid="$tree_grandchild" '$2 == pid { print $3 }')
if [ -z "$child_group" ] || [ "$child_group" = "$grandchild_group" ]; then
  echo "fixture did not create a descendant in a separate process group" >&2
  exit 1
fi
sleep 30 &
isolated_foreign=$!
disown "$isolated_foreign"
PRUKKA_DEMO_STOP_SECONDS=1
started=$SECONDS
term_state=$PRUKKA_STATE
tree_error_file=$test_root/tree-error
set +e
demo_cleanup 2>"$tree_error_file"
tree_status=$?
set -e
tree_error=$(<"$tree_error_file")
elapsed=$((SECONDS - started))
if [ "$tree_status" -eq 0 ] || [ "$elapsed" -gt 3 ] || [ ! -e "$term_state" ] ||
  ! grep -Fq 'refusing unsafe KILL' <<<"$tree_error"; then
  echo "TERM-resistant cleanup was not bounded and fail-safe (elapsed ${elapsed}s)" >&2
  exit 1
fi
if ! kill -0 "$isolated_foreign" 2>/dev/null; then
  echo "group cleanup signalled a foreign process" >&2
  exit 1
fi
[ -e "$tree_state/parent-term" ] || {
  echo "managed daemon root did not receive graceful TERM" >&2
  exit 1
}
if [ -e "$tree_state/child-term" ] || [ -e "$tree_state/grandchild-term" ]; then
  echo "graceful TERM escaped the managed daemon root" >&2
  exit 1
fi
kill "$isolated_foreign"
wait "$isolated_foreign" 2>/dev/null || true
for pid in $tree_pids; do
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "fail-safe cleanup signalled TERM-resistant tree PID $pid" >&2
    exit 1
  fi
done
test_force_snapshot "$tree_snapshot" "$term_pid"
demo_cleanup
if [ -e "$term_state" ]; then
  echo "cleanup retry retained state after explicit fixture recovery" >&2
  exit 1
fi

# A root may exit on TERM while its recorded descendants remain alive.
PRUKKA_STATE=$(mktemp -d)
orphan_state=$test_root/orphan-tree
mkdir -p "$orphan_state"
"$0" cleanup-orphan-parent "$orphan_state" >"$test_root/orphan-fixture.log" 2>&1 &
DAEMON_PID=$!
orphan_root=$DAEMON_PID
for _ in $(seq 1 100); do
  [ -e "$orphan_state/parent.pid" ] && [ -e "$orphan_state/child.pid" ] &&
    [ -e "$orphan_state/grandchild.pid" ] && break
  sleep 0.01
done
[ -e "$orphan_state/grandchild.pid" ] || { echo "orphan-tree fixture did not start" >&2; exit 1; }
orphan_snapshot=$(demo_owned_tree_snapshot "$orphan_root") || {
  echo "orphan-tree ownership was not demonstrable before TERM" >&2
  exit 1
}
orphan_pids=$(printf '%s\n' "$orphan_snapshot" | awk '{print $2}')
for expected in "$orphan_root" \
  "$(cat "$orphan_state/child.pid")" "$(cat "$orphan_state/grandchild.pid")"; do
  if ! printf '%s\n' "$orphan_pids" | tr ' ' '\n' | grep -Fx "$expected" >/dev/null; then
    echo "orphan-tree member $expected missing from group snapshot" >&2
    exit 1
  fi
done
PRUKKA_DEMO_STOP_SECONDS=1
orphan_runtime_state=$PRUKKA_STATE
orphan_error_file=$test_root/orphan-error
set +e
demo_cleanup 2>"$orphan_error_file"
orphan_status=$?
set -e
orphan_error=$(<"$orphan_error_file")
if [ "$orphan_status" -eq 0 ] || [ ! -e "$orphan_runtime_state" ] ||
  ! grep -Fq 'daemon-tree PIDs remain after TERM' <<<"$orphan_error"; then
  echo "orphan-tree cleanup did not preserve recoverable state" >&2
  exit 1
fi
[ -e "$orphan_state/root-term" ] || {
  echo "orphan-tree root did not exit through its TERM handler" >&2
  exit 1
}
if [ -e "$orphan_state/child-term" ] || [ -e "$orphan_state/grandchild-term" ]; then
  echo "orphan-tree graceful TERM escaped the daemon root" >&2
  exit 1
fi
for pid in $orphan_pids; do
  [ "$pid" = "$orphan_root" ] || kill -0 "$pid" 2>/dev/null || {
    echo "fail-safe cleanup signalled orphan-tree PID $pid" >&2
    exit 1
  }
done
test_force_snapshot "$orphan_snapshot" "$orphan_root"
demo_cleanup
if [ -e "$orphan_runtime_state" ]; then
  echo "cleanup retry retained orphan-tree state" >&2
  exit 1
fi

echo "demo runtime gate: external ffmpeg and bounded ownership-safe cleanup"
