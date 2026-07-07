#!/usr/bin/env bash
# Bench gate: every benchmark in core/pipeline is a frame-path
# operation and must report exactly 0 allocs/op. Blocking in CI.
set -euo pipefail
cd "$(dirname "$0")/../.."

out=$(go test -run '^$' -bench . -benchmem -benchtime 2000x ./internal/core/pipeline/)
echo "$out"

violations=$(awk '/^Benchmark/ && $(NF-1) + 0 > 0 { print }' <<<"$out")

if [ -n "$violations" ]; then
  echo "::error::frame-path benchmarks allocate (0 allocs/op required):"
  echo "$violations"
  exit 1
fi

echo "bench gate: 0 allocs/op on the frame path"
