# Deterministic provenance helpers shared by PGO capture and its CI gate.

pgo_sha256_stream() {
  if command -v sha256sum >/dev/null 2>&1; then
    LC_ALL=C sha256sum | awk '{ print $1 }'
  else
    LC_ALL=C shasum -a 256 | awk '{ print $1 }'
  fi
}

pgo_sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    LC_ALL=C sha256sum "$1" | awk '{ print $1 }'
  else
    LC_ALL=C shasum -a 256 "$1" | awk '{ print $1 }'
  fi
}

# The profile must follow the exact compiled root-module sources and embeds,
# independent of whether it was captured before or after those files were
# committed.
pgo_source_fingerprint() {
  {
    git ls-files -- '*.go' go.mod go.sum 'internal/webui/dist/**' cmd/prukka/Info.plist
    git ls-files --others --exclude-standard -- \
      '*.go' go.mod go.sum 'internal/webui/dist/**' cmd/prukka/Info.plist
  } |
    LC_ALL=C sort |
    uniq |
    while IFS= read -r file; do
      [ -n "$file" ] && [ -f "$file" ] || continue
      printf '%s\0' "$file"
      cat "$file"
      printf '\0'
    done |
    pgo_sha256_stream
}

pgo_provenance_field() {
  local file=$1 key=$2

  awk -F= -v key="$key" '
    $1 == key { count++; value = substr($0, length(key) + 2) }
    END { if (count != 1 || value == "") exit 1; print value }
  ' "$file"
}
