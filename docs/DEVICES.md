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

# Feed the activated Prukka Camera on macOS (video only; route audio separately):
prukka session push show --lang en device://video/prukka

# Use a camera as the session source, paired with a microphone — the video
# gets the live-subtitle rendition, the speech drives captions and dubbing:
prukka session add show --in "device://av/0|1" --langs en --source it
```

`<id>` is the platform's device index or name:

| Platform | Capture (source) | Playback / injection (target) |
|---|---|---|
| macOS | avfoundation index (`ffmpeg -f avfoundation -list_devices true -i ""`) | audiotoolbox device index (audio) · `prukka` (activated camera) |
| Linux | PulseAudio source name or `default` | PulseAudio sink name (audio) · v4l2 path (video) |
| Windows | dshow device name (`ffmpeg -list_devices true -f dshow -i dummy`) | WASAPI: `default` or an endpoint ID (audio) |

## Virtual-device components

Prukka ships its own native components, with no third-party virtual devices.
Integrated installation depends on the platform and signing state; on an
existing install:

```bash
sudo "$(command -v prukka)" devices install  # Windows: elevated shell, no sudo
prukka devices status          # DEVICE / STATE / NEXT STEP
sudo "$(command -v prukka)" devices remove   # uninstall
```

Release binaries embed the drivers; `prukka update` refreshes installed
drivers together with the binary, and `prukka doctor` flags anything
outdated. Where the OS demands a user action, `NEXT STEP` names the single
one still needed:

- **macOS** — the microphone and speaker are HAL plug-ins, installed and
  active immediately. The camera is a system extension, and macOS only
  activates those from Developer-ID-signed, notarized apps: until Prukka
  releases are signed, the virtual camera stays unavailable on macOS
  (developer mode is no workaround — it requires disabling SIP). The
  audio path is unaffected.
  Once activated, the dashboard discovers the camera by probing its sink;
  video is sent to it while translated audio is routed to Prukka Speaker or
  another audio output. Burned captions are not advertised for this path.
- **Linux** — kernel modules are version-coupled, so they compile against
  the running kernel on install; that needs `make`, a C compiler and the
  kernel headers (the output names the distro packages if missing). With
  Secure Boot, enroll the module key via MOK. After a kernel upgrade,
  `prukka doctor` points back to
  `sudo "$(command -v prukka)" devices install` for the
  rebuild.
- **Windows** — pushes to `device://audio/default` (or a full endpoint ID)
  play the dub **natively over WASAPI** — no driver needed for playback.
  The **Prukka Webcam** (Windows 11) is staged automatically; start it
  with `PrukkaWebcamCtl install` from the staged directory (the controller
  is a resident process — the camera lives for as long as it runs). Prukka
  probes that resident controller, offers the webcam as a video output and
  feeds it directly when selected. The
  **Prukka Microphone** and **Prukka Speaker** are native PortCls drivers
  that stay manual until attestation signing lands: test-sign and install
  them from [drivers/windows](../drivers/windows/) for a fully native call
  setup. `prukka devices remove` identifies their exact hardware IDs and
  removes every matching Prukka package from the Windows Driver Store, so the
  normal uninstaller also cleans development/test-signed installs.

Building everything from source (development, unbundled builds) is
documented per platform in [drivers/](../drivers/).

Capture consent: on the first microphone or camera capture macOS shows a
permission prompt for **prukka** — approve it once and it appears under
System Settings → Privacy & Security → Microphone. When running `prukka up`
from a terminal instead of the service, the prompt names the terminal. Source
PCM is kept in bounded memory rather than archived, but active sessions can
write private rolling HLS media and subtitle files under the state directory
and send media to configured outputs. See [Data protection and AI
transparency](GDPR.md) for the storage and deletion model.

## Desktop Meet or Zoom call

Install the Prukka audio devices, then configure the native call application:

- speaker/output: **Prukka Speaker**;
- microphone/input: your normal microphone.

Choose **Call** in the dashboard. When the endpoints are present, the wizard
selects the supported incoming path: Prukka Speaker → the user's real listening
output. The current schema configures exactly one monolingual TTS voice, while
a genuine translated two-way call needs one voice for each direction. The
dashboard therefore does not offer two-way routing in this version.

With the default directed `it` → `en` MT pair and English voice, the equivalent
one-direction CLI pattern is `--source it --langs it,en --dub-langs en`: both
targets receive captions, while only English receives TTS audio.
