# shellcheck shell=bash
#
# lib.sh holds the download, verification and model-conversion helpers shared
# by build.sh (runtime + full local bundle) and packs.sh (model packs), so the
# two recipes can never drift on how an artifact is fetched, checked or
# converted. It is sourced — never executed — after pins.sh, and expects the
# caller to have set WORK to a scratch directory; convert_marian also expects
# the caller's build-time venv at $WORK/venv.

require() { command -v "$1" >/dev/null || { echo "missing build tool: $1" >&2; exit 1; }; }

# Linux CI has coreutils sha256sum, macOS has the perl shasum wrapper, and
# neither is guaranteed to carry the other; both check the same pinned digest.
verify_sha256() {
  local file="$1" want="$2" got
  if command -v sha256sum >/dev/null; then
    got="$(sha256sum "$file" | awk '{print $1}')"
  else
    got="$(shasum -a 256 "$file" | awk '{print $1}')"
  fi
  [ "$got" = "$want" ] || { echo "checksum mismatch for $file: got $got want $want" >&2; exit 1; }
}

# fetch downloads one artifact, retrying transient upstream failures and naming
# the URL when it still fails — a bare `curl -fs` dies mute with exit 22 and
# leaves CI logs undebuggable.
fetch() {
  local url="$1" dest="$2"
  curl -fsSL --retry 4 --retry-delay 5 -o "$dest" "$url" ||
    { echo "download failed: $url" >&2; exit 22; }
}

# download fetches one pinned artifact and refuses any content drift.
download() {
  local url="$1" dest="$2" want="$3"
  fetch "$url" "$dest"
  verify_sha256 "$dest" "$want"
}

# convert_marian downloads one checksum-pinned Tatoeba Marian model and writes
# its int8 CTranslate2 form plus the SentencePiece models into dest.
convert_marian() {
  local url="$1" want="$2" pair="$3" dest="$4"
  # Assigned separately: local expands all its arguments before any assignment,
  # so $pair would still be unbound (set -u) on the same line.
  local work="$WORK/marian-$pair"
  download "$url" "$work.zip" "$want"
  mkdir -p "$work" && (cd "$work" && unzip -q "$work.zip")
  # Only the parent: ct2-opus-mt-converter refuses an existing --output_dir.
  mkdir -p "$(dirname "$dest")"
  "$WORK/venv/bin/ct2-opus-mt-converter" \
    --model_dir "$work" --output_dir "$dest" --quantization int8
  cp "$work/source.spm" "$work/target.spm" "$dest/"
  # Reclaim the ~600MB zip + unzipped model now: with a language table this runs
  # dozens of times, and keeping every Marian source would exhaust the runner
  # disk long before the output is archived.
  rm -rf "$work" "$work.zip"
}

download_voice() {
  local dest="$1" voice="$2" path="$3" want_onnx="$4" want_json="$5"
  mkdir -p "$dest"
  download "$PIPER_VOICES_URL/$path/$voice.onnx" "$dest/$voice.onnx" "$want_onnx"
  download "$PIPER_VOICES_URL/$path/$voice.onnx.json" "$dest/$voice.onnx.json" "$want_json"
}
