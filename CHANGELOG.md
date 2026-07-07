# Changelog

All notable changes to Prukka. The format follows
[Keep a Changelog](https://keepachangelog.com); versions follow SemVer.

## [Unreleased]

First public cut: a self-hosted, real-time multilingual dubbing and
interpretation engine in a single Go binary. Point it at a live audio/video
source and it re-emits the stream in N languages, as dubbed voice and live
subtitles, on Windows, macOS and Linux. AI inference runs behind pluggable
provider interfaces over two equal backends — hosted OpenRouter or any
OpenAI-compatible server on your own machine — with optional cloud timbre
cloning layered over either.

### Engine

- **Live captions.** FFmpeg-supervised RTMP/SRT/file/device ingest, a
  zero-alloc 20 ms framer, energy voice-activity endpointing, speech-to-text
  and translation with a rolling context window and glossary, delivered as live
  per-language WebVTT. Same-language captions bypass translation.
- **Broadcast dubbing.** Streaming text-to-speech, isochrony shaping that fits
  each take to its source span, a sidechain-ducked bed under the dubbed voice,
  and a per-language timeline that places segments on the source clock and
  spills rather than overwriting speech.
- **Automatic per-speaker voices.** A pure-Go pitch estimator clusters speakers
  and dubs each with a distinct register-matched preset voice, zero
  configuration; an explicit voice map keeps manual control.
- **Voice adaptation in two grades, over any backend.** `providers.clone:
  pitch` is in-engine register matching: the engine measures each speaker's
  fundamental with its own YIN estimator and re-pitches every dubbed take onto
  it (bounded to ±4 semitones, duration-neutral by construction), so the dub
  sits at each speaker's own register — any backend, no key, no cloud.
  `providers.clone: cartesia` is full timbre cloning: the engine captures a
  reference of the speaker's audio, the cloud provider clones it once, and
  every take is synthesized in that voice. Cloning a real voice needs the
  speaker's consent, which the provider enforces. Both are off by default.
- **Color-coded speakers in subtitles.** The WebVTT rendition colors each
  speaker distinctly — a STYLE block plus per-cue class spans, keyed on the
  same pitch clustering — so viewers distinguish speakers without on-screen
  name labels. A single-speaker stream stays plain.
- **Two equal inference backends.** `providers.backend: openrouter` (default)
  or `local` — same three stages, same preset voice bank, interchangeable. The
  local backend speaks the standard OpenAI wire API (multipart transcriptions,
  chat completions, speech), so Ollama, whisper.cpp's server, LocalAI, LM
  Studio and vLLM all drive it unchanged, with no audio leaving the host — the
  fully offline path. Each stage carries its own base URL, so transcription,
  translation and voice may run on three different servers.
- **Video passthrough and the full HLS tree.** The ingest splitter taps the
  source video in the same ffmpeg process that feeds the pipeline and copies it
  (no re-encode) into a rolling HLS rendition; `master.m3u8` binds it to one
  dubbed-audio and one WebVTT rendition per language, generated per request so
  an audio-only source degrades cleanly. Every rendition is shifted by the
  session delay onto one shared clock, so aligned players sync video, audio and
  subtitles.
- **Pushes everywhere.** Per-language pulls over HLS/MPEG-TS and pushes to
  YouTube/Twitch over RTMP; AV pushes carry the session video with optional
  live burned-in captions (`subs=burn`), end to end through CLI, RPC and the
  dashboard. `device://` targets play the dub or inject the captioned video
  into a virtual device — natively over WASAPI on Windows.
- **The call profile.** `device://audio/<id>` captures a microphone or virtual
  cable as a live source with fast endpointing, so a call session interprets a
  live conversation.

### Native virtual devices

- A microphone, a speaker and a webcam per OS under `drivers/`, all Prukka's
  own code built by each platform's native toolchain — no third-party devices
  or build tools. macOS: two contract-harness-gated CoreAudio HAL loopback
  drivers over one shared core plus a CoreMedia I/O camera. Linux: ALSA
  loopback modules and a V4L2 webcam. Windows: a Media Foundation virtual
  camera (Windows 11) and a PortCls virtual audio pair over one shared core.
  A dedicated CI workflow compiles the whole matrix on each native runner.

### Providers & resilience

- **Bounded dispatcher.** A shared, lock-free MPMC work pool caps concurrent
  provider calls across the whole service and applies backpressure instead of
  spawning unbounded goroutines.
- **Hedged speech-to-text.** Past the observed p95 an identical backup fires
  and the first answer wins; the loser is canceled. Wraps retry, sits inside
  the circuit breaker, and its spend lands under the budget guard.
- **Resilience.** Jittered retry, a per-model circuit breaker degrading to
  pass-through, and a per-session budget guard that pauses paid stages in
  order and can hard-stop.

### Control plane & operations

- gRPC over a local UNIX socket / named pipe with a per-install token, a REST
  gateway and Server-Sent Events; an optional pinned-TLS 1.3 remote listener
  with mandatory mTLS. The dashboard is a Svelte single-page app (registry-fed
  wizard, live session table, language editing, push dialog, doctor, event
  log), embedded in the binary; a hosted origin talks only to `127.0.0.1`, so
  media never crosses a third party.
- **The dashboard configures everything.** A Settings section edits the whole
  daemon configuration — backend, voice adaptation, models, defaults, budgets,
  privacy — through one validated transaction: the edit is checked, written
  atomically to the config file and swapped live, or rejected whole with the
  offending field named. Provider API keys are pasted in the browser and land
  directly in the OS keychain: write-only, never in a file, never in a reply —
  the UI shows only a key-configured badge.
- **Hardened local surface.** A host guard refuses foreign Host headers on
  loopback binds, closing the DNS-rebinding hole that sidesteps CORS; writes
  stay token-gated and reads stay loopback-only.
- One-command install per OS with a self-installing, checksum-verified ffmpeg;
  `prukka key set` stores provider keys in the OS keychain with a hidden
  prompt; `service install` registers a systemd / launchd / Windows service;
  `setup` and `doctor` cover environment checks and fix hints; `prukka update`
  self-updates explicitly against published checksums — never automatic.
- Cost per session and language is first-class across the CLI, tray and
  dashboard, converted to euros from real provider usage.

### Observability & performance

- Prometheus `/metrics` (per-stage and end-to-end latency histograms, active
  sessions, provider errors, cost fan-out, fallback state, dispatcher
  saturation) with a bundled Grafana dashboard.
- The release binary is stripped, `-trimpath`ed and profile-guided-optimized.
- Dub timelines keep a bounded live retention window (30 s + hysteresis), so
  an endless session holds minutes of audio in memory, never hours.
- Measured live against the real providers: source→caption **2.16 s**,
  speech-end→dubbed-audio-ready **3.72 s**.

### Engineering

- One binary, cobra subcommands, dependency injection wired only in `main`.
- An immutable maintainer `.golangci.yml` running an extended linter set
  (field-alignment, complexity, length, security, exhaustive switches,
  context hygiene and more); a zero-suppression policy enforced in CI; race
  tests, cross-browser dashboard e2e, live provider-conformance suites and
  end-to-end demos on every change.
