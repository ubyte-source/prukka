# Changelog

Notable changes are recorded here using
[Keep a Changelog](https://keepachangelog.com/) categories and SemVer tags.
Only shipped, testable behaviour belongs in a release entry; planned features,
unverified benchmarks and compliance claims do not.

## [Unreleased]

### Changed

- Reduced the local-provider runtime schema to the effective helper path, STT
  model path, directed MT pairs, TTS voice path and supported voice language.
  Retired remote-provider tuning is migration input only and is removed on save.
- Kept retired v1 provider and per-track voice fields wire-compatible while
  rejecting non-default values explicitly instead of accepting ignored edits.
- Targets incompatible with the configured monolingual voice now remain
  caption-only instead of receiving synthesis from the wrong language model.
- Session reads/events expose effective dubbed languages so clients advertise
  audio only when the configured voice supports it and the lane is ready.
- The dashboard marks unsupported dubbing targets as caption-only, filters
  directed MT capability, removes the unsupported two-way workflow and verifies
  the server's effective voice capability before creating an audio route.
- Made the missing speech bundle explicit: standard setup installs FFmpeg only;
  the helper, native tools and model files remain operator supplied.
- Limited dashboard settings to effective session defaults and added visible,
  retryable setup/configuration failures.
- Improved dashboard keyboard operation, focus handling, status text, error
  persistence, privacy notices and English/Italian labels.
- Replaced blanket privacy/accessibility claims with dated implementation
  notes, operator checklists, known limits and official EU/Italian sources.
- Corrected README and security documentation to match the implemented local
  provider and current secret/threat model.
- Reclassified the native-engine workflow as ephemeral macOS build validation
  and documented its unverified inputs, redistribution block and missing
  release install path.
- Made malformed STT events and terminal STT/MT/TTS helper failures fail the
  affected lane explicitly instead of leaving partial output silently alive.

### Performance

- Added daemon-wide bounds for stored session definitions and active lanes.
  Long-lived STT helpers and per-lane MT/TTS caches count against active lanes;
  dispatcher workers and queue bounds apply to MT/TTS calls.
- Added allocation assertions for designated PCM and mixer hot paths. These
  gates do not imply that the complete application performs zero allocations.
- Refined PGO and benchmark tooling so profile/gate failures are visible rather
  than silently accepted.

### Security

- Added CSP, frame-denial, MIME-sniffing, referrer and browser-permission
  headers to every daemon HTTP response.

## [0.1.0] - 2026-07-13

Initial development snapshot containing the Go daemon/CLI, local control API,
embedded dashboard, media pipeline, native-provider adapters, FFmpeg runtime
installer, tests and platform-driver source. The complete native speech bundle
and models were not distributed with this tag, so it was not an out-of-box
speech release.

[Unreleased]: https://github.com/ubyte-source/prukka/compare/0.1.0...HEAD
[0.1.0]: https://github.com/ubyte-source/prukka/releases/tag/0.1.0
