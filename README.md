<p align="center">
  <img src="assets/brand/prukka.svg" width="240" alt="Prukka — teal winged helmet">
</p>

# Prukka

Prukka is an actively developed, self-hosted Go application for real-time
multilingual captions and dubbing. It combines a bounded media pipeline, local
speech-engine adapters, HLS/WebVTT delivery, a gRPC/REST control plane and an
embedded Svelte dashboard.

[![Go Version](https://img.shields.io/badge/Go-1.26.5-blue.svg)](https://go.dev/)
[![CI](https://github.com/ubyte-source/prukka/actions/workflows/ci.yml/badge.svg)](https://github.com/ubyte-source/prukka/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-Apache--2.0%20%7C%20GPL--2.0--only-green.svg)](#license)

## Current status

The repository is **not a turnkey speech product yet**.

- `prukka setup` installs only the pinned, checksum-verified FFmpeg runtime.
- The native speech helper, whisper.cpp/CTranslate2/Piper executables and model
  files are not distributed by the standard setup or release archive.
- Speech lanes remain unavailable until an operator builds a compatible engine
  bundle and configures `providers.local.bin` and its model paths.
- One TTS voice and its supported language are configured at a time. A target
  with an available MT route but a different voice language remains
  caption-only; a target without a directed MT route is unavailable. The
  default English voice is not presented as multilingual synthesis.
- The daemon, CLI, control APIs and dashboard can be built and inspected without
  that bundle. Creating a media session will report the missing lane dependency.

Do not use the project for production or regulated workloads without an
independent security, privacy, accessibility and operational assessment.

## Implemented architecture

```text
file / RTMP / SRT / device
          │
          ▼
   supervised ingest ──► local STT ──► local MT ──► optional local TTS
          │                                  │                 │
          └──────────── source clock ────────┴──── mixer ──────┘
                                             │
                              WebVTT / HLS / RTMP / SRT / device

gRPC over local IPC ──► REST + SSE ──► embedded dashboard
```

Key boundaries:

- consumer-owned STT, translation and synthesis interfaces live in
  `internal/core/engine`;
- `internal/providers/native` runs one configured helper through `stt`, `mt`
  and `tts` subcommands over stdio;
- `providers.dispatch.max_sessions` caps stored active/waiting definitions,
  `max_lanes` caps concurrent long-lived STT lanes and their provider caches,
  and fixed workers plus a bounded queue admit MT/TTS calls;
- the FFmpeg supervisor owns external media-process execution;
- the control API uses a per-install token and local IPC; the HTTP listener
  defaults to loopback; and
- configuration is strictly decoded, validated, atomically persisted and
  swapped as an immutable runtime snapshot.

See [Architecture](docs/ARCHITECTURE.md) for package responsibilities.

## Build and inspect

Requirements:

- Go 1.26.5 or the version declared in `go.mod`;
- macOS, Linux or Windows on a supported architecture; and
- Node only when rebuilding or testing the dashboard. `make tools-node`
  installs the repository-pinned toolchain.

```bash
git clone https://github.com/ubyte-source/prukka.git
cd prukka
make tools
make build
./bin/prukka setup
./bin/prukka doctor
./bin/prukka up
```

The dashboard is served at `http://127.0.0.1:8080/ui/` by default. `doctor`
prints the effective configuration/state locations and reports the missing
speech bundle explicitly.

Useful control-plane commands:

```bash
./bin/prukka session add demo \
  --in rtmp://0.0.0.0:1935/in/demo \
  --langs it,en,de
./bin/prukka session list
./bin/prukka session langs demo +fr -de
./bin/prukka session remove demo
./bin/prukka stats
./bin/prukka devices status
```

Run `./bin/prukka <command> --help` for the authoritative flags. Source URLs
support `file://`, `rtmp://`, `srt://` and supported `device://` forms.

## Configure the local speech bundle

The helper source under `engine/` defines the stdio contract and orchestrates
whisper.cpp, CTranslate2/SentencePiece and Piper. The repository currently does
not provide a supported build/package procedure for the complete bundle.
`prukka doctor` resolves the configured helper first. A compatible single
binary without an adjacent `prukka-engine-manifest.json` produces a warning,
because native tools and model readiness are not declared. The experimental
`engine/build.sh` output includes the versioned manifest; only then does Doctor
validate the native executables needed by enabled stages and their configured
model files. These checks are static: even a complete layout remains a warning
because Doctor does not start the helper or load models. A real lane is the
current runtime validation.

Model paths may be absolute or relative to the helper executable's directory.
The current schema is:

```yaml
providers:
  voices: local                 # local or off
  local:
    bin: /absolute/path/to/engine/prukka
    stt:
      model: models/stt/ggml-base.bin
    mt:
      pairs:
        - from: it
          to: en
    tts:
      language: en
      voice: models/tts/en_US-lessac-medium.onnx
  dispatch:
    workers: 8
    queue: 256
    max_lanes: 4
    max_sessions: 32

defaults:
  langs: [it, en]
  subs: vtt                     # off, vtt or burn
  bed: -15dB
  delay: 8s
```

Retired pre-release fields are accepted only for migration and are removed on
the next successful settings save. They are not runtime features. The
dashboard exposes effective session defaults; engine binary/model/voice
provisioning remains an operator task.

MT pairs are directed: the default bundle layout declares `it` → `en`, not the
reverse. Output in the same base language needs no MT model. For a concrete
source the dashboard disables targets without a route; with auto-detection it
shows the cautious union of configured pair endpoints and the daemon validates
the detected source → target direction before starting translation.

`max_sessions` bounds every registered definition, including work waiting for
an active-lane slot, and must be at least `max_lanes`. All dispatch limits are
read at daemon startup; changing the YAML requires a restart and does not alter
lanes already admitted under the previous limits.

Daemon settings load built-in defaults, a strict YAML file, then supported
`PRUKKA_*` environment overrides. CLI flags select the configuration file and
log level. Unknown YAML fields are rejected.

## Data and control endpoints

For a running session, the HTTP data plane can expose:

| Path | Content |
|---|---|
| `/{session}/master.m3u8` | HLS master playlist |
| `/{session}/audio/{lang}/index.m3u8` | HLS dubbed-audio rendition, when available |
| `/{session}/subs/{lang}/index.m3u8` | HLS WebVTT subtitle rendition |
| `/{session}/video/index.m3u8` | HLS video rendition, when available |
| `/{session}/{lang}/audio.ts` | live audio-only MPEG-TS |
| `/{session}/{lang}/subs.vtt` | rolling WebVTT document |
| `/api/v1/` | REST mirror of the control API |
| `/api/v1/events` | Server-Sent Events |
| `/healthz` | liveness |
| `/metrics` | Prometheus metrics |

Control mutations, configuration reads, local-device enumeration and
`/api/v1/doctor` diagnostics require the install token.
Session list/event payloads expose a sanitised source label instead of source
credentials, paths or query strings. HLS, direct audio and WebVTT reads do not
require the token and rely on the HTTP listener remaining inside the intended
trust boundary.

## Dashboard

The dashboard supports session creation/removal, language changes, output
pushes, daemon diagnostics, event status and session-default settings. It has
English and Italian UI messages and an engineering target of WCAG 2.2 AA.

No formal accessibility certification is claimed. See
[Dashboard accessibility](docs/ACCESSIBILITY.md) for legal scope, implemented
behaviour, known limits and the required manual test matrix.

## Privacy and security

When a compatible local helper is configured, inference is designed to stay on
the host. Media still crosses configured source/output routes and can be read
through media endpoints if the listener is reachable. Local-only processing
does not remove GDPR obligations.

Read:

- [Data protection and AI transparency](docs/GDPR.md)
- [Security policy](SECURITY.md)
- [Managed FFmpeg runtime](docs/FFMPEG.md)

Never expose the daemon directly to an untrusted network or publish the control
token or stream URLs. The repository makes no blanket claim of GDPR, AI Act,
EAA, WCAG or sector-specific compliance.

## Engineering checks

```bash
make test       # race-enabled Go tests and test-mapping gate
make lint       # blocking lint and workflow checks
make bench      # allocation/performance gate for designated hot paths
make cover-gate # blocking coverage floors for critical packages
make cover      # coverage reports for daemon and speech-engine Go modules
make web        # rebuild the embedded dashboard
make web-e2e    # Playwright against a real daemon
```

`make pgo` captures a real-engine workload, validates current hot symbols and
then rebuilds with Go PGO. No stale or protocol-double profile is shipped.
Benchmark allocation assertions apply only to the named hot paths under their
test inputs; they are not a whole-application zero-allocation claim.

Contribution rules are in [CONTRIBUTING.md](CONTRIBUTING.md). Notable changes
are recorded in [CHANGELOG.md](CHANGELOG.md).

## License

The application and macOS/Windows drivers are Apache-2.0. Linux kernel modules
are GPL-2.0-only. Managed FFmpeg is a separately downloaded GPL executable.
See [LICENSE](LICENSE), [drivers/linux/LICENSE](drivers/linux/LICENSE),
[NOTICE.txt](NOTICE.txt) and [docs/FFMPEG.md](docs/FFMPEG.md).
