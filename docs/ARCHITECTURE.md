# Architecture

This document describes the current code, including incomplete distribution
work. It does not describe a future hosted provider or a complete packaged
speech product.

## System boundary

Prukka is one Go control/media process plus separately executed runtime tools:

```text
operator/client
    │
    ├─ local IPC gRPC ───────────────────────────────┐
    └─ loopback HTTP: REST / SSE / dashboard / media │
                                                     ▼
                                              cmd/prukka
                                                     │
        ┌────────────────────────────────────────────┼───────────────────┐
        ▼                                            ▼                   ▼
  session/runtime                              media ingest         control plane
        │                                            │
        ▼                                            ▼
  max-lane admission ◄──── PCM/source clock ── FFmpeg or WAV
        │
        ▼
  local STT helper → bounded MT/TTS call dispatcher
        │
        ▼
  local MT helper → optional local TTS helper
        │
        ▼
  captions / timeline mixer / HLS / push outputs
```

The Go binary remains the owner of session state, validation, scheduling,
timelines, output routing and control. Native inference runs outside the Go
process through a configured helper executable.

## Distribution status

`prukka setup` installs FFmpeg only. It does not install the speech helper,
whisper.cpp, CTranslate2/SentencePiece, Piper or models. The `engine/` source
defines a helper that orchestrates those dependencies, but the complete bundle
has no supported build/package/install flow in this repository.

Until `providers.local.bin` names an executable bundle with the required
models, the daemon and control plane start but media lanes report the speech
engine as unavailable. This is a visible capability failure, not a local-to-
cloud fallback.

## Package responsibilities

| Area | Responsibility |
|---|---|
| `cmd/prukka` | CLI commands and composition root; creates concrete adapters and owns process lifecycle |
| `internal/core` | Domain types, language rules, session state, engine ports, streaming orchestration, PCM/timeline operations and configuration |
| `internal/providers/native` | Adapts consumer-owned STT/MT/TTS interfaces to the configured helper subprocess protocol |
| `engine` | Source for the separately built helper/orchestrator; resolves native tools and models relative to its executable |
| `internal/dispatch`, `internal/ring` | Bounded MT/TTS call admission, worker execution and the MPMC queue |
| `internal/media/ffmpeg` | Managed FFmpeg install/resolution and supervised media processes |
| `internal/media/ingest` | Native WAV and FFmpeg-backed stream/device sources |
| `internal/media/egress` | Rolling WebVTT, HLS, mixed audio and push destinations |
| `internal/control` | gRPC, REST gateway, SSE, HTTP data plane, token checks and settings transactions |
| `internal/doctor` | Configuration, engine, FFmpeg and state-directory probes |
| `internal/observability` | Structured logging and Prometheus metrics |
| `internal/devices`, `drivers` | Device discovery plus platform-specific virtual-device install/source code |
| `web` | Svelte dashboard source; the built static bundle is embedded by `internal/webui` |

Interfaces belong to the consuming core package. Provider, ingest and egress
packages depend inward on those contracts; concrete construction stays in
`cmd/prukka`.

## Media flow

1. A session is validated and stored. Runtime status is observable as
   `starting`, `running`, `finished` or `failed`; returned source identity is
   sanitised.
2. The lane acquires one daemon-wide `max_lanes` slot and resolves the current
   immutable configuration snapshot. Configuration validation checks that a
   helper path is present, but the helper is not executed or fully validated
   until after the media source opens; startup failures then fail the lane.
3. A native WAV reader or supervised FFmpeg process produces canonical PCM.
   FFmpeg also supplies the video/media paths needed by HLS and push outputs.
4. The STT adapter starts one long-lived helper `stt` process under that lane
   slot, streams little-endian mono PCM on stdin and reads newline-delimited
   transcript events on stdout.
5. Committed text is translated per target language. Directed configured MT
   pairs are checked before spawn; same-base output bypasses MT. The shared
   worker/queue dispatcher bounds MT/TTS calls, and the native MT adapter keeps
   a warm helper per supported language pair inside its lane.
6. Caption sinks update the bounded direct WebVTT document and, when available,
   its HLS subtitle rendition.
7. With `providers.voices: local`, the TTS adapter keeps a warm process for the
   configured voice and returns PCM chunks only for targets compatible with
   `providers.local.tts.language`. Other targets remain caption-only. With
   `voices: off`, synthesis is skipped for every target.
8. Timeline and mixer stages place dubbed takes against the source clock and
   configured delay/bed. Egress serves or pushes the resulting media.

HLS creation is best-effort: failure is logged and direct caption/audio paths
can continue where their dependencies remain available. A missing speech
helper is not best-effort because STT and MT are required for a lane.

## Native helper contract

The configured executable receives one of three subcommands:

| Subcommand | Input | Output | Lifetime |
|---|---|---|---|
| `stt` | raw signed 16-bit mono PCM on stdin | newline-delimited JSON transcript events | one transcription session |
| `mt` | newline-delimited JSON text requests | newline-delimited JSON translations | warm per language pair |
| `tts` | newline-delimited JSON clauses | base64 PCM chunks plus turn boundary messages | warm per voice |

