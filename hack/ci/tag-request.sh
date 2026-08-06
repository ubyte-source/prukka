#!/usr/bin/env bash
# tag-request.sh <tag> <output-file> — resolve a dispatched release tag against
# the protected origin and record it for a privileged consumer.
#
# This runs from the dispatched ref, so what it writes is untrusted data; the
# trust boundary is tag-request-verify.sh. Validating here only fails a bad
# request early, before any build minute is spent.
set -euo pipefail

TAG="${1:?usage: tag-request.sh <tag> <output-file>}"
OUT="${2:?usage: tag-request.sh <tag> <output-file>}"
HERE="$(cd "$(dirname "$0")" && pwd -P)"

"$HERE/semver-tag.sh" "$TAG"
git check-ref-format "refs/tags/$TAG"
git fetch --force --tags origin refs/heads/main:refs/remotes/origin/main
ref_oid=$(git rev-parse "refs/tags/$TAG")
commit_oid=$(git rev-parse "refs/tags/$TAG^{commit}")
git merge-base --is-ancestor "$commit_oid" refs/remotes/origin/main || {
  echo "tag $TAG does not point into protected main" >&2
  exit 1
}
remote_ref=$(git ls-remote --refs origin "refs/tags/$TAG" | awk 'NR == 1 { print $1 }')
[[ -n "$remote_ref" && "$remote_ref" == "$ref_oid" ]] || {
  echo "tag $TAG changed while the request was being validated" >&2
  exit 1
}
printf '%s\n%s\n%s\n' "$TAG" "$ref_oid" "$commit_oid" > "$OUT"
