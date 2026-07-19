#!/usr/bin/env bash
# Block regressions in the portable, critical Go packages. Platform-only
# packages stay in their dedicated OS test matrix and are intentionally not
# folded into these thresholds.
set -euo pipefail
cd "$(dirname "$0")/../.."

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/prukka-coverage.XXXXXX")
trap 'rm -rf "$tmpdir"' EXIT

failures=0

run_area() {
	local name=$1
	local minimum=$2
	local workdir=$3
	shift 3

	local profile="$tmpdir/$name.out"
	local total

	echo "coverage gate: testing $name ($*)"
	(
		cd "$workdir"
		go test -count=1 -covermode=atomic -coverprofile="$profile" "$@"
	)

	total=$(
		cd "$workdir"
		go tool cover -func="$profile" |
			awk '/^total:/ { gsub(/%/, "", $3); value=$3 } END { if (value == "") exit 1; print value }'
	)

	printf 'coverage gate: %-8s %5.1f%% (minimum %.1f%%)\n' "$name" "$total" "$minimum"
	if ! awk -v actual="$total" -v minimum="$minimum" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
		echo "::error::coverage for $name is $total%; minimum is $minimum%"
		failures=$((failures + 1))
	fi
}

# Keep these explicit: a threshold change should be a visible review decision.
run_area core    80.0 .      ./internal/core/...
run_area control 80.0 .      ./internal/control
run_area native  80.0 .      ./internal/providers/native
run_area speech  80.0 .      ./internal/speech
run_area bounded 80.0 .      ./internal/providers/bounded
run_area dispatch 80.0 .     ./internal/dispatch
run_area engine  55.0 .      ./internal/speechengine

if ((failures > 0)); then
	echo "coverage gate: $failures area(s) below threshold"
	exit 1
fi

echo "coverage gate: all critical areas passed"
