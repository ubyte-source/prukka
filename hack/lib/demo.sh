# Shared demo plumbing, sourced by every hack/demo-*.sh: one place for the
# state dir, the ffmpeg link, daemon boot, health, key skip and cleanup.
#
# Usage:
#   source "$(dirname "$0")/lib/demo.sh"
#   demo_init 18097                    # state dir, PRUKKA_* env, cleanup trap
#   demo_require_ffmpeg "my demo" ||  exit 0   # skip cleanly without ffmpeg
#   demo_start_daemon debug            # boot + wait healthy
#   …
#   demo_skip_without_key "my demo" && exit 0  # skip cleanly without a key

BIN=bin/prukka
DAEMON_PID=""

demo_init() {
  PRUKKA_STATE="$(mktemp -d)"
  export PRUKKA_STATE
  export PRUKKA_HTTP="127.0.0.1:$1"

  if [ -n "${OPENROUTER_API_KEY:-}" ]; then
    export PRUKKA_OPENROUTER_KEY="$OPENROUTER_API_KEY"
  fi

  trap demo_cleanup EXIT
}

demo_cleanup() {
  [ -n "$DAEMON_PID" ] && kill "$DAEMON_PID" 2>/dev/null && wait "$DAEMON_PID" 2>/dev/null
  rm -rf "$PRUKKA_STATE"
}

# demo_require_ffmpeg resolves ffmpeg (PATH or PRUKKA_DEMO_FFMPEG), links it
# into the daemon's managed path and exports FF; false means skip.
demo_require_ffmpeg() {
  FF="${PRUKKA_DEMO_FFMPEG:-$(command -v ffmpeg || true)}"

  if [ -z "$FF" ]; then
    echo "==> $1 SKIPPED (no ffmpeg; run \`prukka setup\` and set PRUKKA_DEMO_FFMPEG)"
    return 1
  fi

  mkdir -p "$PRUKKA_STATE/bin"
  ln -sf "$FF" "$PRUKKA_STATE/bin/ffmpeg"
}

demo_start_daemon() {
  "$BIN" daemon --log-level "${1:-warn}" >"$PRUKKA_STATE/daemon.log" 2>&1 &
  DAEMON_PID=$!

  for _ in $(seq 1 50); do
    curl -fs "http://$PRUKKA_HTTP/healthz" >/dev/null 2>&1 && break
    sleep 0.2
  done

  curl -fs "http://$PRUKKA_HTTP/healthz" >/dev/null
  echo "==> daemon healthy on $PRUKKA_HTTP"
}

# demo_skip_without_key polls the daemon log after a session start: true
# (skip) without a usable OpenRouter key, false once work is flowing.
demo_skip_without_key() {
  for _ in $(seq 1 20); do
    if grep -q "lane unavailable" "$PRUKKA_STATE/daemon.log"; then
      echo "==> $1 SKIPPED (no usable OpenRouter key)"
      return 0
    fi

    grep -qE "utterance transcribed|ffmpeg started" "$PRUKKA_STATE/daemon.log" && return 1
    sleep 0.5
  done

  return 1
}
