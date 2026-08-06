# Changelog

Notable changes are recorded here using
[Keep a Changelog](https://keepachangelog.com/) categories and SemVer tags.
Only shipped, testable behaviour belongs in a release entry; planned features,
unverified benchmarks and compliance claims do not.

## [0.1.0] - 2026-08-07

First release. A local-first, one-command real-time speech-translation daemon
and CLI shipped as a single self-contained `prukka` binary, with an embedded
dashboard, a hosted web UI and a managed speech engine covering 29 languages
any-to-any through an English-hub translation pivot. On macOS, Linux and
Windows, `prukka setup` installs and SHA-256 verifies every runtime dependency
against its published catalog — the pinned FFmpeg build, the managed native
speech tools (whisper.cpp, CTranslate2/Opus-MT, Piper) and the configured
model packs; an explicit `providers.local.bin` bundle overrides the managed
install.

### Added

- Single self-contained binary: the one `prukka` binary is the daemon, CLI and
  speech-engine orchestrator, run as hidden `stt|mt|tts` subcommands it
  self-executes — there is no separate engine binary and no separate Go
  module.
- Managed native-tool distribution: `prukka setup` and the dashboard download
  the native speech tools (whisper.cpp, CTranslate2/Opus-MT, Piper) and the
  model packs the configuration needs from prukka's own GitHub release — the
  release whose tag equals the daemon version — SHA-256 verified against
  `prukka-engine-catalog.json`, an asset of that same release, then staged and
  published atomically under the daemon state directory. An explicit
  `providers.local.bin` wins over the managed install.
- 29 languages through an English translation hub: every bundled Opus-MT model
  pairs English with one language, each language ships one Piper voice, and
  the pivot layer (`internal/providers/pivot`) serves any source-to-target
  route as two legs through English. Session admission, warm-up, live
  translation and the dashboard judge translation capability with one shared
  predicate instead of an N^2 matrix of direct-pair models.
- Guided dashboard wizard: a new session is built from what the user has —
  source (microphone, camera plus microphone, a live call heard through the
  virtual speaker, or a stream/file URL), then languages, then outputs, then a
  review step with an editable auto-suggested name and, for audience feeds, a
  per-session delay. The daemon's call/broadcast profile is derived from the
  chosen source, never asked; a call offers "also translate my voice"
  (two-way) whenever the installed voices and routes cover the pair, and every
  missing language is installable inline without leaving the flow. On a
  machine without the speech engine, a first-run card leads with the one-click
  core install before any session is configured.
- Dashboard design gates: the design tokens' WCAG-AA contrast pairs are
  recomputed from the stylesheet by a unit test, an axe-core scan covers the
  main surfaces in both color schemes, and screenshot regression (recorded on
  macOS) pins the rendered design; the design system itself is documented in
  docs/BRAND.md.
- Language management in the dashboard: a Languages section lists installed
  and available voice and translation packs, installs and removes them with
  live download progress over the events stream, and the daemon extends or
  retires its own MT-pair and voice configuration in the same validated
  transaction path settings use. Settings expose the voices stage (local/off)
  and the session defaults, delay included, so runtime configuration lives in
  the dashboard.
- Control API: `GET /api/v1/engine` reports the managed engine, its packs and
  the operation in progress; `POST /api/v1/engine/runtime`,
  `POST /api/v1/engine/packs` and `DELETE /api/v1/engine/packs/{id}` drive
  installs and removals; the SSE stream carries an `engine` progress event.
  The REST surface validates strictly: a request body with an unknown JSON
  field answers `400 Bad Request` naming the field, and `defaults.delay_seconds`
  carries explicit presence on the wire, so an omitted value keeps the
  configured delay while an explicit `0` selects no playout delay.
- Decode policy: a live-window snapshot decodes in one temperature pass and,
  where the tuned window fits it, inside a bounded audio context, so wait-k
  clauses commit at speech pace on every profile; an endpointed final keeps
  the model's full context and its temperature-fallback ladder, so caption and
  dub quality is decided by the final. One decode goroutine serves queued
  finals first and a single newest-wins live snapshot otherwise, and emitted
  `end_samples` boundaries stay monotone, so partial captions stream
  mid-utterance while finals stay in source order. STT helpers cap at the
  eight threads past which whisper.cpp stops scaling, within each profile's
  CPU-sharing rule.
- Call fast-turn policy: call lanes run 20 ms ingress/feed quanta, 40 ms
  playout and device buffers, sentence-sized STT endpoints and fast
  local-agreement commits; broadcast lanes keep deeper cushions tuned for
  robustness. Every lane warms its MT and TTS models concurrently before
  capture starts, so its first committed clause never waits on a model load.
- Bounded resource model: daemon-wide bounds for stored session definitions
  and active lanes; long-lived STT helpers and per-lane MT/TTS caches count
  against active lanes; dispatcher workers and queue bounds apply to MT/TTS
  calls; designated PCM and mixer hot paths carry zero-allocation benchmark
  gates (which do not imply that the complete application performs zero
  allocations).
- Observability: structured startup phases and durations for provider warm-up,
  STT readiness and the first media frame; Whisper readiness and
  inference-duration telemetry; the Prometheus histogram
  `prukka_post_commit_latency_seconds` measuring the pipeline from
  source-clause commit to caption publication or placement of a complete
  synthesized take (capture, STT, device playback and receiving-app buffering
  are outside its window).
- Publishing pipeline: the `build-engine` workflow, dispatched with a
  published release tag, builds one runtime archive per published platform —
  macOS and Linux on amd64 and arm64, Windows on amd64 — holding the native
  tools with their upstream license texts and `ENGINE-NOTICE.txt`, plus
  architecture-independent model packs, generates the validated
  `prukka-engine-catalog.json`, and attaches them to that same prukka release,
  verifying every published asset against the catalog.

### Security

- Every daemon HTTP response carries CSP, frame-denial, MIME-sniffing,
  referrer and browser-permission headers.
- Archive extraction, pack publication, model carry-over and pack removal all
  resolve engine-bundle paths through `os.Root`, so a hostile archive cannot
  read or write outside the engine root through any symlink chain; archives
  are additionally bounded in entry count, per-file size and total expansion.

[0.1.0]: https://github.com/ubyte-source/prukka/releases/tag/0.1.0
