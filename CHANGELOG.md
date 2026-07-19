# Changelog

Notable changes are recorded here using
[Keep a Changelog](https://keepachangelog.com/) categories and SemVer tags.
Only shipped, testable behaviour belongs in a release entry; planned features,
unverified benchmarks and compliance claims do not.

## [Unreleased]

## [0.1.0] - 2026-07-19

First tagged release. A local-first, one-command real-time speech-translation
daemon and CLI shipped as a single self-contained `prukka` binary, with an
embedded dashboard, a hosted web UI and a managed, bidirectional
Italian↔English speech engine. On macOS, `prukka setup` installs and SHA-256
verifies every runtime dependency against its published catalog — the pinned
FFmpeg build, the managed native speech tools (whisper.cpp, CTranslate2/Opus-MT,
Piper) and the configured model packs; other platforms run against an
operator-supplied bundle.

### Added

- Single self-contained binary: the one `prukka` binary is the daemon, CLI and
  speech-engine orchestrator, run as hidden `stt|mt|tts` subcommands it
  self-executes — there is no separate `prukka-engine` binary and no separate
  Go module.
- Managed native-tool distribution: `prukka setup` and the dashboard download
  the native speech tools (whisper.cpp, CTranslate2/Opus-MT, Piper) and the
  model packs the configuration needs from prukka's own GitHub release — the
  release whose tag equals the daemon version — SHA-256 verified against
  `prukka-engine-catalog.json`, an asset of that same release, then staged and
  published atomically under the daemon state directory. An explicit
  `providers.local.bin` still wins over the managed install.
- Language management in the dashboard: a Languages section lists installed
  and available voice and translation packs, installs and removes them with
  live download progress over the events stream, and the daemon extends or
  retires its own MT-pair and voice configuration in the same validated
  transaction path settings use.
- Control API: `GET /api/v1/engine` reports the managed engine, its packs and
  the operation in progress; `POST /api/v1/engine/runtime`,
  `POST /api/v1/engine/packs` and `DELETE /api/v1/engine/packs/{id}` drive
  installs and removals; the SSE stream gains an `engine` progress event.
- Publishing pipeline: the `build-engine` workflow, dispatched with a published
  release tag, builds per-architecture macOS runtime archives (native tools
  with their upstream license texts and `ENGINE-NOTICE.txt`) and
  architecture-independent model packs, generates the validated
  `prukka-engine-catalog.json`, and attaches them to that same prukka release,
  verifying every published asset against the catalog.

### Changed

- Reduced the local-provider runtime schema to the effective helper path,
  primary and optional call STT model paths, directed MT pairs, TTS voice paths
  and their supported languages.
  Retired remote-provider tuning is migration input only and is removed on save.
- Kept retired v1 provider and per-track voice fields wire-compatible while
  rejecting non-default values explicitly instead of accepting ignored edits.
- Targets without a configured voice for their language now remain caption-only
  instead of receiving synthesis from the wrong language model.
- Session reads/events expose effective dubbed languages so clients advertise
  audio only when the configured voice supports it and the lane is ready.
- The dashboard marks unsupported dubbing targets as caption-only, filters
  directed MT capability and verifies both directions and voice capabilities
  before the two-way wizard creates its paired audio routes.
- Limited dashboard settings to effective session defaults and added visible,
  retryable setup/configuration failures.
- Improved dashboard keyboard operation, focus handling, status text, error
  persistence, privacy notices and English/Italian labels.
- Replaced blanket privacy/accessibility claims with dated implementation
  notes, operator checklists, known limits and official EU/Italian sources.
- Corrected README and security documentation to match the implemented local
  provider and current secret/threat model.
- Made malformed STT events and terminal STT/MT/TTS helper failures fail the
  affected lane explicitly instead of leaving partial output silently alive.
- Added a persistent macOS code-signing identity override for development
  builds and corrected the embedded microphone consent text to describe the
  all-local runtime accurately; ad-hoc signing remains the default.
- Linked dashboard-created call directions reciprocally, exposed the same
  lifecycle link through `session add --pair`, and made removal of either side
  clean up the complete pair.
- Made local TTS publication transactional: a failed provider can no longer
  leak a truncated take into a live device, while successful speech is placed
  at the latest safe playout fence.
- Hardened the macOS HAL loopback with bounded per-client lifecycle state, so
  duplicate start/stop callbacks cannot re-anchor a device that still has a
  live reader.
- Rejected oversized or pathologically repetitive Whisper results before they
  can fan out into MT/TTS, while preserving the endpoint and the healthy lane.

### Performance

- Added a call-only fast-turn policy: 20 ms ingress/feed quanta, 40 ms playout
  and device buffers, sentence-sized STT endpoints, faster local-agreement
  commits, bounded-context one-pass Whisper decoding, and concurrent MT/TTS
  warm-up.
  Broadcast endpointing, decoding context and media cushions retain their
  previous quality/robustness policy.
- Added Whisper readiness and inference-duration telemetry, kept capture
  draining while final inference runs, and gated superseded partials while
  allowing their native requests to complete safely before final inference.
  Removed avoidable MPEG-TS/SRT mux buffering. Device cancellation now unblocks
  synchronous WASAPI writes instead of wedging lane teardown.
- Added a call-profile STT model override, defaulting fresh configurations to
  the bundled multilingual base model with primary-model fallback when omitted.
  The experimental engine recipe also includes the checksum-verified
  `ggml-tiny-q5_1.bin` as an explicit low-resource option; broadcast lanes
  retain the configured primary model and Doctor validates the override when it
  is configured.
- Bumped the native bundle protocol to version 2 for the post-load STT
  readiness handshake and source-timed transcript events; version 1 bundles
  must be rebuilt.
- Select matching checksum-pinned Piper and piper-phonemize archives for Intel
  and Apple Silicon engine builds.
- Added daemon-wide bounds for stored session definitions and active lanes.
  Long-lived STT helpers and per-lane MT/TTS caches count against active lanes;
  dispatcher workers and queue bounds apply to MT/TTS calls.
- Added allocation assertions for designated PCM and mixer hot paths. These
  gates do not imply that the complete application performs zero allocations.
- Refined PGO and benchmark tooling so profile/gate failures are visible rather
  than silently accepted.
- Repaired AVFoundation device timelines from capture timestamps, added bounded
  startup-only retries for transient format negotiation and a live-but-silent
  first frame, and taught live sinks to re-anchor after genuine discontinuities
  without jumping over queued voice.
- Moved macOS output rebinding onto immutable asynchronously refreshed
  CoreAudio inventories, so a wedged HAL property read cannot block route
  admission or device discovery; cold discovery is context-bounded.
- Added source-free structured startup phases and durations for provider warmup,
  STT readiness and the first media frame.

### Security

- Added CSP, frame-denial, MIME-sniffing, referrer and browser-permission
  headers to every daemon HTTP response.

[Unreleased]: https://github.com/ubyte-source/prukka/compare/0.1.0...HEAD
[0.1.0]: https://github.com/ubyte-source/prukka/releases/tag/0.1.0
