#!/usr/bin/env bash
# Comment gate: decides which files hack/cmd/comment-gate sees.
set -euo pipefail
cd "$(dirname "$0")/../.."

# Untracked files too: git grep alone skips them, so CI would be the first to
# see a violation. One line at a time because mapfile is bash 4 and the macOS
# host this repository supports ships bash 3.2.
files=()
while IFS= read -r file; do
	files+=("$file")
done < <(
	{
		git ls-files -- '*.go'
		git ls-files --others --exclude-standard -- '*.go'
	} | grep -v '^internal/gen/' | sort -u
)

if [ "${#files[@]}" -eq 0 ]; then
	echo "::error::no Go files to check; refusing to pass"
	exit 1
fi

go run ./hack/cmd/comment-gate "${files[@]}"
