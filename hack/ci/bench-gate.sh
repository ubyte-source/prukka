#!/usr/bin/env bash
# Bench gate: designated streaming hot paths must report exactly 0 allocs/op.
# Blocking in CI; an empty benchmark match is a failure.
set -euo pipefail
cd "$(dirname "$0")/../.."

run_suite() {
  local dir=$1
  local package=$2
  local pattern=$3
  local label=$4
  local out benchmarks violations

  out=$(cd "$dir" && GOMAXPROCS=1 go test -run '^$' -bench "$pattern" -benchmem -benchtime 2000x "$package")
  echo "$out"

  benchmarks=$(awk '/^Benchmark/ { count++ } END { print count + 0 }' <<<"$out")
  if [ "$benchmarks" -eq 0 ]; then
    echo "::error::$label matched no benchmarks"
    exit 1
  fi

  violations=$(awk '
    /^Benchmark/ {
      for (i = 2; i <= NF; i++) {
        if ($i == "allocs/op" && $(i-1) + 0 > 0) print
      }
    }
  ' <<<"$out")

  if [ -n "$violations" ]; then
    echo "::error::$label allocates (0 allocs/op required):"
    echo "$violations"
    exit 1
  fi
}

for package in \
  ./internal/core/pipeline/ \
  ./internal/media/ingest/file/ \
  ./internal/media/wasapi/ \
  ./internal/providers/native/ \
  ./internal/ring/
do
  run_suite . "$package" '^BenchmarkFrame' "$package frame path"
done

run_suite engine . '^BenchmarkS16StreamDecoderDecode$' 'engine STT decoder'

echo "bench gate: 0 allocs/op on designated streaming hot paths"
