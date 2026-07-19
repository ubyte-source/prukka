#!/usr/bin/env bash
#
# packs.sh assembles the architecture-independent model packs for the engine
# publishing pipeline. Each pack is a directory whose *contents* become the
# member root of prukka-engine-pack_<id>.tar.gz (models/... at the archive
# root), plus a single-line <id>.meta.json descriptor written beside it that
# the catalog generator (hack/cmd/engine-catalog) merges into
# prukka-engine-catalog.json:
#
#   stt-core/models/stt/...        whisper models (broadcast + call)
#   mt-it-en/models/mt-it-en/...   Opus-MT Marian converted to CTranslate2 int8
#   mt-en-it/models/mt-en-it/...   the reverse direction, same layout
#   voice-en/models/tts/...        piper voice + config + upstream MODEL_CARD
#   voice-it/models/tts/...
#
# Every download is pinned through pins.sh (shared with build.sh) and
# checksum-verified. The Marian -> CTranslate2 conversion runs in an isolated
# build-time venv exactly as in build.sh; nothing produced here contains
# Python. Packs carry no compiled code, so this script runs on Linux or macOS.
#
# Usage: engine/packs.sh <output-dir>
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd -P)"

# All pins (immutable commits, sha256 checksums, URLs, model and voice names)
# live in pins.sh, shared with build.sh and notice.sh.
. "$HERE/pins.sh"

OUT="${1:?usage: packs.sh <output-dir>}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

require() { command -v "$1" >/dev/null || { echo "missing build tool: $1" >&2; exit 1; }; }
require curl; require unzip; require python3

# verify_sha256 mirrors build.sh, but this script must also run on Linux CI
# where coreutils sha256sum is native and the perl shasum wrapper may be
# absent; either tool checks the same pinned digest.
verify_sha256() {
  local file="$1" want="$2" got
  if command -v sha256sum >/dev/null; then
    got="$(sha256sum "$file" | awk '{print $1}')"
  else
    got="$(shasum -a 256 "$file" | awk '{print $1}')"
  fi
  [ "$got" = "$want" ] || { echo "checksum mismatch for $file: got $got want $want" >&2; exit 1; }
}

# download fetches one pinned artifact and refuses any content drift.
download() {
  local url="$1" dest="$2" want="$3"
  curl -fsL -o "$dest" "$url"
  verify_sha256 "$dest" "$want"
}

# write_meta emits the single-line pack descriptor beside the pack directory.
# Values are fixed strings from pins.sh, so no JSON escaping is needed.
write_meta() {
  local id="$1" json="$2"
  printf '%s\n' "$json" > "$OUT/$id.meta.json"
}

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd -P)"
[ "$OUT" != "$HERE" ] || { echo "output directory must differ from $HERE" >&2; exit 2; }

# ---- stt-core: whisper models (broadcast + call) --------------------------
mkdir -p "$OUT/stt-core/models/stt"
download "$WHISPER_MODELS_URL/$WHISPER_MODEL" \
  "$OUT/stt-core/models/stt/$WHISPER_MODEL" "$WHISPER_MODEL_SHA256"
download "$WHISPER_MODELS_URL/$WHISPER_CALL_MODEL" \
  "$OUT/stt-core/models/stt/$WHISPER_CALL_MODEL" "$WHISPER_CALL_MODEL_SHA256"
write_meta stt-core \
  '{"id":"stt-core","kind":"stt","license":"MIT (OpenAI Whisper models via ggerganov/whisper.cpp)"}'

# ---- mt-it-en / mt-en-it: Marian -> CTranslate2 int8 ----------------------
# Python at build time only, in an isolated venv; the packs ship converted
# model data, never the converter.
python3 -m venv "$WORK/venv"
"$WORK/venv/bin/python" -m pip install --quiet "$MT_CONVERTER_CT2_WHEEL" "$MT_CONVERTER_SPM_WHEEL"

# convert_marian mirrors build.sh: download one checksum-pinned Tatoeba Marian
# model and write its int8 CTranslate2 form plus SentencePiece models into the
# pack's models/mt-<from>-<to> directory.
convert_marian() {
  local url="$1" want="$2" pair="$3"
  # Assigned separately: local expands all its arguments before any assignment,
  # so $pair would still be unbound (set -u) on the same line.
  local work="$WORK/marian-$pair"
  download "$url" "$work.zip" "$want"
  mkdir -p "$work" && (cd "$work" && unzip -q "$work.zip")
  mkdir -p "$OUT/mt-$pair/models"
  "$WORK/venv/bin/ct2-opus-mt-converter" \
    --model_dir "$work" --output_dir "$OUT/mt-$pair/models/mt-$pair" --quantization int8
  cp "$work/source.spm" "$work/target.spm" "$OUT/mt-$pair/models/mt-$pair/"
  # Reclaim the ~600MB zip + unzipped model now: with a language table this loop
  # runs dozens of times, and keeping every Marian source would exhaust the
  # runner disk long before the packs are archived.
  rm -rf "$work" "$work.zip"
}

