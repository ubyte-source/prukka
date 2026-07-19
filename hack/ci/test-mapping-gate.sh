#!/usr/bin/env bash
# Test-mapping gate: every source file carries its own tests, so a failing
# test names the file under suspicion. For each tracked foo.go there must
# be a sibling foo_test.go or foo_internal_test.go. helpers_test.go files
# may hold shared fixtures but no Test functions.
set -euo pipefail
cd "$(dirname "$0")/../.."

# Exceptions, each earning its place:
#   internal/gen/**      generated code, excluded everywhere
#   */doc.go             package documentation, zero statements
#   cmd/prukka/main.go   the process entry point; its body is os.Exit(run())
EXCEPTIONS='^internal/gen/|/doc\.go$|^cmd/prukka/main\.go$'

fail=0

while IFS= read -r src; do
	[ -f "$src" ] || continue
	base="${src%.go}"
  if [ ! -f "${base}_test.go" ] && [ ! -f "${base}_internal_test.go" ]; then
    echo "::error::${src} has no ${base##*/}_test.go — every file carries its own tests"
    fail=1
  fi
done < <(git ls-files --cached --others --exclude-standard '*.go' |
  grep -v '_test\.go$' | grep -vE "$EXCEPTIONS")

# helpers_test.go must not hide Test functions: those belong to a file.
# TestMain alone is allowed — it is the package's re-exec harness, not a test.
while IFS= read -r helper; do
  if grep -E '^func (Test|Benchmark|Fuzz)' "$helper" | grep -qv '^func TestMain('; then
    echo "::error::${helper} defines tests; move them next to the file they exercise"
    fail=1
  fi
done < <(git ls-files --cached --others --exclude-standard '*helpers_test.go')

# Reverse mapping: every test file names the source it exercises, so a
# failing test always points at one file. foo_test.go (or
# foo_internal_test.go) must sit beside foo.go or a platform variant
# foo_{unix,windows,darwin,linux,other}.go. Sanctioned exceptions:
#   *helpers_test.go      shared fixtures (no Test funcs, checked above)
#   */benchmark_test.go   the package's bench-gated hot-path benchmarks
#   deploy/**             tests exercise install.sh/install.ps1, not Go
while IFS= read -r tst; do
  base="${tst%_test.go}"
  [ -f "${base}.go" ] && continue
  base="${base%_internal}"
  [ -f "${base}.go" ] && continue
  matched=0
  for variant in unix windows darwin linux other; do
    if [ -f "${base}_${variant}.go" ]; then matched=1; break; fi
  done
  [ "$matched" -eq 1 ] && continue
  echo "::error::${tst} tests no matching source — name it after the file it exercises"
  fail=1
done < <(git ls-files --cached --others --exclude-standard '*_test.go' |
  grep -vE "$EXCEPTIONS" | grep -vE 'helpers_test\.go$|(^|/)benchmark_test\.go$|^deploy/')

# benchmark_test.go carries only the bench-gate's benchmarks, never tests.
while IFS= read -r bench; do
  if grep -qE '^func (Test|Fuzz)' "$bench"; then
    echo "::error::${bench} defines tests; move them next to the file they exercise"
    fail=1
  fi
done < <(git ls-files --cached --others --exclude-standard '*/benchmark_test.go')

if [ "$fail" -ne 0 ]; then
  exit 1
fi

echo "test-mapping gate: every source file carries its own tests"
