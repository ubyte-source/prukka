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
#   internal/core/ports.go  interface and type declarations only, zero
#                        statements — nothing can fail; its contracts are
#                        compile-time asserted where they are implemented
#   engine/**            the native engine is its own module, built and tested
#                        by its own CI recipe (engine/build.sh), not this gate
EXCEPTIONS='^internal/gen/|/doc\.go$|^cmd/prukka/main\.go$|^internal/core/ports\.go$|^engine/'

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
while IFS= read -r helper; do
  if grep -qE '^func (Test|Benchmark|Fuzz)' "$helper"; then
    echo "::error::${helper} defines tests; move them next to the file they exercise"
    fail=1
  fi
done < <(git ls-files --cached --others --exclude-standard '*helpers_test.go')

if [ "$fail" -ne 0 ]; then
  exit 1
fi

echo "test-mapping gate: every source file carries its own tests"
