#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

source hack/lib/pgo-provenance.sh

profile=cmd/prukka/default.pgo
provenance=cmd/prukka/default.pgo.provenance
if [ ! -e "$profile" ] && [ ! -e "$provenance" ]; then
  echo "pgo profile gate: no profile committed"
  exit 0
fi
if [ ! -f "$profile" ] || [ ! -f "$provenance" ]; then
  echo "pgo profile and provenance must either both exist or both be absent" >&2
  exit 1
fi

[ "$(pgo_provenance_field "$provenance" format)" = 1 ] || {
  echo "unsupported pgo provenance format" >&2
  exit 1
}
for key in source_sha256 profile_sha256 engine_sha256 ffmpeg_sha256 fixture_sha256; do
  value=$(pgo_provenance_field "$provenance" "$key")
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid $key in pgo provenance" >&2; exit 1; }
done
for key in sessions profile_seconds; do
  value=$(pgo_provenance_field "$provenance" "$key")
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || { echo "invalid $key in pgo provenance" >&2; exit 1; }
done

expected_source=$(pgo_provenance_field "$provenance" source_sha256)
actual_source=$(pgo_source_fingerprint)
[ "$actual_source" = "$expected_source" ] || {
  echo "pgo profile was captured from a different source tree" >&2
  exit 1
}
expected_profile=$(pgo_provenance_field "$provenance" profile_sha256)
actual_profile=$(pgo_sha256_file "$profile")
[ "$actual_profile" = "$expected_profile" ] || {
  echo "pgo profile digest does not match its provenance" >&2
  exit 1
}
expected_go=$(pgo_provenance_field "$provenance" go_version)
actual_go=$(go env GOVERSION)
[ "$actual_go" = "$expected_go" ] || {
  echo "pgo profile Go version $expected_go does not match $actual_go" >&2
  exit 1
}

profile_top=$(go tool pprof -top -nodecount=0 -nodefraction=0 "$profile")
grep -Eq 'github.com/ubyte-source/prukka/internal/(core/pipeline|media/egress/audio)' <<<"$profile_top" ||
  { echo "pgo profile contains no frame-path samples" >&2; exit 1; }
grep -Eq 'pipeline\.\(\*Mixer\)\.PullInto|pipeline\.AppendS16LE' <<<"$profile_top" ||
  { echo "pgo profile contains no current frame-path symbols" >&2; exit 1; }
if grep -Eq 'providers/(cartesia|openrouter)|providers/helpers/(retry|breaker)' <<<"$profile_top"; then
  echo "pgo profile contains retired provider symbols" >&2
  exit 1
fi

echo "pgo profile gate: provenance, source and frame-path samples verified"
