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

`prukka setup` installs FFmpeg, the managed native speech tools and the
model packs the configuration needs (macOS today). Artifacts come from
prukka's own GitHub release — the release whose tag equals the daemon
version — are SHA-256 verified against the `prukka-engine-catalog.json`
asset of that same release and are staged and published atomically under
`<state>/engine/`; `internal/speech` owns download, inventory and removal,
and the dashboard's Languages section drives pack installs and removals over
the control API with progress on the events stream. An explicit
`providers.local.bin` always overrides the managed install.

On platforms without a published runtime, or before setup runs, the daemon
and control plane start but media lanes report the speech engine as
unavailable. This is a visible capability failure, not a local-to-cloud
fallback.

## Package responsibilities

| Area | Responsibility |
|---|---|
| `cmd/prukka` | CLI commands and composition root; creates concrete adapters and owns process lifecycle |
| `internal/core` | Domain types, language rules, session state, engine ports, streaming orchestration, PCM/timeline operations and configuration |
| `internal/providers/native` | Adapts consumer-owned STT/MT/TTS interfaces to the configured helper subprocess protocol |
| `internal/speechengine` | Speech-engine orchestrator, run as hidden `stt`/`mt`/`tts` subcommands the single `prukka` binary self-executes; resolves native tools and models relative to its executable |
| `engine` | Native-tool build recipe (build.sh, packs.sh, pins.sh, mt.cpp, patches) that produces the release assets; not a Go module and builds no orchestrator binary |
| `internal/dispatch` | Bounded MT/TTS call admission: a fixed worker pool over a buffered job queue |
| `internal/media/ffmpeg` | Managed FFmpeg install/resolution and supervised media processes |
| `internal/speech` | Managed engine catalog, verified downloads, atomic bundle/pack install and inventory |
| `internal/media/ingest` | Native WAV and FFmpeg-backed stream/device sources |
| `internal/media/egress` | Rolling WebVTT, HLS, mixed audio and push destinations |
| `internal/control` | gRPC, REST gateway, SSE, HTTP data plane, token checks, settings and engine transactions |
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
   helper path is present, but a real lane remains the full runtime validation.
   A call lane starts its required directed MT helpers and selected TTS voices
   concurrently at this point, before capture opens, so first-turn model loads
   do not sit on the live path. A warm-up failure fails the lane.
3. The STT adapter starts one long-lived helper `stt` process under that lane
   slot and waits for its readiness handshake.
4. Only after STT is ready does the engine's first frame request lazily call
   ingress `Open`: a native WAV reader or supervised FFmpeg process then
   produces canonical little-endian mono PCM. This ordering prevents model
   startup from accumulating stale live-capture audio. FFmpeg also supplies the
   video/media paths needed by HLS and push outputs. The STT adapter streams
   that PCM on stdin and reads newline-delimited transcript events on stdout.
5. Committed text is translated per target language. Directed configured MT
   pairs are checked before spawn; same-base output bypasses MT. The shared
   worker/queue dispatcher bounds MT/TTS calls, and the native MT adapter keeps
   a warm helper per supported language pair inside its lane.
6. When subtitles are enabled, caption sinks update the bounded direct WebVTT
   document and, when available, its HLS subtitle rendition. The call profile
   forces subtitles off, so call lanes register no WebVTT sink. Calls sourced
   from `device://audio/` also skip the rolling HLS/AAC tree while retaining
   direct dubbed audio and audio-device pushes; AV calls retain the video tree
   required by video/device pushes, while network/file calls and broadcast lanes
   continue to create HLS best-effort.
7. With `providers.voices: local`, the TTS adapter keeps one warm process per
   voice in `providers.local.tts.voices` and each dubbed target speaks through
   the voice configured for its language, so one lane can dub several targets
   and a two-way call dubs both directions. Targets without a configured voice
   remain caption-only. With `voices: off`, synthesis is skipped for every
   target.
8. Timeline and mixer stages place dubbed takes against the source clock and
   configured delay/bed. Egress serves or pushes the resulting media.

HLS creation is best-effort: failure is logged and direct caption/audio paths
can continue where their dependencies remain available. A missing speech
helper is not best-effort because STT and MT are required for a lane.

The call profile uses a latency-bounded quality policy: ingest PCM and
dubbed-audio feeds advance in 20 ms quanta, mixer tracks use a 40 ms playout
cushion, and STT uses a 300 ms silence hang, 5 s maximum segment window,
250 ms minimum speech run and a 5 s partial stride. Making the partial stride
equal to the hard segment ceiling suppresses speculative inference on the
CPU-only call path; natural silence normally endpoints a turn first. The
longer ceiling avoids cutting ordinary words in half. Windows DirectShow and
Linux PulseAudio capture request a 20 ms native fragment for calls;
AVFoundation has no matching FFmpeg knob and retains its device default. Darwin
device PCM follows capture
timestamps through an asynchronous resampler, preserving the sample clock when
the pinned FFmpeg AVFoundation demuxer skips a callback; any already-lost frame
is represented as silence until the managed runtime includes the upstream
demuxer fix. Call playback requests a 40 ms buffer from Linux PulseAudio and
native Windows WASAPI. Broadcast retains its 100 ms feed and backend playback
defaults (including the existing 200 ms WASAPI buffer). Audio-only MPEG-TS and
SRT outputs disable mux delay/preload and flush packets promptly.
These are profile policies rather than user-configurable latency promises:
model speed, host load, audio drivers and the receiving application still
contribute delay.
Fresh and unrelated partial configurations select the bundle's multilingual
base model through `providers.local.stt.call_model`. Historical one-model
configurations that explicitly set `stt.model` but omit or null the call
override retain their primary-model fallback; broadcast lanes always use the
primary model. The quantized tiny model remains an explicit low-resource
option, not the quality default. The schema does not infer capability or
quality from a filename. Call decoding uses greedy best-of-one, a single
temperature pass and a 512-position (10.24 s) audio context, leaving headroom
around the 5 s endpoint without paying for the fixed 30 s context. Token
timestamps remain enabled because canceling the pinned whisper.cpp build in
no-timestamps mode can poison
the reused server. Superseded partials are gated from output but allowed to
leave that server cleanly before the final inference begins.

