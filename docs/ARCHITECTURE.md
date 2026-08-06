# Architecture

This document describes the code at this commit: how one session's audio
becomes translated speech, what each package owns, and where the boundaries
are enforced. It documents no unimplemented work.

## Reading path

A session's audio enters through one source URL and leaves as translated
speech. Six files carry it, and read in this order each answers one question:

1. `cmd/prukka/daemon.go` — **what does the process own, and for how long?**
   `newDaemonRuntime` takes the configuration holder its caller built and
   assembles the other singletons that outlive every session — the session
   store, the caption/audio/media registries, the dispatch pool, the
   `max_lanes` semaphore — then hands them to `lane.NewStarter`. Nothing
   session-specific is built here.
2. `internal/core/session/runtime.go` — **which lane runs, and which one may
   write?** `Runtime.Run` reacts to store events and owns one goroutine per
   running lane. `laneID` — slug, revision, generation — is the identity the
   store binds, so a superseded incarnation's late write is refused instead of
   overwriting its successor's state.
3. `internal/lane/lane.go` — **how is one lane assembled?** `NewStarter` takes
   a `max_lanes` slot and reads the live configuration snapshot; `run`
   owns the assembled lane until the source ends or the session is canceled.
   Its file header names the layers it reaches for — `providers.go`,
   `warm.go`, `source.go`, `outputs.go` — and each of those opens by saying
   what it owns.
4. `internal/lane/source.go` — **where does the audio come in?** `ingressFor`
   picks the ingress from the URL scheme: `internal/media/ingest/file` reads a
   WAV natively, `internal/media/ingest/stream` runs a supervised FFmpeg
   process for every other supported source, `rtmp://`, `srt://` and
   `device://` among them. The open is deferred to the lane's first frame
   request, because opening a capture device starts capture.
5. `internal/core/realtime/lane.go` — **how do the three stages overlap?**
   `Lane.Run` opens the transcription, then runs a pump (source PCM into the
   transcriber, source audio onto the bed) beside a consumer that feeds one
   serial worker per target language. `speak.go` is the voice stage: it holds
   a take until the provider reports terminal success, so no partial speech
   is ever published.
6. `internal/media/egress/audio/audio.go` — **where does the dubbed voice
   become something a listener can reach?** `Registry` holds one playout per
   session and language, and every consumer draws from it through its own
   cursor: an HLS rendition (`StartHLS`), a direct MPEG-TS reader
   (`ServeTS`), or a push to a network URL or local audio device (`Push`).

Two files are worth reading before those six, because the rest is written in
their vocabulary: `internal/core/ports.go` (`Ingress`, `Frames`) and
`internal/core/realtime/ports.go` (`Transcriber`, `Translator`,
`Synthesizer`). They are the contracts the adapters implement, and
`hack/ci/core-boundary-gate.sh` is what keeps the dependency arrow pointing
at them.

Captions travel the same path to a different sink: `internal/lane/outputs.go`
builds one caption sink per language over `internal/media/egress/vtt` and,
when the HLS tree exists, `internal/media/egress/hls`.

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

## Runtime dependencies

`prukka setup` installs what the binary executes but does not contain. Two
artifact families, two separate supply chains:

- **Speech tools and model packs** come from prukka's own GitHub release —
  the one whose tag equals the daemon version — SHA-256 verified against the
  `prukka-engine-catalog.json` asset of that same release, then staged and
  published atomically under `<state>/engine/`. `internal/speech` owns the
  download, inventory and removal; the dashboard's Languages section drives
  pack installs and removals over the control API, with progress on the
  events stream. Runtimes are published for macOS and Linux on amd64 and
  arm64, and for Windows on amd64.
- **FFmpeg** is not in that release. `internal/media/ffmpeg` downloads the
  platform pin declared in its own build map straight from the upstream
  vendor and verifies the compiled-in SHA-256; the provenance is recorded in
  [Managed FFmpeg runtime](FFMPEG.md).

