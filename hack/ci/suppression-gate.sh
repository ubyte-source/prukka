#!/usr/bin/env bash
# Suppression gate: the tree fails when a .go file carries a lint-suppression
# directive outside the maintainer's performance allowlist. #nosec is never
# allowed anywhere.
set -euo pipefail
# ALLOWLIST and git grep's printed paths are repo-root-relative, so the gate
# must not depend on the caller's cwd.
cd "$(dirname "$0")/../.."

# Paths where a performance or native-helper //nolint is permitted. Adding a
# path is a linter-contract change and needs maintainer review via CODEOWNERS.
ALLOWLIST='^internal/media/wasapi/|^internal/speechengine/|^internal/update/replace(_windows)?\.go$'

# Tracked files, so a rename cannot move a directive out of the allowlist
# undetected, and files not yet added, which git grep alone would skip.
scan_targets() {
  git ls-files -z -- '*.go'
  git ls-files -z --others --exclude-standard -- '*.go'
}

# BSD xargs collapses "no match" and "error" into exit 1, so the scan is
# judged by its stderr, which only a real failure writes.
scan_for() {
  local pattern=$1 flag=$2 output problems
  problems=$(mktemp)
  output=$(scan_targets | xargs -0 grep -"$flag"E "$pattern" -- 2>"$problems" || true)
  if [ -s "$problems" ]; then
    echo "::error::suppression gate could not scan the whole tree; refusing to pass" >&2
    cat "$problems" >&2
    rm -f "$problems"
    exit 2
  fi
  rm -f "$problems"
  printf '%s' "$output"
}

nolint_files=$(scan_for '//[[:space:]]*nolint' l)

offenders=$(grep -vE "$ALLOWLIST" <<<"$nolint_files" | grep -v '^$' || true)
if [ -n "$offenders" ]; then
  echo "::error::nolint added outside the performance allowlist ($ALLOWLIST); fix the code or ask the maintainer"
  echo "$offenders"
  exit 1
fi

nosec=$(scan_for '#[[:space:]]*nosec' n)
if [ -n "$nosec" ]; then
  echo "::error::the tree contains a #nosec directive; it is forbidden — fix the code or ask the maintainer"
  echo "$nosec"
  exit 1
fi

echo "suppression gate: clean"