## Native helper contract

The configured executable receives one of three subcommands:

| Subcommand | Input | Output | Lifetime |
|---|---|---|---|
| `stt` | raw signed 16-bit mono PCM on stdin | a readiness message, then newline-delimited JSON transcript events | one transcription session |
| `mt` | newline-delimited JSON text requests | newline-delimited JSON translations | warm per language pair |
| `tts` | newline-delimited JSON clauses | base64 PCM chunks plus turn boundary messages | warm per voice |

Child-process cancellation, pipe closure, output-size bounds and stderr tails
are owned by the adapter. Malformed STT events and terminal STT/MT/TTS process
or protocol failures fail the lane explicitly. The helper and every native
executable/library/model beside it are part of the operator's trusted computing
base.

For STT, the adapter invokes `stt --protocol-version 2`; older helpers are
rejected with a rebuild error instead of being allowed to hang during startup.
Readiness is the sole-field `{"ready":true}` message emitted only after the
helper's whisper server has loaded and passed its health probe. Every partial
and final event must carry a non-negative, exclusive cumulative `end_samples`
boundary. The adapter maps that boundary back to the source PCM timeline so
inference and queueing delay do not move the media timestamp. The adapter
bounds the readiness wait and does not begin PCM delivery first. Call lanes also
prewarm every concrete source-to-target MT pair and configured voice they will
use; the MT and TTS warm-ups run concurrently and their long-lived processes
are then reused for live clauses. Warm-up jobs use the same daemon-wide worker
and queue bounds as live MT/TTS work, and startup bounds the whole warm-up to
30 seconds. Auto-detected sources cannot prewarm an MT direction until the
source language is known.

The active provider configuration intentionally contains only:

- `providers.voices`, selecting `local` or `off`;
- `providers.local`: the helper executable path, primary and optional call STT
  model paths, directed source-to-target MT pairs, TTS voice-model paths and
  the concrete language each voice supports; and
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
selector, the primary STT model, directed MT pairs and TTS voice languages at
the protocol level. The optional call model is file-only. The UI deliberately
exposes only session defaults; helper provisioning, model layout and
voice/language pairing require operator filesystem work. Fields not present on
the settings wire retain their file values.

Runtime changes use a narrow change hook. Restart notes are returned when a
field cannot apply safely to existing work. A settings write is all-or-nothing.
Dispatch limits are file-only and restart-only; changing the file does not
retrofit new limits onto lanes already running under the startup snapshot.

## Concurrency and memory

- `providers.dispatch.max_sessions` caps all stored session definitions,
  including active and waiting sessions. It must be at least `max_lanes`.
- A daemon-wide weighted semaphore caps active long-lived lanes at
  `providers.dispatch.max_lanes`, including their STT helper and per-lane warm
  MT/TTS caches. The built-in `max_lanes` default is 2, which leaves room for
  the incoming and outgoing lanes of one two-way call.
- Broadcast STT helpers divide effective `GOMAXPROCS` by `max_lanes` and clamp
  the result to 1–4 threads. A call pair shares the host's effective CPU budget,
  clamped to 1–8 threads per helper, because its two sides are normally
  turn-taking; additional configured pairs divide that budget so multi-call
  deployments do not oversubscribe without bound. Simultaneous speakers can
  still contend, but work remains bounded by the lane and per-helper caps.
- The global dispatcher separately bounds concurrent MT/TTS calls, model
  warm-ups and queued jobs; a full queue applies backpressure instead of
  creating unbounded work.
- Helper reads and writes have a single documented owner. Warm MT/TTS processes
  serialize their protocol where required and are discarded after terminal
  failures.
- Session, stream and helper lifetimes derive from cancellation contexts.
- PCM conversion and mixer hot paths reuse caller-owned storage where their
  APIs permit it. Benchmarks enforce allocation budgets for named operations,
  not for the complete pipeline or every session lifecycle.
- Direct WebVTT documents and HLS state use bounded live windows; operators
  must still load-test configured session/language limits.

`prukka_stt_inference_seconds` measures successful local Whisper partial and
final requests. The legacy-named `prukka_e2e_latency_seconds` starts at source
clause commit and ends at caption publication or transactional placement of a
complete synthesized take; failed or partial takes do not count as delivered.
It does not include capture, STT, device playback or receiving-app buffering.
External capture-to-speaker measurement is still required before claiming a
conversational end-to-end latency budget.

Structured `lane startup` records expose the bounded phases
`providers_warming`, `transcription_warming` and `waiting_for_media`, their
ready/failed transitions and millisecond durations. They contain
session/profile identity and task counts only; source URLs, model/voice paths
and provider error details remain outside these aggregate records.

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
| managed engine and model packs | state directory (`engine/`) | dashboard/API pack removal or explicit purge; `prukka setup` repairs |
| operator-built helper/models | operator-selected paths | operator lifecycle; not owned by setup |
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