An explicit `providers.local.bin` overrides the managed speech install.
Before setup runs, or on a platform with no published runtime, the daemon and
control plane start and media lanes report the speech engine as unavailable —
there is no remote fallback that would hide it.

## Package responsibilities

| Area | Responsibility |
|---|---|
| `cmd/prukka` | CLI commands and composition root; builds the process-scoped singletons (config holder, caption/audio/media registries, dispatch pool, lane semaphore) and owns process lifecycle |
| `internal/core` | Domain types and the ports adapters implement (`internal/core/ports.go`), plus the strictly decoded configuration schema in `internal/core/config` |
| `internal/core/session` | Session definitions and their validation, the store that stamps a revision on every accepted write, and the runtime that starts, retries and stops one lane per session |
| `internal/core/realtime` | The streaming lane: the provider ports (`Transcriber`, `Translator`, `Synthesizer`) and the orchestration that overlaps transcription, translation and synthesis |
| `internal/core/pipeline` | The audio stages a lane lays its takes on: track scheduling and playout, bed mixing and ducking, the bounded call voice queue, PCM coding in one canonical format |
| `internal/lane` | Assembles and runs one session's lane: ingress resolution, provider construction and warm-up, capability admission and driving `internal/core/realtime`; a lane's concrete providers, ingress and sinks are built here |
| `internal/providers/native` | Adapts consumer-owned STT/MT/TTS interfaces to the configured helper subprocess protocol |
| `internal/providers/pivot` | English-hub MT routing: `pivot.Supported` judges a route (direct pair, same base, or source-to-English-to-target) and the decorator serves a bridged route as two hub legs |
| `internal/providers/bounded` | Decorates the translator and synthesizer with the daemon-wide dispatch pool, so every live MT/TTS call is admitted through the shared workers and queue and a released lane closes its provider exactly once for every in-flight caller |
| `internal/dispatch` | Bounded MT/TTS call admission: a fixed worker pool over a buffered job queue |
| `internal/nativewire` | Single source of the daemon/helper contract: message shapes, subcommand verbs, helper flag names and the protocol version, imported by the daemon, the helper and the protocol fixture so no side can spell the wire or the argv half differently |
| `internal/enginebundle` | On-disk engine-bundle layout (helper executable names, model directory structure) shared by the installer, the engine helpers and Doctor |
| `internal/speechengine` | Speech-engine orchestrator, run as hidden `stt`/`mt`/`tts` subcommands the single `prukka` binary self-executes; resolves native tools and models relative to its executable |
| `engine` | Native-tool build recipe (build.sh, packs.sh, lib.sh, pins.sh, mt.cpp, patches) that produces the release assets; not a Go module and builds no orchestrator binary |
| `internal/media/ingest` | Native WAV files (`internal/media/ingest/file`) and FFmpeg-backed stream and device sources (`internal/media/ingest/stream`) |
| `internal/media/egress` | Rolling WebVTT documents (`internal/media/egress/vtt`), the HLS tree (`internal/media/egress/hls`) and the dubbed-audio playout registry with its push targets (`internal/media/egress/audio`) |
| `internal/media/ffmpeg` | Managed FFmpeg install/resolution and supervised media processes |
| `internal/media/wasapi` | Windows playback endpoint, written because FFmpeg ships no playback muxer there |
| `internal/speech` | Managed engine catalog, verified downloads, atomic bundle/pack install and inventory |
| `internal/control` | gRPC, REST gateway, SSE, HTTP data plane, token checks, settings and engine transactions; the generated gRPC and gateway code is `internal/gen` |
| `internal/doctor` | Configuration, engine, FFmpeg and state-directory probes |
| `internal/observability` | Structured logging and Prometheus metrics |
| `internal/devices`, `internal/media/discover`, `drivers` | Virtual-device install/removal, capture and playback inventory, and the platform driver sources |
| `internal/osservice` | Installs, removes and inspects the per-user service that runs the daemon: a launchd agent, a systemd user unit or a Windows scheduled logon task. It is the operating system's notion of a service, unrelated to `control.Service` |
| `internal/update`, `internal/tray` | The explicit self-update — fetch, verify against published checksums, replace atomically, never automatic — and the tray companion |
| `web` | Svelte dashboard source; the built static bundle is embedded by `internal/webui` |
| `hack` | The tooling `make` drives: the blocking gate scripts under `hack/ci`, the demo, load and PGO drivers, and six Go commands under `hack/cmd` (comment gate, deterministic tar, engine catalog, literal gate, protocol engine, release SBOM) |

