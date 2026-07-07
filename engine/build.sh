#!/usr/bin/env bash
#
# build.sh assembles an experimental macOS native speech-engine bundle by
# downloading dependencies directly from their upstream projects. Prukka setup
# does not install this bundle and releases do not currently attach it. There is
# no supported release installation path yet. The output does not contain a
# complete third-party license inventory and must not be redistributed.
#
# Every git source is pinned to an immutable commit and every downloaded model
# and binary is checksum-verified below. Two supply-chain gaps remain before
# this could be a supported release: the build-time Python wheels (ctranslate2,
# sentencepiece — used only to convert the Marian model, never shipped) are not
# checksum-pinned, and nothing is mirrored under the prukka org yet, so the
# inputs are still fetched directly from their upstreams. Python appears at build
# time only, in an isolated venv; the shipped runtime is entirely compiled.
#
# Usage: engine/build.sh <output-dir>
# Result layout (see README.md): prukka, prukka-engine-manifest.json,
# whisper-server, mt, piper/, lib/, models/{stt,mt-it-en,tts}.
set -euo pipefail

# ---- pinned upstream sources ---------------------------------------------
# Every git source is pinned to an immutable commit (tags can be force-moved),
# and every downloaded artifact is checksum-verified below, so an upstream that
# changes cannot silently alter the bundle.
WHISPER_CPP_COMMIT="080bbbe85230f624f0b52127f1ae1218247989f9" # ggml-org/whisper.cpp
CT2_COMMIT="399239a790ad0da4e4363e0dcbb83495b5abd742"        # OpenNMT/CTranslate2 v4.8.1
SENTENCEPIECE_COMMIT="17d7580d6407802f85855d2cc9190634e2c95624" # google/sentencepiece v0.2.0
PIPER_VERSION="2023.11.14-2"                                  # rhasspy/piper (bin 1.2.0)
WHISPER_MODEL="ggml-base.bin"                                 # ggml-org/whisper.cpp models
PIPER_VOICE="en_US-lessac-medium"                             # rhasspy/piper-voices

# Content checksums pin the binary downloads regardless of the (mutable) branch
# refs they are served from.
WHISPER_MODEL_SHA256="60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe"
PIPER_SHA256="ced85c0a3df13945b1e623b878a48fdc2854d5c485b4b67f62857cf551deaf8b"
PIPER_VOICE_SHA256="5efe09e69902187827af646e1a6e9d269dee769f9877d17b16b1b46eeaaf019f"
PIPER_VOICE_JSON_SHA256="efe19c417bed055f2d69908248c6ba650fa135bc868b0e6abb3da181dab690a0"

# Helsinki-NLP Opus-MT ita->eng, SentencePiece (spm32k), Marian format — no
# torch needed for conversion, and no Moses/BPE/perl at runtime.
MT_MODEL_URL="https://object.pouta.csc.fi/Tatoeba-MT-models/ita-eng/opus-2021-02-18.zip"
MT_MODEL_SHA256="d776d13be1e20d965118ca28a28c1f30e6616b327f0d92cfa7aa0f2b47b5e6e7"

OUT="${1:?usage: build.sh <output-dir>}"
HERE="$(cd "$(dirname "$0")" && pwd -P)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
MANIFEST_SOURCE="$HERE/prukka-engine-manifest.json"

require() { command -v "$1" >/dev/null || { echo "missing build tool: $1" >&2; exit 1; }; }
require git; require cmake; require clang++; require curl; require unzip; require python3; require go

verify_sha256() {
  local file="$1" want="$2" got
  got="$(shasum -a 256 "$file" | awk '{print $1}')"
  [ "$got" = "$want" ] || { echo "checksum mismatch for $file: got $got want $want" >&2; exit 1; }
}

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd -P)"
[ "$OUT" != "$HERE" ] || { echo "output directory must differ from $HERE" >&2; exit 2; }
MANIFEST="$OUT/prukka-engine-manifest.json"
rm -f "$MANIFEST"
mkdir -p "$OUT/lib" "$OUT/models/stt" "$OUT/models/tts"

# ---- 1. STT: whisper.cpp -------------------------------------------------
git clone https://github.com/ggml-org/whisper.cpp "$WORK/whisper.cpp"
git -C "$WORK/whisper.cpp" checkout "$WHISPER_CPP_COMMIT"
git -C "$WORK/whisper.cpp" apply "$HERE/patches/whisper-server-language.patch"
cmake -S "$WORK/whisper.cpp" -B "$WORK/whisper.cpp/build" -DCMAKE_BUILD_TYPE=Release -DWHISPER_BUILD_SERVER=ON
cmake --build "$WORK/whisper.cpp/build" -j --target whisper-server
cp "$WORK/whisper.cpp/build/bin/whisper-server" "$OUT/whisper-server"
cp "$WORK"/whisper.cpp/build/bin/libwhisper*.dylib "$WORK"/whisper.cpp/build/bin/libggml*.dylib "$OUT/lib/" 2>/dev/null || \
  cp "$WORK"/whisper.cpp/build/bin/libwhisper*.so* "$WORK"/whisper.cpp/build/bin/libggml*.so* "$OUT/lib/"
