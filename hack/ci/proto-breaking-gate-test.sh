#!/usr/bin/env bash
# Deterministic repository fixture for PR and tagged-release baseline selection.
set -euo pipefail
cd "$(dirname "$0")/../.."

script=$PWD/hack/ci/proto-breaking-gate.sh
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
repo=$fixture/repo
mkdir -p "$repo/proto"
git -C "$repo" init -q
git -C "$repo" config user.email fixture@example.invalid
git -C "$repo" config user.name fixture

printf 'syntax = "proto3";\n' >"$repo/proto/api.proto"
git -C "$repo" add proto/api.proto
git -C "$repo" commit -qm first
git -C "$repo" tag 0.1.0

calls=$fixture/buf-calls
fake_buf=$fixture/buf
cat >"$fake_buf" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$PROTO_GATE_CALLS"
EOF
chmod +x "$fake_buf"

first_output=$(PROTO_GATE_CALLS="$calls" PRUKKA_REPO_ROOT="$repo" \
  PRUKKA_PROTO_CURRENT_TAG=0.1.0 BUF="$fake_buf" "$script")
[[ "$first_output" == *"no earlier semantic-version release tag"* ]]
[[ ! -e "$calls" ]]

printf '// next\n' >>"$repo/proto/api.proto"
git -C "$repo" add proto/api.proto
git -C "$repo" commit -qm second
# The lower tag on the same commit proves the release path cannot accidentally
# compare the schema against an alias of HEAD.
git -C "$repo" tag 0.1.1
git -C "$repo" tag -a 0.2.0 -m release

: >"$calls"
PRUKKA_REPO_ROOT="$repo" PRUKKA_PROTO_CURRENT_TAG= \
  BUF="$fake_buf" PROTO_GATE_CALLS="$calls" "$script" >/dev/null
grep -Fxq 'breaking proto --against .git#tag=0.2.0,subdir=proto' "$calls"

: >"$calls"
PRUKKA_REPO_ROOT="$repo" PRUKKA_PROTO_CURRENT_TAG=0.2.0 \
  BUF="$fake_buf" PROTO_GATE_CALLS="$calls" "$script" >/dev/null
grep -Fxq 'breaking proto --against .git#tag=0.1.0,subdir=proto' "$calls"
if grep -Fq 'tag=0.2.0' "$calls" || grep -Fq 'tag=0.1.1' "$calls"; then
  echo "proto breaking fixture selected a tag on the release commit" >&2
  exit 1
fi

# Git's version sort orders these identifiers numerically; SemVer requires
# ASCII lexical ordering because neither identifier is purely numeric.
printf '// prerelease ten\n' >>"$repo/proto/api.proto"
git -C "$repo" add proto/api.proto
git -C "$repo" commit -qm prerelease-ten
git -C "$repo" tag 1.0.0-alpha10
printf '// prerelease two\n' >>"$repo/proto/api.proto"
git -C "$repo" add proto/api.proto
git -C "$repo" commit -qm prerelease-two
git -C "$repo" tag 1.0.0-alpha2

: >"$calls"
PRUKKA_REPO_ROOT="$repo" PRUKKA_PROTO_CURRENT_TAG=1.0.0-alpha2 \
  BUF="$fake_buf" PROTO_GATE_CALLS="$calls" "$script" >/dev/null
grep -Fxq 'breaking proto --against .git#tag=1.0.0-alpha10,subdir=proto' "$calls"

echo "proto breaking gate fixture: PASS"
