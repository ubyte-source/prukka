#!/usr/bin/env bash
# tag-request-verify.sh <request-file> — the trust boundary of both publishing
# pipelines. It runs from a default-branch checkout and treats the request file
# as attacker-controlled input, re-establishing every claim against a freshly
# fetched origin. It prints the verified reference as GITHUB_OUTPUT lines, so a
# failure appends nothing.
set -euo pipefail

FILE="${1:?usage: tag-request-verify.sh <request-file>}"
HERE="$(cd "$(dirname "$0")" && pwd -P)"

[[ -f "$FILE" ]]
[[ $(wc -c < "$FILE") -le 512 ]]
# Not mapfile: these scripts also run on a maintainer's macOS bash 3.2.
request=()
while IFS= read -r line; do request+=("$line"); done < "$FILE"
[[ ${#request[@]} -eq 3 ]] || { echo "invalid release request" >&2; exit 1; }
tag=${request[0]}
requested_ref=${request[1]}
requested_commit=${request[2]}

"$HERE/semver-tag.sh" "$tag"
[[ "$requested_ref" =~ ^[0-9a-f]{40}$ && "$requested_commit" =~ ^[0-9a-f]{40}$ ]] || {
  echo "invalid release object ids" >&2
  exit 1
}
git fetch --force --tags origin refs/heads/main:refs/remotes/origin/main
ref_oid=$(git rev-parse "refs/tags/$tag")
commit_oid=$(git rev-parse "refs/tags/$tag^{commit}")
[[ "$ref_oid" == "$requested_ref" && "$commit_oid" == "$requested_commit" ]] || {
  echo "tag $tag moved after the request" >&2
  exit 1
}
git merge-base --is-ancestor "$commit_oid" refs/remotes/origin/main || {
  echo "tag $tag does not point into protected main" >&2
  exit 1
}
remote_ref=$(git ls-remote --refs origin "refs/tags/$tag" | awk 'NR == 1 { print $1 }')
[[ "$remote_ref" == "$ref_oid" ]] || { echo "remote tag $tag moved" >&2; exit 1; }

printf 'tag=%s\nref_oid=%s\ncommit_oid=%s\n' "$tag" "$ref_oid" "$commit_oid"
