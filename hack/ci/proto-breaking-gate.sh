#!/usr/bin/env bash
# Reject wire/JSON-incompatible changes to the released prukka.v1 API.
set -euo pipefail
export LC_ALL=C
repo_root=${PRUKKA_REPO_ROOT:-"$(dirname "$0")/../.."}
cd "$repo_root"

is_semver() {
  local tag=${1#v} prerelease identifier
  local pattern='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$'
  [[ "$tag" =~ $pattern ]] || return 1

  prerelease=${BASH_REMATCH[5]:-}
  if [[ -n "$prerelease" ]]; then
    local IFS=.
    for identifier in $prerelease; do
      if [[ "$identifier" =~ ^[0-9]+$ && ${#identifier} -gt 1 && "$identifier" == 0* ]]; then
        return 1
      fi
    done
  fi

  return 0
}

# semver_compare prints -1, 0 or 1. It implements SemVer precedence directly;
# generic "version sort" disagrees for alphanumeric prerelease identifiers.
semver_compare() {
  local left=${1#v} right=${2#v}
  left=${left%%+*}
  right=${right%%+*}

  local left_core=$left right_core=$right left_pre= right_pre=
  if [[ "$left" == *-* ]]; then
    left_core=${left%%-*}
    left_pre=${left#*-}
  fi
  if [[ "$right" == *-* ]]; then
    right_core=${right%%-*}
    right_pre=${right#*-}
  fi

  local comparison index
  local -a left_core_ids right_core_ids
  IFS=. read -r -a left_core_ids <<<"$left_core"
  IFS=. read -r -a right_core_ids <<<"$right_core"
  for index in 0 1 2; do
    comparison=$(numeric_string_compare "${left_core_ids[index]}" "${right_core_ids[index]}")
    if [[ "$comparison" != 0 ]]; then
      printf '%s\n' "$comparison"
      return
    fi
  done

  if [[ -z "$left_pre" || -z "$right_pre" ]]; then
    if [[ -z "$left_pre" && -z "$right_pre" ]]; then
      echo 0
    elif [[ -z "$left_pre" ]]; then
      echo 1
    else
      echo -1
    fi
    return
  fi

  local IFS=.
  local -a left_ids=($left_pre) right_ids=($right_pre)
  local left_id right_id limit=${#left_ids[@]}
  if (( ${#right_ids[@]} > limit )); then
    limit=${#right_ids[@]}
  fi
  for ((index = 0; index < limit; index++)); do
    if (( index >= ${#left_ids[@]} )); then
      echo -1
      return
    fi
    if (( index >= ${#right_ids[@]} )); then
      echo 1
      return
    fi

    left_id=${left_ids[index]}
    right_id=${right_ids[index]}
    if [[ "$left_id" =~ ^[0-9]+$ && "$right_id" =~ ^[0-9]+$ ]]; then
      comparison=$(numeric_string_compare "$left_id" "$right_id")
    elif [[ "$left_id" =~ ^[0-9]+$ ]]; then
      comparison=-1
    elif [[ "$right_id" =~ ^[0-9]+$ ]]; then
      comparison=1
    elif [[ "$left_id" == "$right_id" ]]; then
      comparison=0
    elif [[ "$left_id" < "$right_id" ]]; then
      comparison=-1
    else
      comparison=1
    fi
    if [[ "$comparison" != 0 ]]; then
      printf '%s\n' "$comparison"
      return
    fi
  done
  echo 0
}

numeric_string_compare() {
  local left=$1 right=$2
  if (( ${#left} < ${#right} )); then
    echo -1
  elif (( ${#left} > ${#right} )); then
    echo 1
  elif [[ "$left" == "$right" ]]; then
    echo 0
  elif [[ "$left" < "$right" ]]; then
    echo -1
  else
    echo 1
  fi
}

current_tag=${PRUKKA_PROTO_CURRENT_TAG:-}
current_commit=
if [[ -n "$current_tag" ]]; then
  is_semver "$current_tag" || {
    echo "proto breaking gate: invalid current SemVer tag: $current_tag" >&2
    exit 2
  }
  git show-ref --verify --quiet "refs/tags/$current_tag" || {
    echo "proto breaking gate: current tag not found: $current_tag" >&2
    exit 2
  }
  current_commit=$(git rev-parse "refs/tags/$current_tag^{commit}")
  head_commit=$(git rev-parse HEAD)
  if [[ "$current_commit" != "$head_commit" ]]; then
    echo "proto breaking gate: current tag $current_tag does not resolve to HEAD" >&2
    exit 2
  fi
fi

baseline=
baseline_commit=
all_tags=$(git tag --merged HEAD --list)
while IFS= read -r candidate; do
  is_semver "$candidate" || continue
  candidate_commit=$(git rev-parse "refs/tags/$candidate^{commit}")

  if [[ -n "$current_tag" ]]; then
    comparison=$(semver_compare "$candidate" "$current_tag")
    # Equal-precedence build variants and every newer version are not a
    # predecessor. Tags on HEAD are aliases of the sources under review.
    if (( comparison >= 0 )) || [[ "$candidate_commit" == "$current_commit" ]]; then
      continue
    fi
  fi

  if [[ -z "$baseline" ]]; then
    baseline=$candidate
    baseline_commit=$candidate_commit
    continue
  fi

  comparison=$(semver_compare "$candidate" "$baseline")
  if (( comparison > 0 )); then
    baseline=$candidate
    baseline_commit=$candidate_commit
  elif (( comparison == 0 )); then
    if [[ "$candidate_commit" != "$baseline_commit" ]]; then
      echo "proto breaking gate: equal-precedence tags resolve to different commits: $baseline, $candidate" >&2
      exit 2
    fi
    # Build metadata has no precedence; choose a stable spelling so fixture
    # output and diagnostics do not depend on ref iteration order.
    if [[ "$candidate" < "$baseline" ]]; then
      baseline=$candidate
    fi
  fi
done <<<"$all_tags"

if [[ -z "$baseline" ]]; then
  echo "proto breaking gate: no earlier semantic-version release tag; nothing to compare"
  exit 0
fi

buf_bin=${BUF:-"$PWD/.tools/bin/buf"}
"$buf_bin" breaking proto --against ".git#tag=${baseline},subdir=proto"
echo "proto breaking gate: compatible with ${baseline}"
