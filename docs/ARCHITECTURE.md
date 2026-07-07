# Architecture Documentation

## Overview

This document describes the architectural design of Prukka, a self-hosted
real-time multilingual dubbing and interpretation engine. The system ingests a
live audio/video source, transcribes and translates it into N languages, and
re-emits each as dubbed voice and live subtitles — all on the operator's
machine. AI inference runs behind swappable ports over two equal backends
(hosted OpenRouter or any OpenAI-compatible local server), with optional
voice adaptation — in-engine register matching or cloud timbre cloning —
layered over either.

The architecture prioritizes low end-to-end latency, keeping every byte of
media local, memory efficiency on the hot path, and honest degradation: any
stage that cannot do its job downgrades visibly rather than failing silently.

## System Architecture

### Pipeline Design

The system implements a modular, stage-driven architecture with clear
separation of concerns:

```
Ingest (device/file/RTMP/SRT) → Framer → VAD → Dispatcher → STT → MT → TTS → Mixer → Egress (HLS/VTT/push)
```

Each stage operates behind a small consumer-side interface, so a source, a
provider or an output format can be added without touching the core.

### Component Layers

1. **Ingest Layer**: Supervised ffmpeg turns any source into reference PCM (plus an optional video tap)
2. **Pipeline Layer**: Framing, voice-activity detection and per-speaker pitch clustering
3. **Provider Layer**: A bounded dispatcher fronts hedged STT, translation and streaming TTS
4. **Egress Layer**: Isochrony mixing, the HLS/WebVTT tree and RTMP/SRT/device pushes
5. **Control Layer**: The gRPC/REST/SSE control plane, the embedded dashboard, configuration and lifecycle

## Component Architecture

### 1. Ingest Layer (`internal/media/ingest`, `internal/media/ffmpeg`)

**FFmpeg Supervisor** (`internal/media/ffmpeg`)
- The single package sanctioned to exec ffmpeg with computed arguments and to install its own dependency
- Turns any source into 16 kHz mono reference PCM over a pipe, taps the source's video in the same process when present, and drives every encoded output
- Installs a pinned static ffmpeg into the state directory on first run (`prukka setup`); the updater's privileged binary write also lives here

**File & Stream Sources** (`internal/media/ingest/{file,stream}`)
- `file://` sources are decoded natively and paced to real time
- Listen-mode network sources (RTMP/SRT) accept a single connection, so the PCM feed and the video tap share one ffmpeg process

**Device Sources** (`internal/media/ffmpeg/devices.go`)
- `device://audio/<id>` captures a microphone or virtual cable (avfoundation / PulseAudio / dshow) as a live session source — the call profile's ear

### 2. Pipeline (`internal/core/pipeline`)

Transforms the reference PCM stream into scheduled, translated, voiced audio.

**Framer** (`framer.go`)
- Zero-allocation 20 ms framer feeding the detector; the hot path is covered by a blocking zero-alloc benchmark gate (`make bench`)

**Energy VAD** (`vad.go`)
- Segments utterances by energy with configurable endpointing; flushes the tail when the source ends

**Pitch & Speaker Clustering** (`pitch.go`, `speakers.go`)
- A pure-Go YIN estimator measures each utterance's pitch; a 1-D log-distance clusterer groups speakers and assigns each a distinct, register-matched preset voice from the bank — on by default, overridable with an explicit voice map

**Lane** (`lane.go`)
- Orchestrates the per-target flow: transcribe once → translate per language in parallel (same-language shortcut) → deliver captions → dub with the speaker's voice; degrades to captions-only when dubbing is unavailable

**Track & Mixer** (`track.go`, `mix.go`)
- The isochrony track places each dubbed take at its PTS + delay D, clamps tempo to a natural range and spills right into silence gaps; the mixer ducks the original bed under the dub (sidechain) and holds every rendition on one shared clock

### 3. Provider Layer (`internal/providers`, `internal/dispatch`, `internal/ring`)

Bounds and hardens every paid call.

**Ring** (`internal/ring`)
- Lock-free MPMC ring buffer (cache-line-isolated islands) carrying job pointers; the one maintainer-authorized `//nolint:govet` in the repo protects its intentional layout