curl -fsL -o "$WORK/$WHISPER_MODEL" \
  "https://huggingface.co/ggml-org/whisper.cpp/resolve/main/$WHISPER_MODEL"
verify_sha256 "$WORK/$WHISPER_MODEL" "$WHISPER_MODEL_SHA256"
cp "$WORK/$WHISPER_MODEL" "$OUT/models/stt/$WHISPER_MODEL"

# ---- 2. MT engine: CTranslate2 (library + CLI) ---------------------------
git clone --recursive https://github.com/OpenNMT/CTranslate2 "$WORK/ct2"
git -C "$WORK/ct2" checkout "$CT2_COMMIT"
git -C "$WORK/ct2" submodule update --init --recursive
cmake -S "$WORK/ct2" -B "$WORK/ct2/build" -DCMAKE_BUILD_TYPE=Release \
  -DWITH_MKL=OFF -DWITH_ACCELERATE=ON -DOPENMP_RUNTIME=NONE -DBUILD_CLI=OFF \
  -DCMAKE_POLICY_VERSION_MINIMUM=3.5
cmake --build "$WORK/ct2/build" -j
cp "$WORK"/ct2/build/libctranslate2*.dylib "$OUT/lib/" 2>/dev/null || cp "$WORK"/ct2/build/libctranslate2*.so* "$OUT/lib/"

# ---- 3. MT tokenizer: SentencePiece (library for the mt wrapper) ---------
git clone https://github.com/google/sentencepiece "$WORK/spm"
git -C "$WORK/spm" checkout "$SENTENCEPIECE_COMMIT"
cmake -S "$WORK/spm" -B "$WORK/spm/build" -DCMAKE_BUILD_TYPE=Release -DSPM_ENABLE_SHARED=ON \
  -DCMAKE_POLICY_VERSION_MINIMUM=3.5
cmake --build "$WORK/spm/build" -j
cp "$WORK"/spm/build/src/libsentencepiece.*dylib "$OUT/lib/" 2>/dev/null || cp "$WORK"/spm/build/src/libsentencepiece.so* "$OUT/lib/"

# ---- 4. mt wrapper (CTranslate2 + SentencePiece) -------------------------
clang++ -std=c++17 -O2 \
  -I "$WORK/ct2/include" -I "$WORK/ct2/build" -I "$WORK/spm/src" \
  "$HERE/mt/mt.cpp" \
  -L "$WORK/ct2/build" -lctranslate2 -L "$WORK/spm/build/src" -lsentencepiece \
  -o "$OUT/mt"

# ---- 5. MT model: Marian -> CTranslate2 int8 (Python at build time only) -
curl -fsL -o "$WORK/mt.zip" "$MT_MODEL_URL"
verify_sha256 "$WORK/mt.zip" "$MT_MODEL_SHA256"
mkdir -p "$WORK/marian" && (cd "$WORK/marian" && unzip -q "$WORK/mt.zip")
python3 -m venv "$WORK/venv"
"$WORK/venv/bin/python" -m pip install --quiet "ctranslate2==4.8.1" "sentencepiece==0.2.0"
"$WORK/venv/bin/ct2-opus-mt-converter" \
  --model_dir "$WORK/marian" --output_dir "$OUT/models/mt-it-en" --quantization int8
cp "$WORK/marian/source.spm" "$WORK/marian/target.spm" "$OUT/models/mt-it-en/"

# ---- 6. TTS: Piper + voice ----------------------------------------------
curl -fsL -o "$WORK/piper.tar.gz" \
  "https://github.com/rhasspy/piper/releases/download/$PIPER_VERSION/piper_macos_x64.tar.gz"
verify_sha256 "$WORK/piper.tar.gz" "$PIPER_SHA256"
tar -xzf "$WORK/piper.tar.gz" -C "$WORK"
cp -R "$WORK/piper" "$OUT/piper"
curl -fsL -o "$OUT/models/tts/$PIPER_VOICE.onnx" \
  "https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/$PIPER_VOICE.onnx"
verify_sha256 "$OUT/models/tts/$PIPER_VOICE.onnx" "$PIPER_VOICE_SHA256"
curl -fsL -o "$OUT/models/tts/$PIPER_VOICE.onnx.json" \
  "https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/$PIPER_VOICE.onnx.json"
verify_sha256 "$OUT/models/tts/$PIPER_VOICE.onnx.json" "$PIPER_VOICE_JSON_SHA256"

# ---- 7. Orchestrator (pure Go, this module) ------------------------------
(cd "$HERE" && go build -o "$OUT/prukka" .)

# ---- 8. Bundle declaration -----------------------------------------------
# Written last so an interrupted build cannot declare a partial layout ready.
cp "$MANIFEST_SOURCE" "$MANIFEST"
chmod 0644 "$MANIFEST"

echo "engine bundle assembled at $OUT"
