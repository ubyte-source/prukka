# prukka native speech engine

This experimental module is the all-local speech-engine orchestrator that the
daemon can spawn as a child process. It is **one binary, `prukka`, with three
subcommands** — each doing exactly one job:

| subcommand | job | native dependency |
|---|---|---|
| `prukka stt` | transcribe the source audio in its own language | whisper.cpp (`whisper-server`) |
| `prukka mt`  | translate source → target with a real Opus-MT model | CTranslate2 + SentencePiece (the `mt` wrapper) |
| `prukka tts` | synthesize the translated text | Piper |

The daemon's adapters in `internal/providers/native` spawn `config.Local.Bin`
(this binary) over stdio: `stt --model M --rate R --threads N [--language L]`,
`mt --from F --to T`, `tts --model V --rate R`. `stt` transcribes only — there
is **no whisper `--translate` shortcut**; translation is `mt`'s job, always.

The daemon divides its effective `GOMAXPROCS` budget by `max_lanes` and clamps
each Whisper helper to 1–4 computation threads. Direct `stt` invocations
default to one thread and accept an explicit value from 1 through 64.

## Why a separate module

Keeping this in its own Go module means the pure-Go daemon never inherits a cgo
or native-toolchain dependency: it cross-compiles and lints as before. The
engine is designed to be built as a separate per-OS bundle, not linked in.

## Distribution and supply-chain status

There is currently **no supported release installation path** for this bundle.
`prukka setup` does not download, mirror or install it, and Prukka releases do
not attach it. The manual `build-engine` workflow validates an experimental
macOS build in an ephemeral runner but does not upload the bundle; it is not a
release or installation workflow.

- [`build.sh`](build.sh) fetches its inputs directly from their upstream
  projects (not yet from a prukka mirror). Every git source is pinned to an
  immutable commit, and every downloaded model and binary is checksum-verified.
- The remaining gap is the build-time Python wheels (`ctranslate2`,
  `sentencepiece`), used only to convert the Marian model and never shipped,
  which are not yet checksum-pinned.
- **No Python and no toolchain at runtime.** Python is used at *build* time only,
  to convert the Marian model to CTranslate2. The resulting runtime is compiled.
- The Opus-MT model uses **SentencePiece** (`spm32k`), so tokenization is one
  compiled step — no Moses/BPE/perl chain.

Pins live at the top of `build.sh` (git sources by commit, downloads by SHA-256):

| component | pinned to |
|---|---|
| whisper.cpp | commit `080bbbe8` (ggml-org) |
| CTranslate2 | commit `399239a7` = v4.8.1 (OpenNMT) |
| SentencePiece | commit `17d7580d` = v0.2.0 (google) |
| Piper | release `2023.11.14-2` + sha256 (rhasspy) |
| STT model | `ggml-base.bin` + sha256 (ggml-org) |
| MT model | Tatoeba `ita-eng/opus-2021-02-18` + sha256 (Helsinki-NLP) |
| TTS voice | `en_US-lessac-medium` + sha256 (rhasspy/piper-voices) |

## Bundle layout

`build.sh <out>` produces the folder the daemon points `config.Local.Bin` into:

```
prukka                 # this Go orchestrator (one binary, subcommands stt/mt/tts)
prukka-engine-manifest.json # declares the native bundle layout to Doctor
whisper-server         # whisper.cpp server
mt                     # CTranslate2 + SentencePiece translator wrapper
piper/                 # piper binary + its dylibs + espeak-ng-data
lib/                   # shared libs the helpers dlopen (whisper/ggml, ctranslate2, sentencepiece)
models/
  stt/ggml-base.bin
  mt-it-en/            # config.json, model.bin, shared_vocabulary.json, source.spm, target.spm
  tts/en_US-lessac-medium.onnx(.json)
```

Each helper is spawned with `DYLD_LIBRARY_PATH=<bundle>/lib` (Piper uses its own
dir). `mt` resolves its model by convention from `<bundle>/models/mt-<from>-<to>`.
More language pairs can be added by supplying converted `mt-<from>-<to>` model
folders and declaring the directed pair in `providers.local.mt.pairs` — the
runtime needs no new code.

`prukka doctor` first resolves the configured helper executable. Without an
adjacent `prukka-engine-manifest.json`, it reports a warning: a compatible
single-binary helper may implement the stdio protocol without this layout, but
native tools and model readiness have not been declared.

The native bundle manifest has this exact contract:

```json
{
  "schema": "prukka.engine.bundle",
  "version": 1,
  "kind": "native"
}
```

The object is limited to 4096 bytes; unknown or duplicate fields, trailing JSON
values and different schema, version or kind values are invalid. Only a valid
manifest enables layout validation of the native executable for every enabled
stage and the configured model files. An invalid manifest or an incomplete
declared layout fails Doctor. A complete layout remains a warning because these
checks do not start the helper or load models; successful lane startup is the
current runtime validation.

## Building locally

```sh
engine/build.sh /tmp/prukka-engine
```

Requires `git cmake clang++ curl unzip python3 go`. The experimental macOS
recipe links CTranslate2 against Accelerate (no MKL). Linux and Windows recipes
have not been implemented.

Running this script fetches unverified upstream artifacts as described above.
Treat its output as a local development artifact until every input is immutable
and verified and a supported release process is documented. The bundle lacks a
complete third-party license inventory and must not be redistributed.
