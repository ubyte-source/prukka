#!/usr/bin/env bash
# Core-boundary gate: a package under internal/core may depend — directly or
# transitively — only on the standard library, on the rest of internal/core,
# and on the packages named below.
set -euo pipefail
cd "$(dirname "$0")/../.."

MODULE=github.com/ubyte-source/prukka

# Dependency-free helpers: pure functions of their arguments, with no
# filesystem, process or network state, so importing one cannot reverse the
# dependency.
INSIDE="^${MODULE}/internal/core(/|\$)|^${MODULE}/internal/(besteffort|redact)\$|^golang\.org/x/sync/errgroup\$"

# internal/core/config is the one exception: the daemon configuration is YAML
# on disk, so that package alone reaches a serializer and the platform's paths.
ADAPTER="${MODULE}/internal/core/config"
ADAPTER_EXTRA="^gopkg\.in/yaml\.v3\$|^${MODULE}/internal/(paths|hostos)\$"

fail=0
checked=0

while IFS= read -r pkg; do
	checked=$((checked + 1))
	allow=$INSIDE
	if [ "$pkg" = "$ADAPTER" ]; then
		allow="${INSIDE}|${ADAPTER_EXTRA}"
	fi

	while IFS= read -r dep; do
		# Standard-library paths have no dot in their first element; module
		# paths always do.
		if [[ ! $dep =~ ^[^/]*\. ]]; then
			continue
		fi
		if [[ $dep =~ $allow ]]; then
			continue
		fi
		echo "::error::${pkg} depends on ${dep}, which is outside the core boundary"
		fail=1
	done < <(go list -deps "$pkg")
done < <(go list ./internal/core/...)

# internal/core moving or being renamed must fail here, not pass here.
if [ "$checked" -eq 0 ]; then
	echo "::error::no packages under ./internal/core to check"
	fail=1
fi

if [ "$fail" -ne 0 ]; then
	exit 1
fi

echo "core-boundary gate: $checked packages under internal/core, none reaching outside it"
