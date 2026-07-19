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
# live in pins.sh, shared with build.sh and notice.sh; the download, checksum
# and Marian-conversion helpers live in lib.sh, shared with build.sh.
. "$HERE/pins.sh"
. "$HERE/lib.sh"

OUT="${1:?usage: packs.sh <output-dir>}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

require curl; require unzip; require python3

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

# A pack directory's contents are the archive root, so a model pack holds
# models/mt-<pair>/ under its own mt-<pair>/ directory.
convert_marian "$MT_MODEL_URL" "$MT_MODEL_SHA256" "it-en" "$OUT/mt-it-en/models/mt-it-en"
convert_marian "$MT_EN_IT_MODEL_URL" "$MT_EN_IT_MODEL_SHA256" "en-it" "$OUT/mt-en-it/models/mt-en-it"
write_meta mt-it-en '{"id":"mt-it-en","kind":"mt","from":"it","to":"en"}'
write_meta mt-en-it '{"id":"mt-en-it","kind":"mt","from":"en","to":"it"}'

# ---- voice-en / voice-it: piper voices + upstream MODEL_CARD ---------------
# The MODEL_CARD ships beside each voice in rhasspy/piper-voices and states
# that voice's own license terms; it is copied into the pack verbatim as
# models/tts/<voice>.MODEL_CARD. The card is not checksum-pinned, so its
# fetch is guarded by size: a pack must never ship a voice without its
# license record.
download_pack_voice() {
  local pack="$1" voice="$2" path="$3" want_onnx="$4" want_json="$5"
  local dest="$OUT/$pack/models/tts"
  download_voice "$dest" "$voice" "$path" "$want_onnx" "$want_json"
  fetch "$PIPER_VOICES_URL/$path/MODEL_CARD" "$dest/$voice.MODEL_CARD"
  [ -s "$dest/$voice.MODEL_CARD" ] || { echo "empty MODEL_CARD for $voice" >&2; exit 1; }
}

download_pack_voice voice-en "$PIPER_VOICE" "$PIPER_VOICE_PATH" \
  "$PIPER_VOICE_SHA256" "$PIPER_VOICE_JSON_SHA256"
download_pack_voice voice-it "$PIPER_VOICE_IT" "$PIPER_VOICE_IT_PATH" \
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
  convert_marian "$enx_url" "$enx_sha" "en-$iso1" "$OUT/mt-en-$iso1/models/mt-en-$iso1"
  convert_marian "$xen_url" "$xen_sha" "$iso1-en" "$OUT/mt-$iso1-en/models/mt-$iso1-en"
  write_meta "mt-en-$iso1" "{\"id\":\"mt-en-$iso1\",\"kind\":\"mt\",\"from\":\"en\",\"to\":\"$iso1\"}"
  write_meta "mt-$iso1-en" "{\"id\":\"mt-$iso1-en\",\"kind\":\"mt\",\"from\":\"$iso1\",\"to\":\"en\"}"
  download_pack_voice "voice-$iso1" "$voice" "$vdir" "$onnx_sha" "$json_sha"
  write_meta "voice-$iso1" "{\"id\":\"voice-$iso1\",\"kind\":\"voice\",\"lang\":\"$iso1\",\
\"voice\":\"models/tts/$voice.onnx\",\"license\":\"see bundled MODEL_CARD\"}"
done <<< "$PIVOT_LANGS"

echo "engine model packs assembled at $OUT"
