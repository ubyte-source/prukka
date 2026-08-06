#!/usr/bin/env bash
# semver-tag.sh <tag> — the one definition of a valid release tag: canonical
# SemVer 2.0.0, no "v" prefix, no leading zeroes in numeric prerelease
# identifiers.
set -euo pipefail

tag=${1-}
semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$'
[[ "$tag" =~ $semver ]] || { echo "tag is not canonical SemVer: $tag" >&2; exit 1; }

prerelease=${BASH_REMATCH[5]:-}
if [[ -n "$prerelease" ]]; then
  IFS=. read -ra identifiers <<< "$prerelease"
  for identifier in "${identifiers[@]}"; do
    if [[ "$identifier" =~ ^[0-9]+$ && ${#identifier} -gt 1 && "$identifier" == 0* ]]; then
      echo "numeric prerelease identifiers cannot have leading zeroes: $tag" >&2
      exit 1
    fi
  done
fi