A few small packages exist so that a rule has exactly one spelling, and are
read from anywhere: `internal/core/lang` (the language registry the
dropdowns, the CLI and the API all validate against),
`internal/media/deviceurl` (the `device://` grammar), `internal/redact` (how
much of a URL any message may show), `internal/fetch` (the one hardened
downloader), `internal/strictjson` (unknown fields, trailing data and
duplicate keys rejected), `internal/procio` (the stdio plumbing every
supervised child shares), `internal/paths` and `internal/hostos` (platform
locations and the OS branch) and `internal/besteffort` (an error the caller
has decided not to act on). `internal/testkit` holds the fixtures shared
across test packages and is the one package production code must not import.

Interfaces belong to the consuming core package. Provider, ingest and egress
packages depend inward on those contracts; `cmd/prukka` constructs only the
process-scoped singletons, while `internal/lane` constructs everything a
single lane needs. `hack/ci/core-boundary-gate.sh` holds that direction:
nothing under `internal/core` may reach an adapter, with `internal/core/config`
— the daemon's YAML on disk — named in the gate as the single exception.

## Media flow

1. A session is validated and stored. Runtime status is observable as
   `starting`, `running`, `finished` or `failed`; returned source identity is
   sanitised.
2. The lane acquires one daemon-wide `max_lanes` slot and resolves the current
   immutable configuration snapshot. Configuration validation checks that a
   helper path is present, but a real lane remains the full runtime validation.
   A call lane pays its MT and TTS warm-up here, before capture opens, so
   first-turn model loads do not sit on the live path; a warm-up failure fails
   the lane.
3. The STT adapter starts one long-lived helper `stt` process under that lane
   slot and waits for its readiness handshake.
4. Only after STT is ready does the lane's first frame request lazily call
   ingress `Open`: a native WAV reader or supervised FFmpeg process then
   produces canonical little-endian mono PCM. This ordering prevents model
   startup from accumulating stale live-capture audio. FFmpeg also supplies the
   video/media paths needed by HLS and push outputs. The STT adapter streams
   that PCM on stdin and reads newline-delimited transcript events on stdout.
5. Committed text is translated per target language. MT capability is judged
   by the pivot-aware `pivot.Supported` predicate before spawn: a route is
   served by a directed configured pair, bypasses MT for same-base output, or
   is bridged through the English hub (source-to-English-to-target) by
   `internal/providers/pivot` — which is why the bundle ships only
   English-paired models yet 29 languages translate any-to-any. Admission,
   warm-up and live translation share that single predicate, and the dashboard
   mirrors it in `web/src/lib/capabilities.ts`. The shared worker/queue
   dispatcher bounds MT/TTS calls, and the
   native MT adapter keeps a warm helper per supported language pair inside
   its lane.
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
8. On a broadcast lane, timeline and mixer stages place dubbed takes against the
   source clock and configured delay/bed. A call lane has no bed and no mixer:
   each target's takes enter a bounded voice queue that speaks them as they are
   synthesized. Egress serves or pushes the resulting media.

HLS creation is best-effort: failure is logged and direct caption/audio paths
can continue where their dependencies remain available. A missing speech
helper is not best-effort because STT and MT are required for a lane.

### Profile policies: call and broadcast