convert_marian "$MT_MODEL_URL" "$MT_MODEL_SHA256" "it-en"
convert_marian "$MT_EN_IT_MODEL_URL" "$MT_EN_IT_MODEL_SHA256" "en-it"
write_meta mt-it-en '{"id":"mt-it-en","kind":"mt","from":"it","to":"en"}'
write_meta mt-en-it '{"id":"mt-en-it","kind":"mt","from":"en","to":"it"}'

# ---- voice-en / voice-it: piper voices + upstream MODEL_CARD ---------------
# The MODEL_CARD ships beside each voice in rhasspy/piper-voices and states
# that voice's own license terms; it is copied into the pack verbatim as
# models/tts/<voice>.MODEL_CARD. curl -f turns a missing card (HTTP 404) into
# a hard failure: a pack must never ship a voice without its license record.
download_voice() {
  local pack="$1" voice="$2" path="$3" want_onnx="$4" want_json="$5"
  local dest="$OUT/$pack/models/tts"
  mkdir -p "$dest"
  download "$PIPER_VOICES_URL/$path/$voice.onnx" "$dest/$voice.onnx" "$want_onnx"
  download "$PIPER_VOICES_URL/$path/$voice.onnx.json" "$dest/$voice.onnx.json" "$want_json"
  curl -fsL -o "$dest/$voice.MODEL_CARD" "$PIPER_VOICES_URL/$path/MODEL_CARD"
  [ -s "$dest/$voice.MODEL_CARD" ] || { echo "empty MODEL_CARD for $voice" >&2; exit 1; }
}

download_voice voice-en "$PIPER_VOICE" "$PIPER_VOICE_PATH" \
  "$PIPER_VOICE_SHA256" "$PIPER_VOICE_JSON_SHA256"
download_voice voice-it "$PIPER_VOICE_IT" "$PIPER_VOICE_IT_PATH" \
  "$PIPER_VOICE_IT_SHA256" "$PIPER_VOICE_IT_JSON_SHA256"
write_meta voice-en "{\"id\":\"voice-en\",\"kind\":\"voice\",\"lang\":\"en\",\
\"voice\":\"models/tts/$PIPER_VOICE.onnx\",\"license\":\"see bundled MODEL_CARD\"}"
write_meta voice-it "{\"id\":\"voice-it\",\"kind\":\"voice\",\"lang\":\"it\",\
\"voice\":\"models/tts/$PIPER_VOICE_IT.onnx\",\"license\":\"see bundled MODEL_CARD\"}"

# ---- pivot languages: en<->X model pair + one voice per language ------------
# Every added language translates to and from every other through the English
# hub (see internal/providers/pivot), so shipping en<->X in both directions plus
# one voice is enough for any-to-any. PIVOT_LANGS (pins.sh) is the pinned table;
# each whitespace-separated row is:
#   iso1 iso3 voice voice_dir enx_url enx_sha xen_url xen_sha onnx_sha json_sha
while read -r iso1 _iso3 voice vdir enx_url enx_sha xen_url xen_sha onnx_sha json_sha; do
  [ -n "${iso1:-}" ] || continue
  case "$iso1" in \#*) continue ;; esac
  convert_marian "$enx_url" "$enx_sha" "en-$iso1"
  convert_marian "$xen_url" "$xen_sha" "$iso1-en"
  write_meta "mt-en-$iso1" "{\"id\":\"mt-en-$iso1\",\"kind\":\"mt\",\"from\":\"en\",\"to\":\"$iso1\"}"
  write_meta "mt-$iso1-en" "{\"id\":\"mt-$iso1-en\",\"kind\":\"mt\",\"from\":\"$iso1\",\"to\":\"en\"}"
  download_voice "voice-$iso1" "$voice" "$vdir" "$onnx_sha" "$json_sha"
  write_meta "voice-$iso1" "{\"id\":\"voice-$iso1\",\"kind\":\"voice\",\"lang\":\"$iso1\",\
\"voice\":\"models/tts/$voice.onnx\",\"license\":\"see bundled MODEL_CARD\"}"
done <<< "$PIVOT_LANGS"

echo "engine model packs assembled at $OUT"
