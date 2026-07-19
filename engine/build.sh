#!/usr/bin/env bash
#
# build.sh assembles the native speech-engine runtime for macOS, Linux or
# Windows by downloading dependencies directly from their upstream projects.
# Every git source is pinned to an immutable commit and every downloaded model
# and binary is checksum-verified through pins.sh, the pin file shared with
# packs.sh and notice.sh. One supply-chain gap remains: the build-time Python
# wheels (ctranslate2, sentencepiece — used only to convert the Marian model,
# never shipped) are version-pinned but not checksum-pinned. Python appears at
# build time only, in an isolated venv; the shipped runtime is entirely
# compiled.
#
# Usage: engine/build.sh [--runtime-only] <output-dir>
#
# Default (full) mode produces the complete bundle of native tools:
# prukka-engine-manifest.json, whisper-server, mt, piper/, lib/,
# models/{stt,mt-it-en,mt-en-it,tts}. There is no orchestrator binary: the
# single prukka daemon self-executes its own engine subcommands against these
# tools.
#
# --runtime-only builds just the per-architecture runtime half for the
# publishing pipeline: whisper-server, mt, piper/, lib/ (including lib/licenses/)
# and the manifest. It skips the whisper model downloads, the Python/venv MT
# conversion and the voice downloads; those ship separately as model packs.
#
# Windows runs under bash (git-bash) with the MSVC toolchain on PATH (the
# build-engine workflow sets it up) and Ninja as the CMake generator.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd -P)"
. "$HERE/pins.sh"

RUNTIME_ONLY=0
if [ "${1:-}" = "--runtime-only" ]; then
  RUNTIME_ONLY=1
  shift
fi
OUT="${1:?usage: build.sh [--runtime-only] <output-dir>}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
MANIFEST_SOURCE="$HERE/prukka-engine-manifest.json"

# ---- platform detection ---------------------------------------------------
# LIBEXT/EXE name the shared-library and executable suffixes; CMAKE_GEN forces
# Ninja on Windows so every platform uses a single-config Release build.
CMAKE_GEN=()
case "$(uname -s)" in
  Darwin) OS=macos; LIBEXT=dylib; EXE= ;;
  Linux) OS=linux; LIBEXT=so; EXE= ;;
  MINGW* | MSYS* | CYGWIN*) OS=windows; LIBEXT=dll; EXE=.exe; CMAKE_GEN=(-G Ninja) ;;
  *) echo "unsupported build OS: $(uname -s)" >&2; exit 2 ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  arm64 | aarch64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 2 ;;
esac

require() { command -v "$1" >/dev/null || { echo "missing build tool: $1" >&2; exit 1; }; }
require git; require cmake; require curl; require go
case "$OS" in
  windows) require cl; require ninja ;;
  linux) require clang++; require patchelf ;;
  macos) require clang++ ;;
esac
# unzip and python3 only serve the MT model conversion, skipped in
# --runtime-only mode.
if [ "$RUNTIME_ONLY" -eq 0 ]; then require unzip; require python3; fi

# Distributed macOS runtimes must load on every supported release, not only the
# CI builder's; clang and cmake both honor this floor.
export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-12.0}"

verify_sha256() {
  local file="$1" want="$2" got
  case "$OS" in
    macos) got="$(shasum -a 256 "$file" | awk '{print $1}')" ;;
    *) got="$(sha256sum "$file" | awk '{print $1}')" ;;
  esac
  [ "$got" = "$want" ] || { echo "checksum mismatch for $file: got $got want $want" >&2; exit 1; }
}

# relocate points a compiled binary at bundle-relative libraries wherever the
# bundle lands. subdir is the lib location relative to the binary ("lib" for the
# tools at the bundle root, "" for a library sitting beside its siblings in
# lib/). macOS rewrites the Mach-O @rpath; Linux sets an $ORIGIN RPATH; Windows
# resolves DLLs from PATH (the daemon adds lib/), so nothing is needed.
relocate() {
  local binary="$1" subdir="$2" origin rpath existing
  case "$OS" in
    macos) origin='@loader_path' ;;
    linux) origin='$ORIGIN' ;;
    windows) return 0 ;;
  esac
  if [ -n "$subdir" ]; then rpath="$origin/$subdir"; else rpath="$origin"; fi
  case "$OS" in
    macos)
      otool -l "$binary" | sed -n 's/^ *path \(.*\) (offset [0-9]*)$/\1/p' |
        while IFS= read -r existing; do
          install_name_tool -delete_rpath "$existing" "$binary" || exit 1
        done
      install_name_tool -add_rpath "$rpath" "$binary"
      ;;
    linux) patchelf --set-rpath "$rpath" "$binary" ;;
  esac
}