A session runs under one of two profiles. The call profile bounds delay
everywhere it can; broadcast buys stability with a cushion.

| Stage | Call | Broadcast |
|---|---|---|
| ingest PCM and dubbed-audio feed | 20 ms quanta | 100 ms quanta |
| dubbed playout | a bounded voice queue with no cushion: takes play in arrival order and the unplayed backlog is capped at 3 s, past which the queue sheds its stalest audio rather than speak it late | mixer tracks hold 300 ms of audio ahead of the live edge, or the configured session delay when that is larger |
| STT endpointing | 300 ms silence hang, sentence-sized 5 s maximum window, 250 ms minimum speech run | the helper's own defaults: the daemon serializes no tuning flags |
| Whisper decoding | greedy best-of-one, one temperature pass and a 512-position (10.24 s) audio context: headroom around the 5 s endpoint without paying for the fixed 30 s context | endpointed finals keep the helper's defaults — full context, temperature-fallback ladder; live-window snapshots decode in one temperature pass and, while the tuned window fits half the 512-position span, inside that bounded context, because the wait-k committer needs their cadence and the final re-decodes the same audio at full quality |
| device capture | a 20 ms native fragment requested from Windows DirectShow and Linux PulseAudio | the device defaults |
| device playback | two feed quanta held in the platform layer: 40 ms on the native Windows WASAPI sink | 200 ms there, the same rule over its 100 ms quanta |

FFmpeg's AVFoundation input exposes no capture-buffer option, so macOS keeps
its device default under both profiles. macOS device PCM instead follows the
capture timestamps through an asynchronous resampler, so a skipped demuxer
callback becomes silence rather than a permanently shortened stream.
Audio-only MPEG-TS and SRT outputs disable mux delay and preload and flush
packets promptly.

These are profile policies, not latency promises: model speed, host load,
audio drivers and the receiving application all add delay the daemon does not
control.

### The decode loop

One inference goroutine owns the whisper decode loop, and four rules keep its
output ordered and its latency bounded:

- Endpointed finals queue in order and are never dropped.
- The live window occupies a single newest-wins slot, consumed only when no
  final waits.
- A live snapshot is decoded only while its `end_samples` boundary still
  exceeds the last final's, so emitted boundaries stay monotone.
- A superseded snapshot is dropped before dispatch, never aborted in flight:
  disconnecting whisper.cpp mid-inference poisons its next decode. Only the
  bounded inference deadline cuts a decode short.

Token timestamps stay enabled for the same reason: with a bounded audio
context, the no-timestamps mode of the pinned whisper.cpp can poison the next
decode after a canceled request. Partials are therefore opportunistic — paced
by decode wall time, not by a timer — and natural silence normally endpoints a
turn first.

### Which STT model a lane loads

- `providers.local.stt.model` is the primary model, and broadcast lanes
  always use it.
- `providers.local.stt.call_model` overrides it for call lanes. The built-in
  default sets both to the bundled multilingual `models/stt/ggml-base.bin`,
  and a configuration that mentions neither key keeps that default.
- A configuration that names `stt.model` and omits or nulls the call override
  serves calls with the primary model, so a single-model bundle keeps working.
- The quantized tiny model is an explicit low-resource choice. The schema
  infers nothing about capability or quality from a filename.

## Native helper contract

The configured executable receives one of three subcommands:

| Subcommand | Input | Output | Lifetime |
|---|---|---|---|
| `stt` | raw signed 16-bit mono PCM on stdin | a readiness message, then newline-delimited JSON transcript events | one transcription session |
| `mt` | newline-delimited JSON text requests | newline-delimited JSON translations | warm per language pair |
| `tts` | newline-delimited JSON clauses | base64 PCM chunks plus turn boundary messages | warm per voice |

Child-process cancellation, pipe closure, output-size bounds and stderr tails
are owned by the adapter. Malformed STT events and terminal STT/MT/TTS process
or protocol failures fail the lane explicitly.

