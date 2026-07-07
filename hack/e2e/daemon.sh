#!/usr/bin/env bash
# Launch a daemon for the dashboard e2e suite (Playwright webServer).
# State lives in a fixed scratch dir so the tests can read control.token;
# a previous run's state is replaced.
set -euo pipefail
cd "$(dirname "$0")/../.."

PORT="${PRUKKA_E2E_PORT:-18093}"
STATE="${TMPDIR:-/tmp}/prukka-e2e-state"

rm -rf "$STATE"
mkdir -p "$STATE"

# A per-run config file keeps the suite hermetic (no host config read) and
# writable, so the Settings section's save path is exercised for real.
: > "$STATE/config.yaml"

go build -o bin/prukka ./cmd/prukka

PRUKKA_STATE="$STATE" PRUKKA_HTTP="127.0.0.1:$PORT" \
  exec bin/prukka daemon --config "$STATE/config.yaml" --log-level warn