# copy_runtime_libs copies the built shared libraries matching a name stem from
# a directory into the bundle lib/, tolerating the per-OS suffix conventions
# (.dylib, .so / .so.N, .dll).
copy_runtime_libs() {
  local src_dir="$1" stem="$2" copied=0 name f names=("$2")
  # MSVC DLLs carry no lib prefix (whisper.dll, ctranslate2.dll), so on Windows
  # also match the de-prefixed name.
  [ "$OS" = windows ] && [ "${stem#lib}" != "$stem" ] && names+=("${stem#lib}")
  for name in "${names[@]}"; do
    for f in "$src_dir/${name}"*.${LIBEXT} "$src_dir/${name}"*.${LIBEXT}.*; do
      [ -e "$f" ] || continue
      cp -a "$f" "$OUT/lib/"
      copied=1
    done
  done
  [ "$copied" -eq 1 ] || { echo "no ${stem}*.${LIBEXT} libraries found in $src_dir" >&2; exit 1; }
}

copy_shipped_licenses() {
  local src="$1" dest="$2" file rel
  [ -d "$src" ] || return 0
  find "$src" -type f \( -iname 'LICENSE*' -o -iname 'COPYING*' -o -iname 'NOTICE*' \) |
    while IFS= read -r file; do
      rel="${file#"$src"/}"
      mkdir -p "$dest/$(dirname "$rel")"
      cp "$file" "$dest/$rel"
    done
}

collect_licenses() {
  local dest="$OUT/lib/licenses"
  mkdir -p "$dest/whisper.cpp" "$dest/ctranslate2" "$dest/sentencepiece"
  cp "$WORK/whisper.cpp/LICENSE" "$dest/whisper.cpp/LICENSE"
  cp "$WORK/ct2/LICENSE" "$dest/ctranslate2/LICENSE"
  cp "$WORK/spm/LICENSE" "$dest/sentencepiece/LICENSE"
  copy_shipped_licenses "$WORK/piper" "$dest/piper"
  copy_shipped_licenses "$WORK/piper-phonemize" "$dest/piper-phonemize"
}

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd -P)"
[ "$OUT" != "$HERE" ] || { echo "output directory must differ from $HERE" >&2; exit 2; }
MANIFEST="$OUT/prukka-engine-manifest.json"
rm -f "$MANIFEST"
mkdir -p "$OUT/lib"
[ "$RUNTIME_ONLY" -eq 1 ] || mkdir -p "$OUT/models/stt" "$OUT/models/tts"

# ---- 1. STT: whisper.cpp -------------------------------------------------
git clone "$WHISPER_CPP_REPO" "$WORK/whisper.cpp"
git -C "$WORK/whisper.cpp" checkout "$WHISPER_CPP_COMMIT"
git -C "$WORK/whisper.cpp" apply "$HERE/patches/whisper-server-language.patch"
# Metal only helps on Apple Silicon; every other target stays on CPU. Pin a
# portable x86-64 baseline (AVX2/FMA/F16C) so a CI host's -march=native cannot
# emit instructions that crash on customer machines; arm NEON needs no flags.
WHISPER_FLAGS=(-DGGML_NATIVE=OFF -DWHISPER_BUILD_SERVER=ON -DBUILD_SHARED_LIBS=ON)
if [ "$OS" = macos ] && [ "$ARCH" = arm64 ]; then
  WHISPER_FLAGS+=(-DGGML_METAL=ON)
else
  WHISPER_FLAGS+=(-DGGML_METAL=OFF)
fi
if [ "$ARCH" = amd64 ]; then
  WHISPER_FLAGS+=(-DGGML_AVX=ON -DGGML_AVX2=ON -DGGML_FMA=ON -DGGML_F16C=ON)
fi
# Every Windows component (and the mt wrapper) must share one C runtime; the
# DLL-based bundle uses the dynamic CRT (/MD). MSVC_CRT carries that choice.
MSVC_CRT=()
[ "$OS" = windows ] && MSVC_CRT=(-DCMAKE_POLICY_DEFAULT_CMP0091=NEW -DCMAKE_MSVC_RUNTIME_LIBRARY=MultiThreadedDLL)
WHISPER_FLAGS+=(${MSVC_CRT[@]+"${MSVC_CRT[@]}"})
cmake ${CMAKE_GEN[@]+"${CMAKE_GEN[@]}"} -S "$WORK/whisper.cpp" -B "$WORK/whisper.cpp/build" \
  -DCMAKE_BUILD_TYPE=Release "${WHISPER_FLAGS[@]}"
