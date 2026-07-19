#!/usr/bin/env bash
# Control-plane demo: daemon up with dashboard, session CRUD over
# CLI and REST, registry-validated languages, token-gated writes, doctor.
# Run via `make demo-control`; CI runs it headless.
set -euo pipefail
cd "$(dirname "$0")/.."
source hack/lib/demo.sh

demo_init "${PRUKKA_DEMO_PORT:-18099}"
demo_start_daemon info

echo "==> session CRUD over the CLI"
"$BIN" session add demo --in rtmp://0.0.0.0:1935/in/demo --langs it,en,de
"$BIN" session langs demo +fr -de
"$BIN" session list
"$BIN" stats

echo "==> language validation comes from the single registry"
if "$BIN" session add bad --in rtmp://x --langs ch 2>"$PRUKKA_STATE/err.log"; then
  echo "FAIL: invalid language accepted"
  exit 1
fi
grep -q 'did you mean' "$PRUKKA_STATE/err.log"

echo "==> REST mirror and token-gated writes"
curl -fsS --connect-timeout 2 --max-time 5 \
  "http://$PRUKKA_HTTP/api/v1/sessions" | grep -q '"slug":"demo"'
curl -fsS --connect-timeout 2 --max-time 5 \
  "http://$PRUKKA_HTTP/api/v1/languages" | grep -q '"label":"Italiano — it"'
code=$(curl -sS --connect-timeout 2 --max-time 5 -o /dev/null -w '%{http_code}' \
  -X POST "http://$PRUKKA_HTTP/api/v1/sessions" -d '{}')
[ "$code" = "401" ]
TOKEN=$(cat "$PRUKKA_STATE/control.token")
curl -fsS --connect-timeout 2 --max-time 5 -X DELETE \
  -H "Authorization: Bearer $TOKEN" "http://$PRUKKA_HTTP/api/v1/sessions/demo" >/dev/null

echo "==> dashboard is served"
curl -fsS --connect-timeout 2 --max-time 5 "http://$PRUKKA_HTTP/ui/" | grep -qi prukka

echo "==> doctor"
"$BIN" doctor || true # warnings (no ffmpeg on CI) must not fail the demo

echo "==> control-plane demo PASS"