**Dispatcher** (`internal/dispatch`)
- A fixed worker pool over the ring: conserved token channels apply backpressure at the full edge and wake parked workers at the empty edge, so bursts are absorbed instead of spawning unbounded goroutines. The accept edge is mutex-guarded so a submit racing shutdown is either run or rejected, never stranded

**Provider Ports & Decorators** (`internal/core/ports.go`, `internal/providers/{openrouter,local,cartesia}`, `internal/providers/helpers/{retry,breaker,hedge}`)
- Small consumer-side `STT`/`MT`/`TTS` interfaces; every backend adapter (hosted OpenRouter or OpenAI-compatible local) is wrapped as `breaker(hedge(retry(client)))`
- **Voice adaptation**: `providers.clone` layers register matching (in-engine pitch shift onto each speaker's fundamental) or Cartesia timbre cloning (each speaker cloned once from a captured reference) over either backend
- **Hedge**: past the observed p95 (sliding window, one-second floor) an identical backup fires and the first answer wins; the loser is canceled
- **Retry**: jittered retries for transient errors, wrapped *inside* the hedge so a broken provider is not doubled
- **Breaker**: per-model circuit breaker guarding against sustained failure

### 4. Egress Layer (`internal/media/egress`)

Serves the translated output in every shape a client might pull or receive.

**HLS Store** (`egress/hls`)
- Per-session tree: video passthrough rendition + one dubbed-audio rendition per language + one WebVTT rendition per language, bound by a `master.m3u8` generated per request (an audio-only source degrades to audio + subtitles). Rendition files are opened from paths rebuilt out of the store's own components, so request data never names a file

**Live WebVTT** (`egress/hls/segmenter.go`, `egress/vtt`)
- Six-second parts with RFC 8216 timestamp mapping; cues spanning parts are repeated

**Burn-in Overlay** (`egress/hls/overlay.go`)
- A wall-clock current-cue file ffmpeg's drawtext re-reads every frame; generation-guarded timers and atomic unique-temp renames mean a later cue always owns the file

**Audio Registry** (`egress/audio`)
- One mixer per session/language encoded on demand to audio-only MPEG-TS or pushed to an RTMP/SRT/device target; each session carries a lifetime gate so removing it ends live listeners instead of leaving them on silence

**Native Windows Playback** (`internal/media/wasapi`)
- Pushes to `device://audio/<id>` on Windows drive the endpoint over WASAPI in pure Go (raw COM vtable dispatch, no cgo), since ffmpeg ships no playback muxer there

### 5. Control Plane (`internal/control`)

The one way in and the one way to watch.

**Server** (`server.go`)
- gRPC over a UNIX domain socket / Windows named pipe with a per-install token interceptor, its grpc-gateway REST mirror, the SSE event bridge and the embedded dashboard, all under one errgroup. The graceful stop is time-boxed so an open event-stream watcher can never keep the daemon alive

**Service** (`control/service`)
- Installs, removes and inspects the OS service (systemd / launchd / SCM) behind one interface with per-OS files for the divergent layer only

**Data Plane** (`http.go`)
- Serves the HLS tree, audio streams and caption documents from the media ports; paths are matched by suffix with legacy leaves first. A host guard refuses foreign Host headers on loopback binds (DNS-rebinding defense)

**Settings Surface** (`settings.go`)
- The dashboard edits the whole configuration through one validated transaction (validate → atomic file write → live swap) and stores provider keys write-only into the OS keychain

### 6. Application & Support Layers

**Configuration** (`internal/core/config`)
- Loading sequence: defaults → `config.yaml` (strict decode, unknown fields rejected) → `PRUKKA_*` env → validation; a live snapshot supports SIGHUP hot-reload

**Secrets** (`internal/secret`)
- Resolves `keychain://` references from the OS keychain (Keychain / Credential Manager / Secret Service); `prukka key set` writes them, doctor probes round-trip

**Observability** (`internal/observability`)
- slog JSON logging and a Prometheus `/metrics` surface (stage and end-to-end latency histograms, session gauge, cost fan-out, provider errors, fallback state, dispatcher saturation)

**Self-Update** (`internal/update`)
- `prukka update` fetches the release, verifies the platform archive against the published checksums and replaces the binary atomically (move-aside on Windows)

**Dashboard** (`web`, embedded from `internal/webui`)
- A Svelte 5 + Vite single-page app (typed API client + SSE, runes state, rendering sections) built by a pinned, checksum-verified Node toolchain; the built bundle is embedded so users install one Go binary

## Data Flow

### Message Processing Pipeline

1. **Ingest**
   - The ffmpeg supervisor produces reference PCM (and a video tap when present) over pipes; `file://` sources are paced to real time, listen-mode network sources share one process

2. **Framing & Detection**
   - The 20 ms framer feeds the energy VAD, which emits complete utterances; the pitch estimator tags each with a speaker cluster

3. **Dispatch & Transcription** (bounded, hedged)
   - Utterances are submitted to the dispatcher; a worker calls hedged STT once, and translation fans out per target language in parallel

4. **Voicing** (per speaker)
   - Each translated segment is spoken by the cluster's register-matched voice via streaming TTS; captions are delivered immediately regardless

5. **Mixing** (one clock)
   - Dubbed takes are placed on the isochrony track at PTS + D, tempo-clamped, ducked over the original bed; every rendition is timestamp-shifted onto one shared clock

6. **Egress**
   - The HLS master binds video passthrough + audio + subtitle renditions; audio streams encode on demand; pushes target RTMP/SRT or a native virtual device with optional burned-in captions

7. **Control & Monitoring**
   - The control plane serves the tree and events; metrics and structured logs are emitted at each stage; budgets pause the pipeline before spend runs away

## Design Principles

### Local by Default

The network is treated as the adversary. Media never leaves the machine; only
utterance/segment payloads reach the configured provider — and on the local
backend, nothing leaves the host at all. A hosted
dashboard is a static bundle that drives the browser to talk to `127.0.0.1` —
it never sees a byte of media.

### Bounded, Hedged Provider Access

Every paid call passes through one bounded dispatcher, so concurrency is capped
across all sessions and bursts apply backpressure instead of spawning
goroutines. Tail latency is cut by hedging past the observed p95; a circuit
breaker and jittered retries harden against provider failure without doubling a
broken one.

### One Clock, Honest Degradation

A single delay D shifts every rendition onto one timeline, so timestamp-aligned
players sync video, dubbed audio and subtitles. When a capability is missing —
no video tap, no system font, no dubbing — the affected output downgrades
visibly and the reason is logged, never silently dropped.

### The Linter Is the Law

The maintainer's `.golangci.yml` is read-only for contributors: code
adapts to the linter, never the reverse. The zero-suppression policy is enforced
in CI by a suppression gate, a config-integrity anchor (`LINTER.sha256`) and
CODEOWNERS. The only sanctioned exceptions are a performance allowlist
(`internal/ring`, `internal/media/wasapi`) and the exec/install zone
(`internal/media/ffmpeg`).

## Repository Layout

```
cmd/prukka/        The one binary and its wiring point (cobra subcommands)
internal/          All Go packages — the compiler forbids outside imports
  core/            Domain types, ports, language registry, session store, pipeline, config
  control/         gRPC/REST/SSE control plane + OS-service install
  media/           ffmpeg supervisor, HLS/VTT/audio egress, ingest, WASAPI playback
  providers/       Backend adapters (openrouter, local, cartesia) + shared helpers (rest, chat, retry, breaker, hedge)
  ring/ dispatch/  Lock-free MPMC ring and the bounded provider pool
  observability/   slog logging + Prometheus metrics + fallback
  secret/ doctor/  Keychain resolution and environment probes
  update/          Explicit checksum-verified self-update
  webui/ tray/     Embedded dashboard bundle and system-tray companion
  gen/             Generated protobuf/gRPC/gateway code (buf)
web/               Dashboard source (Svelte 5 SPA, en/it, Playwright e2e)
drivers/           Native virtual devices per OS (macOS/Linux/Windows)
proto/             prukka.v1.Control API definitions
deploy/            Installers, hosted-dashboard notes, Grafana dashboard
docs/              This document plus device, privacy, brand and signing guides
```