cmake --build "$WORK/whisper.cpp/build" --config Release -j --target whisper-server
WHISPER_BIN_DIR="$WORK/whisper.cpp/build/bin"
[ -f "$WHISPER_BIN_DIR/whisper-server$EXE" ] || WHISPER_BIN_DIR="$WORK/whisper.cpp/build/bin/Release"
cp "$WHISPER_BIN_DIR/whisper-server$EXE" "$OUT/whisper-server$EXE"
relocate "$OUT/whisper-server$EXE" lib
copy_runtime_libs "$WHISPER_BIN_DIR" libwhisper
copy_runtime_libs "$WHISPER_BIN_DIR" libggml

download_whisper_model() {
  local model="$1" want="$2"
  curl -fsL -o "$WORK/$model" "$WHISPER_MODELS_URL/$model"
  verify_sha256 "$WORK/$model" "$want"
  cp "$WORK/$model" "$OUT/models/stt/$model"
}
if [ "$RUNTIME_ONLY" -eq 0 ]; then
  download_whisper_model "$WHISPER_MODEL" "$WHISPER_MODEL_SHA256"
  download_whisper_model "$WHISPER_CALL_MODEL" "$WHISPER_CALL_MODEL_SHA256"
fi

# ---- 2. MT engine: CTranslate2 (library) --------------------------------
git clone --recursive "$CT2_REPO" "$WORK/ct2"
git -C "$WORK/ct2" checkout "$CT2_COMMIT"
git -C "$WORK/ct2" submodule update --init --recursive
CT2_FLAGS=(-DBUILD_CLI=OFF -DOPENMP_RUNTIME=NONE -DCMAKE_POLICY_VERSION_MINIMUM=3.5)
case "$OS" in
  macos) CT2_FLAGS+=(-DWITH_MKL=OFF -DWITH_ACCELERATE=ON) ;;
  linux) CT2_FLAGS+=(-DWITH_MKL=OFF -DWITH_OPENBLAS=ON) ;;
  windows)
    # CTranslate2's OpenBLAS find is not vcpkg-aware, so point it at the exact
    # header and import library vcpkg installed.
    VCPKG_WIN="$VCPKG_INSTALLATION_ROOT/installed/x64-windows"
    OPENBLAS_INC="$(dirname "$(find "$VCPKG_WIN/include" -iname cblas.h 2>/dev/null | head -1)")"
    OPENBLAS_LIB="$(find "$VCPKG_WIN/lib" -iname '*openblas*.lib' 2>/dev/null | head -1)"
    [ -d "$OPENBLAS_INC" ] && [ -n "$OPENBLAS_LIB" ] ||
      { echo "OpenBLAS not found under $VCPKG_WIN" >&2; exit 1; }
    CT2_FLAGS+=(-DWITH_MKL=OFF -DWITH_OPENBLAS=ON
      -DOPENBLAS_INCLUDE_DIR="$(cygpath -w "$OPENBLAS_INC")"
      -DOPENBLAS_LIBRARY="$(cygpath -w "$OPENBLAS_LIB")")
    ;;
esac
CT2_FLAGS+=(${MSVC_CRT[@]+"${MSVC_CRT[@]}"})
cmake ${CMAKE_GEN[@]+"${CMAKE_GEN[@]}"} -S "$WORK/ct2" -B "$WORK/ct2/build" \
  -DCMAKE_BUILD_TYPE=Release "${CT2_FLAGS[@]}"
