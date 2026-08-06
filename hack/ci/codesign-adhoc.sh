#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 1 ]] || { echo "usage: codesign-adhoc.sh <binary>" >&2; exit 2; }
binary=$1
[[ -f "$binary" && ! -L "$binary" ]] || { echo "invalid binary: $binary" >&2; exit 1; }

root=$(git rev-parse --show-toplevel)
epoch=$(git -C "$root" show -s --format=%ct HEAD)
[[ "$epoch" =~ ^[0-9]+$ ]] || { echo "invalid commit timestamp" >&2; exit 1; }

codesign -f -s - "$binary"
timestamp=$(TZ=UTC date -r "$epoch" +%Y%m%d%H%M.%S)
TZ=UTC touch -t "$timestamp" "$binary"
[[ $(stat -f %m "$binary") == "$epoch" ]] || { echo "failed to normalize binary timestamp" >&2; exit 1; }
codesign --verify --strict "$binary"
