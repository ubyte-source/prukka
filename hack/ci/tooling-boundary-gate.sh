#!/usr/bin/env bash
# Tooling-boundary gate: hack/ is build tooling, not a library — so every
# package under it must be `package main`, and no shipped package may import
# one.
set -euo pipefail
cd "$(dirname "$0")/../.."

MODULE=github.com/ubyte-source/prukka

fail=0
tooling=0
shipped=0

while IFS=' ' read -r pkg name; do
	tooling=$((tooling + 1))
	if [ "$name" != "main" ]; then
		echo "::error::${pkg} is package ${name}; everything under hack/ must be package main"
		fail=1
	fi
done < <(go list -f '{{.ImportPath}} {{.Name}}' ./hack/...)

# Walked with -deps, so an indirect import is caught the same as a direct one.
while IFS= read -r pkg; do
	shipped=$((shipped + 1))
	while IFS= read -r dep; do
		case $dep in
		"${MODULE}/hack/"*)
			echo "::error::${pkg} depends on ${dep}; shipped code may not import the build tooling"
			fail=1
			;;
		esac
	done < <(go list -deps "$pkg")
done < <(go list ./cmd/... ./internal/...)

# hack/ or cmd/ moving must fail here, not pass here.
if [ "$tooling" -eq 0 ] || [ "$shipped" -eq 0 ]; then
	echo "::error::inspected ${tooling} tooling and ${shipped} shipped packages; refusing to pass"
	fail=1
fi

if [ "$fail" -ne 0 ]; then
	exit 1
fi

echo "tooling-boundary gate: ${tooling} hack/ packages, all package main, none reached by ${shipped} shipped packages"
