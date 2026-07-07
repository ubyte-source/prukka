# Drivers

> Native Prukka virtual devices for macOS, Linux and Windows — a microphone, a speaker and a webcam per OS, all Prukka's own code, built by each platform's native toolchain.

Prukka pushes dubbed audio and subtitled video into **virtual devices** so any
app — a browser, a call client, OBS — picks them up as a microphone, speaker or
webcam. There are no third-party devices or build tools here, and the
[`drivers`](../.github/workflows/drivers.yml) CI workflow compiles the whole
matrix on each platform's native runner on every push.

## Device Matrix

| | `microphone/` | `audio/` (speaker) | `webcam/` |
|---|---|---|---|
| [`macos/`](macos) | HAL loopback, contract-harness gated | same core, its own identity | CoreMedia I/O extension |
| [`linux/`](linux) | ALSA `snd-prukka-mic` | ALSA `snd-prukka-speaker` | V4L2 `prukka_webcam` |
| [`windows/`](windows) | PortCls loopback | same core, its own identity | Media Foundation virtual camera (Win 11) |

## The Same Shape Everywhere

- **Microphone** — a loopback the engine plays the dub into and call apps capture
- **Speaker** — the same loopback the other way around: apps play the far end, the engine captures it (the call profile's ear)
- **Webcam** — the session's video with burned-in captions, and a branded splash when idle — never a black square

Each OS shares one implementation core between its microphone and speaker
(`common/`), with per-device identity files: fix the core once, both devices
get it. The `device://` URL scheme that targets these devices is documented in
[docs/DEVICES.md](../docs/DEVICES.md).
