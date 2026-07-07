#!/usr/bin/env bash
# Suppression gate: a pull request fails when
# it introduces lint-suppression directives outside the maintainer's
# performance allowlist, or when it touches the linter contract.
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

BASE="${1:-origin/main}"

# Paths where a performance //nolint is permitted (maintainer decision).
# Keep this list minimal; adding a path is a linter-contract change and needs
# maintainer review via CODEOWNERS.
ALLOWLIST='^internal/ring/|^internal/media/wasapi/'

# Files whose added lines introduce a nolint directive.
nolint_files=$(git diff "$BASE"...HEAD --name-only -- . \
  ':(exclude)hack/ci/suppression-gate.sh' ':(exclude)*.md' ':(exclude).github/**' \
  | while read -r f; do
      if git diff "$BASE"...HEAD -- "$f" | grep '^+' | grep -qE '//[[:space:]]*nolint'; then
        echo "$f"
      fi
    done)

offenders=$(grep -vE "$ALLOWLIST" <<<"$nolint_files" | grep -v '^$' || true)
if [ -n "$offenders" ]; then
  echo "::error::nolint added outside the performance allowlist ($ALLOWLIST); fix the code or ask the maintainer"
  echo "$offenders"
  exit 1
fi

# #nosec is never allowed anywhere.
added=$(git diff "$BASE"...HEAD -- . ':(exclude)hack/ci/suppression-gate.sh' ':(exclude)*.md' ':(exclude).github/**' | grep '^+' || true)
if grep -qE '#[[:space:]]*nosec' <<<"$added"; then
  echo "::error::this diff introduces a #nosec directive; it is forbidden — fix the code or ask the maintainer"
  grep -nE '#[[:space:]]*nosec' <<<"$added"
  exit 1
fi

if ! git diff --quiet "$BASE"...HEAD -- .golangci.yml LINTER.sha256; then
  echo "::error::.golangci.yml / LINTER.sha256 changed; the linter contract is maintainer-only"
  exit 1
fi

if git diff "$BASE"...HEAD -- tools/versions.mk | grep -q '^[+-]GOLANGCI_LINT_VERSION'; then
  echo "::error::the golangci-lint version pin changed; that requires the maintainer"
  exit 1
fi

echo "suppression gate: clean"