Child-process cancellation, pipe closure, output-size bounds and stderr tails
are owned by the adapter. Malformed STT events and terminal STT/MT/TTS process
or protocol failures fail the lane explicitly. The helper and every native
executable/library/model beside it are part of the operator's trusted computing
base.

The active provider configuration intentionally contains only:

- `providers.voices`, selecting `local` or `off`;
- `providers.local`: the helper executable path, STT model path, directed
  source-to-target MT pairs, TTS voice-model path and the concrete language that
  voice supports; and
- `providers.dispatch`: MT/TTS worker and queue bounds, the active-lane bound
  and the registered-session bound.

Translation models are resolved by the helper from its own bundle layout. Old
base URLs, remote-model tuning, formats, rates and timeouts are migration-only
fields and are removed on settings persistence.

## Configuration and live updates

Load order is built-in defaults, strict YAML, then supported environment
overrides. Unknown YAML fields fail startup. `config.Holder` publishes immutable
snapshots and persists edits with validation and atomic replacement.

The dashboard settings API currently exposes session defaults, the voice-stage
selector, STT/TTS model paths, directed MT pairs and the TTS language at the
protocol level. The UI deliberately exposes only session defaults; helper
provisioning, model layout and voice/language pairing require operator
filesystem work. Fields not present on the settings wire retain their file
values.

Runtime changes use a narrow change hook. Restart notes are returned when a
field cannot apply safely to existing work. A settings write is all-or-nothing.
Dispatch limits are file-only and restart-only; changing the file does not
retrofit new limits onto lanes already running under the startup snapshot.

## Concurrency and memory

- `providers.dispatch.max_sessions` caps all stored session definitions,
  including active and waiting sessions. It must be at least `max_lanes`.
- A daemon-wide weighted semaphore caps active long-lived lanes at
  `providers.dispatch.max_lanes`, including their STT helper and per-lane warm
  MT/TTS caches.
- Each STT helper receives a 1–4 thread budget derived from effective
  `GOMAXPROCS / max_lanes`; this avoids every lane independently claiming the
  complete host CPU budget.
- The global dispatcher separately bounds concurrent MT/TTS calls and queued
  jobs; a full queue applies backpressure instead of creating unbounded work.
- Helper reads and writes have a single documented owner. Warm MT/TTS processes
  serialize their protocol where required and are discarded after terminal
  failures.
- Session, stream and helper lifetimes derive from cancellation contexts.
- PCM conversion and mixer hot paths reuse caller-owned storage where their
  APIs permit it. Benchmarks enforce allocation budgets for named operations,
  not for the complete pipeline or every session lifecycle.
- Direct WebVTT documents and HLS state use bounded live windows; operators
  must still load-test configured session/language limits.

`make load` uses the host's full online CPU capacity by default. On
representative production hardware, `PRUKKA_LOAD_CPU_BUDGET_PERCENT` can impose
a stricter hard CPU SLO while retaining the measured process-tree CPU report.

`make pgo` creates `cmd/prukka/default.pgo` only from an explicit real-engine
workload, rejects retired/current-hot-path mismatches, and records source,
profile, toolchain, engine, FFmpeg and fixture digests. The maintainer lint gate
revalidates that provenance and source fingerprint before using a committed
profile. The repository does not ship a stale or protocol-double profile. PGO
is not an architectural substitute for measurement.

## Control and security boundaries

- gRPC uses local IPC and a per-install token.
- The HTTP listener defaults to loopback. REST mutations, configuration reads,
  local-device inventory and Doctor diagnostics require the token; HLS, direct
  audio/WebVTT and some other reads are intentionally unauthenticated and rely
  on the listener boundary.
- The host guard rejects foreign hosts on loopback binds. CORS permits one
  configured browser origin but is not authentication.
- API responses/events expose a sanitised source label. Full source and output
  URLs can still exist in process memory or child arguments and must be treated
  as sensitive.
- Network inputs, destinations, the hosted dashboard origin, FFmpeg and the
  native bundle are external trust boundaries.

See [Security policy](../SECURITY.md),
[Data protection and AI transparency](GDPR.md) and
[Dashboard accessibility](ACCESSIBILITY.md).

## State ownership

| State | Owner | Cleanup boundary |
|---|---|---|
| configuration and control token | platform config/state directories | operator rotation/removal or purge |
| PCM and direct caption windows | session/runtime memory | session removal or daemon exit |
| HLS rolling files | media state directory | session removal or graceful daemon shutdown; the next successful startup clears crash debris |
| managed FFmpeg | state directory | explicit purge/reinstall |
| helper/native models | operator-selected paths | operator lifecycle; not owned by setup |
| dashboard locale/token | browser local/session storage | browser/site-data lifecycle |

## Extension rules

When adding a source, provider or sink:

1. Add the smallest consumer-side interface only when existing contracts are
   insufficient.
2. Keep protocol/process/filesystem details in the adapter.
3. Validate and redact at the boundary; never echo secret-bearing URLs.
4. Define queue, memory, timeout and cancellation bounds before wiring it.
5. Add failure-path, race and lifecycle tests as well as the successful path.
6. Update configuration, API, dashboard and operator documentation together;
   do not expose a setting until it changes runtime behaviour.
