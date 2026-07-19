#!/usr/bin/env bash
#
# notice.sh renders ENGINE-NOTICE.txt — the third-party component inventory
# for the published engine runtime and model packs — from the pinned versions
# in pins.sh, so the notice can never disagree with what the recipes actually
# fetch. The license texts as shipped by each upstream are bundled by build.sh
# under lib/licenses/ in the runtime archive; this file is the summary.
#
# Usage: engine/notice.sh <output-dir>   # writes <output-dir>/ENGINE-NOTICE.txt
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd -P)"
. "$HERE/pins.sh"

OUT="${1:?usage: notice.sh <output-dir>}"
mkdir -p "$OUT"

cat > "$OUT/ENGINE-NOTICE.txt" <<EOF
Prukka engine third-party notice
================================

The Prukka engine runtime archives and model packs redistribute the
third-party components below. The license texts as shipped by each upstream
are included in the runtime archive under lib/licenses/.

Runtime components
------------------
- whisper.cpp (commit $WHISPER_CPP_COMMIT)
  $WHISPER_CPP_REPO
  License: MIT

- CTranslate2 v$CT2_VERSION (commit $CT2_COMMIT)
  $CT2_REPO
  License: MIT

- SentencePiece v$SENTENCEPIECE_VERSION (commit $SENTENCEPIECE_COMMIT)
  $SENTENCEPIECE_REPO
  License: Apache-2.0

- Piper (release $PIPER_VERSION)
  $PIPER_REPO
  License: MIT

- piper-phonemize (release $PIPER_PHONEMIZE_VERSION)
  $PIPER_PHONEMIZE_REPO
  License: MIT

GPL-licensed component: espeak-ng
---------------------------------
The Piper runtime bundles espeak-ng, which is licensed under GPL-3.0: the
espeak-ng-data directory inside the pinned Piper release tarball
($PIPER_RELEASE_URL)
and the libespeak-ng library inside the pinned piper-phonemize release
tarball ($PIPER_PHONEMIZE_RELEASE_URL).
Corresponding source for these exact binaries is available from the pinned
upstream release, which builds espeak-ng from its pinned submodule:
  $PIPER_PHONEMIZE_REPO/tree/$PIPER_PHONEMIZE_VERSION
and from the espeak-ng project itself:
  https://github.com/espeak-ng/espeak-ng

Models
------
- Whisper speech-to-text models ($WHISPER_MODEL, $WHISPER_CALL_MODEL)
  $WHISPER_MODELS_URL
  License: MIT (OpenAI Whisper models, redistributed via ggerganov/whisper.cpp)

- Opus-MT translation models (Marian, converted to CTranslate2 int8)
  it->en: $MT_MODEL_URL
  en->it: $MT_EN_IT_MODEL_URL
  License: CC-BY 4.0, with attribution to Helsinki-NLP and the Tatoeba
  Challenge (Tiedemann & Thottingal, 2020).

- Piper voices ($PIPER_VOICE, $PIPER_VOICE_IT)
  $PIPER_VOICES_URL
  License: stated per voice in the upstream MODEL_CARD bundled beside each
  voice in its model pack (models/tts/<voice>.MODEL_CARD).
EOF

echo "engine notice rendered at $OUT/ENGINE-NOTICE.txt"
