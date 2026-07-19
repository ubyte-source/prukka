# Prukka microphone capture (macOS)

> `prukka-miccapture` — the daemon's native audio-device bridge, two modes: capture a real input device to reference-format PCM on stdout, or (`--play`) render PCM from stdin to an output device resolved by name.

## Capture (default)

macOS delivers **silent** buffers to a process that opens a capture device
without first calling `AVCaptureDevice.requestAccess` — which is exactly what
ffmpeg does. Foreground the buffers arrive anyway; under launchd (how the
daemon runs) they are zeroed, so the dubbed voice has nothing to translate.
This helper opens the device through an `AVCaptureSession` that requests
access first, so it captures real audio in both contexts.

The daemon spawns it automatically for macOS `device://audio/...` sources when
the binary ships beside the daemon executable (see
`ffmpeg.MicCaptureBinary` / `WithMicCapture`); everything else — network, file
and paired camera sources — still demuxes through ffmpeg. Its stdout is the
same s16le, 16 kHz, mono PCM pipe ffmpeg produced, so nothing downstream
changes. Device matching is exact name first, then substring; with no match
the capture mode falls back to the system default input.

## Playback (`--play`)

`--play` reads s16le mono PCM from stdin and renders it to one **output**
device resolved by its CoreAudio name. The daemon chooses it automatically for
labeled macOS `device://audio/...` push targets (see
`ffmpeg.StartDevicePlayback` / `audio.WithPlaybackHelper`), replacing ffmpeg's
audiotoolbox muxer, which can address a device only by its position in the
system array — a position Continuity devices reshuffle at will. On a
configuration change (device died, sample rate switched, ownership moved) the
helper first re-resolves the name and rebinds in-process on a serial rebind
queue; only when that rebind fails does it exit `2` so the daemon respawns it
against the current device table.

## Exit codes

Both modes share one contract the Go supervisor depends on: `0` clean end of
stream, `1` startup/configuration error, `2` device change needing a respawn,
`3` permanent microphone-authorization denial (capture only; respawning
cannot fix it — only the user granting access can).

## Build

```bash
./build.sh          # or: make miccapture (repo root)
```

Produces a universal (x86_64 + arm64), macOS 12+ binary at `build/prukka-miccapture`,
with `Info.plist` embedded in `__TEXT,__info_plist` (TCC reads the microphone
usage description there) and signed with `PRUKKA_CODESIGN_IDENTITY` so the grant
survives rebuilds. `make build` compiles it to `bin/prukka-miccapture`.

## Ship

Place `prukka-miccapture` next to the `prukka` daemon binary (inside
`Prukka.app/Contents/MacOS/` in a bundled install). The daemon resolves it
relative to its own executable and prefers it over ffmpeg for audio-device
capture and for labeled audio-device playback; if it is absent, both paths
fall back to ffmpeg unchanged.

## Arguments

```
prukka-miccapture [--play] --device <localized name substring> --rate <hz>
```

The daemon passes the selected device's display name and the reference rate
(16000).