cmake --build "$WORK/ct2/build" --config Release -j
CT2_BUILD_LIB="$WORK/ct2/build"
ls "$CT2_BUILD_LIB"/*ctranslate2*."$LIBEXT" >/dev/null 2>&1 || CT2_BUILD_LIB="$WORK/ct2/build/Release"
copy_runtime_libs "$CT2_BUILD_LIB" libctranslate2
# CTranslate2 loads OpenBLAS at runtime on Windows; ship its DLL beside it.
if [ "$OS" = windows ]; then
  OPENBLAS_DLL="$(find "$VCPKG_WIN/bin" -iname '*openblas*.dll' 2>/dev/null | head -1)"
  [ -n "$OPENBLAS_DLL" ] && cp -a "$OPENBLAS_DLL" "$OUT/lib/"
fi

# ---- 3. MT tokenizer: SentencePiece -------------------------------------
git clone "$SENTENCEPIECE_REPO" "$WORK/spm"
git -C "$WORK/spm" checkout "$SENTENCEPIECE_COMMIT"
# On Windows SentencePiece is linked statically into mt.exe (it is small, and
# it spares a second managed DLL); macOS and Linux keep the shared library.
SPM_SHARED=ON
[ "$OS" = windows ] && SPM_SHARED=OFF
cmake ${CMAKE_GEN[@]+"${CMAKE_GEN[@]}"} -S "$WORK/spm" -B "$WORK/spm/build" \
  -DCMAKE_BUILD_TYPE=Release -DSPM_ENABLE_SHARED=$SPM_SHARED -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
  ${MSVC_CRT[@]+"${MSVC_CRT[@]}"}
cmake --build "$WORK/spm/build" --config Release -j
SPM_BUILD_LIB="$WORK/spm/build/src"
[ -d "$SPM_BUILD_LIB" ] || SPM_BUILD_LIB="$WORK/spm/build/src/Release"
[ "$OS" = windows ] || copy_runtime_libs "$SPM_BUILD_LIB" libsentencepiece

# relocate every shipped shared library so no build-tree path can leak.
if [ "$OS" != windows ]; then
  for lib in "$OUT"/lib/*.$LIBEXT "$OUT"/lib/*.$LIBEXT.*; do
    [ -e "$lib" ] && [ ! -L "$lib" ] || continue
    relocate "$lib" ""
  done
fi

# ---- 4. mt wrapper (CTranslate2 + SentencePiece) ------------------------
if [ "$OS" = windows ]; then
  cl //nologo //std:c++17 //O2 //EHsc //MD \
    //I"$(cygpath -w "$WORK/ct2/include")" //I"$(cygpath -w "$WORK/ct2/build")" \
    //I"$(cygpath -w "$WORK/spm/src")" \
    "$(cygpath -w "$HERE/mt/mt.cpp")" \
    //Fe"$(cygpath -w "$OUT/mt.exe")" \
    //link //LIBPATH:"$(cygpath -w "$CT2_BUILD_LIB")" ctranslate2.lib \
    //LIBPATH:"$(cygpath -w "$SPM_BUILD_LIB")" sentencepiece.lib
  rm -f "$OUT"/mt.obj mt.obj
else
  clang++ -std=c++17 -O2 \
    -I "$WORK/ct2/include" -I "$WORK/ct2/build" -I "$WORK/spm/src" \
    "$HERE/mt/mt.cpp" \
    -L "$CT2_BUILD_LIB" -lctranslate2 -L "$SPM_BUILD_LIB" -lsentencepiece \
    -o "$OUT/mt"
  relocate "$OUT/mt" lib
fi

# ---- 5. MT models: Marian -> CTranslate2 int8 (Python at build time only) -
convert_marian() {
  local url="$1" want="$2" pair="$3"
  local work="$WORK/marian-$pair"
  curl -fsL -o "$work.zip" "$url"
  verify_sha256 "$work.zip" "$want"
  mkdir -p "$work" && (cd "$work" && unzip -q "$work.zip")
  "$WORK/venv/bin/ct2-opus-mt-converter" \
    --model_dir "$work" --output_dir "$OUT/models/mt-$pair" --quantization int8
  cp "$work/source.spm" "$work/target.spm" "$OUT/models/mt-$pair/"
}
if [ "$RUNTIME_ONLY" -eq 0 ]; then
  python3 -m venv "$WORK/venv"
  "$WORK/venv/bin/python" -m pip install --quiet "$MT_CONVERTER_CT2_WHEEL" "$MT_CONVERTER_SPM_WHEEL"
  convert_marian "$MT_MODEL_URL" "$MT_MODEL_SHA256" "it-en"
  convert_marian "$MT_EN_IT_MODEL_URL" "$MT_EN_IT_MODEL_SHA256" "en-it"
fi

# ---- 6. TTS: Piper + voice ----------------------------------------------
# The Linux and Windows piper archives are self-contained (piper plus every
# library it links); only macOS needs the separate piper-phonemize dylibs.
case "$OS-$ARCH" in
  macos-amd64) PIPER_TAG=macos_x64; PIPER_SHA="$PIPER_SHA256_X64"; PIPER_ZIP=0 ;;
  macos-arm64) PIPER_TAG=macos_aarch64; PIPER_SHA="$PIPER_SHA256_AARCH64"; PIPER_ZIP=0 ;;
  linux-amd64) PIPER_TAG=linux_x86_64; PIPER_SHA="$PIPER_SHA256_LINUX_X86_64"; PIPER_ZIP=0 ;;
  linux-arm64) PIPER_TAG=linux_aarch64; PIPER_SHA="$PIPER_SHA256_LINUX_AARCH64"; PIPER_ZIP=0 ;;
  windows-amd64) PIPER_TAG=windows_amd64; PIPER_SHA="$PIPER_SHA256_WINDOWS_AMD64"; PIPER_ZIP=1 ;;
  *) echo "no piper build for $OS-$ARCH" >&2; exit 2 ;;
esac
if [ "$PIPER_ZIP" -eq 1 ]; then
  curl -fsL -o "$WORK/piper.zip" "$PIPER_RELEASE_URL/piper_$PIPER_TAG.zip"
  verify_sha256 "$WORK/piper.zip" "$PIPER_SHA"
  (cd "$WORK" && unzip -q piper.zip)
else
  curl -fsL -o "$WORK/piper.tar.gz" "$PIPER_RELEASE_URL/piper_$PIPER_TAG.tar.gz"
  verify_sha256 "$WORK/piper.tar.gz" "$PIPER_SHA"
  tar -xzf "$WORK/piper.tar.gz" -C "$WORK"
fi
cp -R "$WORK/piper" "$OUT/piper"
if [ "$OS" = macos ]; then
  case "$ARCH" in
    amd64) PP_SHA="$PIPER_PHONEMIZE_SHA256_X64"; PP_TAG=macos_x64 ;;
    arm64) PP_SHA="$PIPER_PHONEMIZE_SHA256_AARCH64"; PP_TAG=macos_aarch64 ;;
  esac
  curl -fsL -o "$WORK/piper-phonemize.tar.gz" "$PIPER_PHONEMIZE_RELEASE_URL/piper-phonemize_$PP_TAG.tar.gz"
  verify_sha256 "$WORK/piper-phonemize.tar.gz" "$PP_SHA"
  tar -xzf "$WORK/piper-phonemize.tar.gz" -C "$WORK"
  cp "$WORK"/piper-phonemize/lib/*.dylib "$OUT/piper/"

  # ---- macOS microphone-capture helper ------------------------------------
  # ffmpeg's raw AVFoundation input is delivered silent to a launchd daemon;
  # the native helper (drivers/macos/capture) opens the device through an
  # authorized AVCaptureSession. Shipping it in the runtime bundle makes
  # device capture work out of the box; the embedded Info.plist carries the
  # TCC usage description and ad-hoc signing keeps the binary TCC-eligible.
  case "$ARCH" in amd64) SWIFT_ARCH=x86_64 ;; *) SWIFT_ARCH=arm64 ;; esac
  CAPTURE_DIR="$HERE/../drivers/macos/capture"
  swiftc -O -target "$SWIFT_ARCH-apple-macosx${MACOSX_DEPLOYMENT_TARGET:-12.0}" \
    -framework AVFoundation -framework CoreMedia \
    -Xlinker -sectcreate -Xlinker __TEXT -Xlinker __info_plist \
    -Xlinker "$CAPTURE_DIR/Info.plist" \
    -o "$OUT/prukka-miccapture" "$CAPTURE_DIR/miccapture.swift"
  codesign --force --sign "${PRUKKA_CODESIGN_IDENTITY:--}" "$OUT/prukka-miccapture"
  codesign --verify --strict "$OUT/prukka-miccapture"
fi

download_voice() {
  local name="$1" path="$2" onnx_sha="$3" json_sha="$4"
  curl -fsL -o "$OUT/models/tts/$name.onnx" "$PIPER_VOICES_URL/$path/$name.onnx"
  verify_sha256 "$OUT/models/tts/$name.onnx" "$onnx_sha"
  curl -fsL -o "$OUT/models/tts/$name.onnx.json" "$PIPER_VOICES_URL/$path/$name.onnx.json"
  verify_sha256 "$OUT/models/tts/$name.onnx.json" "$json_sha"
}
if [ "$RUNTIME_ONLY" -eq 0 ]; then
  download_voice "$PIPER_VOICE" "$PIPER_VOICE_PATH" "$PIPER_VOICE_SHA256" "$PIPER_VOICE_JSON_SHA256"
  download_voice "$PIPER_VOICE_IT" "$PIPER_VOICE_IT_PATH" "$PIPER_VOICE_IT_SHA256" "$PIPER_VOICE_IT_JSON_SHA256"
fi

# ---- 7. License inventory + bundle declaration --------------------------
collect_licenses
# Written last so an interrupted build cannot declare a partial layout ready.
cp "$MANIFEST_SOURCE" "$MANIFEST"
chmod 0644 "$MANIFEST"

echo "engine bundle assembled at $OUT ($OS/$ARCH)"
