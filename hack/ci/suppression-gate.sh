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

# Paths where a performance //nolint is permitted (maintainer decision).
# Keep this list minimal; adding a path is a linter-contract change and needs
# maintainer review via CODEOWNERS.
ALLOWLIST='^internal/ring/|^internal/media/wasapi/'

# Scan complete tracked Go files so a rename cannot move an existing
# directive out of the allowlist without detection. The engine/ module is a
# separate Go module built and linted by its own recipe (engine/build.sh),
# excluded here as it is from the test-mapping gate; its suppressions are that
# module's own concern.
nolint_files=$(git grep -lE '//[[:space:]]*nolint' -- '*.go' ':(exclude)engine/' || true)

offenders=$(grep -vE "$ALLOWLIST" <<<"$nolint_files" | grep -v '^$' || true)
if [ -n "$offenders" ]; then
  echo "::error::nolint added outside the performance allowlist ($ALLOWLIST); fix the code or ask the maintainer"
  echo "$offenders"
  exit 1
fi

# #nosec is never allowed anywhere in Go source (engine/ module excluded as above).
nosec=$(git grep -nE '#[[:space:]]*nosec' -- '*.go' ':(exclude)engine/' || true)
if [ -n "$nosec" ]; then
  echo "::error::the tree contains a #nosec directive; it is forbidden — fix the code or ask the maintainer"
  echo "$nosec"
  exit 1
fi

echo "suppression gate: clean"
