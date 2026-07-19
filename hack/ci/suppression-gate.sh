#!/usr/bin/env bash
# Suppression gate: the tree fails when it contains lint-suppression
# directives outside the maintainer's performance allowlist.
#
# The maintainer authorises //nolint only where strictly necessary for
# maximum performance, and only in the allowlisted paths below; each such
# directive must still be specific (//nolint:linter) and carry an
# explanation, which the linter's nolintlint already enforces. Everywhere
# else the zero-suppression policy stands. #nosec is never allowed.
#
# The directives are Go-source suppressions and matter only in .go files.
# Markdown is excluded (prose may name them), as is this script (it must name
# them to find them) and the CI workflow under .github (its step names
# document the gate and legitimately mention the directives it enforces).
set -euo pipefail
# Root-anchored like test-mapping-gate.sh: ALLOWLIST and git grep's printed
# paths are repo-root-relative, so the gate must not depend on the caller's cwd.
cd "$(dirname "$0")/../.."

# Paths where a performance or native-helper //nolint is permitted (maintainer
# decision). Keep this list minimal; adding a path is a linter-contract change
# and needs maintainer review via CODEOWNERS. internal/speechengine is the native
# speech orchestrator: it execs bundle-resolved tools (G204) and converts PCM
# bit widths (G115), suppressions the maintainer has approved there. The
# self-update's replace files carry the two suppressions that moved with the
# code out of internal/media/ffmpeg, where the linter config's path
# exclusions had covered them: the staged binary must be owner-executable
# (G302) and Windows retirement passes a Win32 struct pointer to
# SetFileInformationByHandle (G103).
ALLOWLIST='^internal/media/wasapi/|^internal/speechengine/|^internal/update/replace(_windows)?\.go$'

# Scan tracked Go files so a rename cannot move an existing directive out of
# the allowlist without detection, AND files not yet added: git grep alone
# skips them, so a new suppression would pass locally and only fail in CI,
# where every file is tracked. --exclude-standard honors .gitignore.
scan_targets() {
  git ls-files -z -- '*.go'
  git ls-files -z --others --exclude-standard -- '*.go'
}

# A blanket `|| true` on the scan would make an unreadable file look like a
# clean tree — the gate passing precisely when it cannot see. grep's exit
# code cannot carry that signal here (BSD xargs collapses "no match" and
# "error" into 1), so the scan is judged by its stderr, which only a real
# failure writes.
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

# #nosec is never allowed anywhere in Go source.
nosec=$(scan_for '#[[:space:]]*nosec' n)
if [ -n "$nosec" ]; then
  echo "::error::the tree contains a #nosec directive; it is forbidden — fix the code or ask the maintainer"
  echo "$nosec"
  exit 1
fi

echo "suppression gate: clean"
