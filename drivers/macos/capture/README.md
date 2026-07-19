# Prukka microphone capture (macOS)

> `prukka-miccapture` — a native AVFoundation helper that captures a real input device and streams reference-format PCM to stdout, replacing ffmpeg's AVFoundation input for the daemon's microphone source.

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
changes.

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
capture; if it is absent, capture falls back to ffmpeg unchanged.

## Arguments

```
prukka-miccapture --device <localized name substring> --rate <hz>
```

The daemon passes the selected device's display name and the reference rate
(16000). With no match the helper falls back to the system default input.
