# Local devices: capture and virtual outputs

Prukka reads from and writes to local audio/video devices with
`device://` URLs (capture sources, playback/injection targets):

```bash
# Interpret your microphone live (call profile, fast endpointing):
prukka session add call --profile call --in device://audio/0 --langs en --source it

# Play the English dub into an output device (e.g. a virtual cable):
prukka session push call --lang en device://audio/1

# Inject the dubbed video (captions burned in) into a virtual camera (Linux):
prukka session push show --lang en --subs burn device://video//dev/video10
```

`<id>` is the platform's device index or name:

| Platform | Capture (source) | Playback / injection (target) |
|---|---|---|
| macOS | avfoundation index (`ffmpeg -f avfoundation -list_devices true -i ""`) | audiotoolbox device index (audio) |
| Linux | PulseAudio source name or `default` | PulseAudio sink name (audio) · v4l2 path (video) |
| Windows | dshow device name | not yet — see below |

## Virtual devices — Prukka's own, on every OS

To make Prukka's output appear as a **microphone, speaker or camera in
other apps** (Zoom, Meet, OBS), install the native Prukka device for your
platform from [drivers/](../drivers/) — all Prukka code, no third-party
devices, none requiring a developer-program account for local use:

- **macOS** — `make mic`, `make speaker`, `make webcam` build the HAL
  audio drivers (each gated by a contract harness) and the camera system
  extension with the Command Line Tools alone. Install per the READMEs in
  `drivers/macos/*`, push the dub to the device's audiotoolbox index and
  select "Prukka Microphone" in the call app.
- **Linux** — `make -C drivers/linux/microphone` (and `audio`, `webcam`)
  builds the ALSA loopbacks and the V4L2 webcam against your kernel
  headers; `insmod` them and push to `device://audio/prukka` /
  `device://video//dev/videoN`. With Secure Boot, enroll the modules via
  MOK.
- **Windows** — pushes to `device://audio/default` (or a full endpoint ID)
  play the dub **natively over WASAPI** — no driver needed for playback.
  The **Prukka Webcam** (`drivers/windows/webcam`, Windows 11) registers
  with `PrukkaWebcamCtl install`; feed it the session video and select it
  in any camera app. The **Prukka Microphone** and **Prukka Speaker**
  (`drivers/windows/{microphone,audio}`) are native PortCls drivers built
  with the WDK; test-sign and install them for a fully native call setup.
  Production (attestation) signing is the last packaging step.

Capture consent: macOS asks for microphone permission on first use; grant
it to the terminal or the Prukka app. Nothing is recorded — audio flows
straight through the translation pipeline (see docs/GDPR.md).