The STT handshake, which the other two subcommands have no equivalent of:

- The adapter invokes `stt --protocol-version 2`. An older helper is rejected
  with a rebuild error instead of being allowed to hang during startup.
- Readiness is the sole-field `{"ready":true}` message, emitted only after the
  helper's whisper server has loaded and passed its health probe. The adapter
  bounds that wait and delivers no PCM before it resolves.
- Every partial and final event carries a non-negative, exclusive cumulative
  `end_samples` boundary, which the adapter maps back onto the source PCM
  timeline so inference and queueing delay cannot move a media timestamp.

Warm-up, which a call lane pays before capture opens:

- A call lane prewarms every MT route and configured voice it will use; a
  route with no direct model warms both of its English hub legs.
- MT and TTS warm-ups run concurrently, and their long-lived processes are
  reused for live clauses.
- Warm-up jobs go through the same daemon-wide worker and queue bounds as live
  MT/TTS work, and startup bounds the whole warm-up to 30 seconds.
- An auto-detected source cannot prewarm an MT direction until the source
  language is known.

The active provider configuration intentionally contains only:

- `providers.voices`, selecting `local` or `off`;
- `providers.local`: the helper executable path, primary and optional call STT
  model paths, directed source-to-target MT pairs, TTS voice-model paths and
  the concrete language each voice supports; and
- `providers.dispatch`: MT/TTS worker and queue bounds, the active-lane bound
  and the registered-session bound.

Translation models are resolved by the helper from its own bundle layout.
Provider base-URL, remote-model tuning, format, rate and timeout fields are
tolerated on load but are not part of the active configuration; settings
persistence removes them.

## Configuration and live updates

Load order is built-in defaults, strict YAML, then supported environment
overrides. Unknown YAML fields fail startup. `config.Holder` publishes immutable
snapshots and persists edits with validation and atomic replacement.

The dashboard settings API exposes session defaults, the voice-stage
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
  deployments do not oversubscribe without bound.
- The global dispatcher separately bounds concurrent MT/TTS calls, model
  warm-ups and queued jobs; a full queue applies backpressure instead of
  creating unbounded work.
- Helper reads and writes have a single documented owner. Warm MT/TTS processes
  serialize their protocol where required and are discarded after terminal
  failures.
- Session, stream and helper lifetimes derive from cancellation contexts.
- PCM conversion and mixer hot paths reuse caller-owned storage where their
  APIs permit it, and benchmarks enforce those allocation budgets.
- Direct WebVTT documents and HLS state use bounded live windows.

What the two pipeline metrics measure:

- `prukka_stt_inference_seconds` — successful local Whisper partial and final
  requests.
- `prukka_post_commit_latency_seconds` — from source clause commit to caption
  publication or to the transactional placement of a complete synthesized
  take. A failed or partial take does not count as delivered, and the
  measurement excludes capture, STT, device playback and receiving-app
  buffering.

Structured `lane startup` records expose the bounded phases
`providers_warming`, `transcription_warming` and `waiting_for_media`, their
ready/failed transitions and millisecond durations. They contain
session/profile identity and task counts only; source URLs, model/voice paths
and provider error details remain outside these aggregate records.

`make load` drives the real-engine acceptance load at the host's full online
CPU capacity, and `PRUKKA_LOAD_CPU_BUDGET_PERCENT` caps it on production-like
hardware. `make pgo` rebuilds `cmd/prukka/default.pgo` from a real-engine
workload and records the provenance `hack/ci/pgo-profile-gate.sh` revalidates
on every `make lint` and `make build`.

## Control and security boundaries

- gRPC uses local IPC and a per-install token.
- The HTTP listener defaults to loopback. REST mutations, configuration reads,
  local-device inventory, engine state and Doctor diagnostics require the token;
  session, stats and language reads, HLS and direct audio/WebVTT are
  intentionally unauthenticated and rely on the listener boundary.
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
