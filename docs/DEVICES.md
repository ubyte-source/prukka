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

Use the `device://` URLs returned by the dashboard's authenticated device
inventory instead of copying a displayed numeric index. macOS audio indexes
can move when a device appears or disappears, so discovered audio URLs carry a
`?label=` hint and Prukka rebinds them by display name when possible. The
inventory omits that hint when a label is already duplicated, preserving the
specific selected index, and playback rejects a label that becomes ambiguous
later instead of choosing its first match. Playback rebinding reads the latest
complete immutable CoreAudio inventory and refreshes it on one coalesced
worker; a blocked native property read therefore cannot block route admission
or the device-list API. A cold device list waits at most 250 ms for its first
inventory and otherwise degrades to the capture list until a later refresh.
A capture label must remain unique and unchanged; a label containing `:` cannot
be rebound because AVFoundation treats the character as a stream separator.
Reselect the capture device after a rename/removal or when a duplicate label
appears. Linux PulseAudio names and Windows endpoint IDs are used directly.

Call capture asks FFmpeg for 20 ms native fragments on Windows DirectShow and
Linux PulseAudio, then delivers 20 ms PCM blocks to the pipeline. Broadcast
capture retains the device defaults. FFmpeg's AVFoundation input exposes no
equivalent audio-buffer option, so macOS retains the backend's native capture
buffer and only the downstream delivery block is reduced. On macOS the PCM
clock follows AVFoundation timestamps through asynchronous resampling, so a
missed native callback becomes silence instead of permanently shortening the
stream. Before the first delivered frame only, transient format-negotiation or
I/O failures are retried with bounded backoff. A process that remains alive but
produces no first frame within five seconds is closed and follows the same
bounded retry path; later stalls and failures remain terminal.

Device playback also uses profile-aware buffering. Calls feed 20 ms PCM blocks
and request a 40 ms buffer from Linux PulseAudio or native Windows WASAPI;
broadcast uses 100 ms blocks and retains each backend's playback default
(including the existing 200 ms native WASAPI buffer). These settings reduce
buffering inside Prukka's call capture/output processes, but drivers, audio
servers and the destination application can still add their own latency.

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

Capture consent: on first microphone or camera capture macOS asks for access.
A certificate-signed application is identified as **prukka**; an ad-hoc CLI
build can instead appear as its executable path or as an unknown application.
When running from an integrated or ordinary terminal, macOS may attribute the
request to that IDE or terminal. If a pending decision leaves the first native
capture alive but silent, approve access and recreate the lane; Prukka bounds
and retries that first-frame wait instead of hanging indefinitely. Source PCM
is kept in bounded memory rather than archived, but active sessions can write
private rolling HLS media and subtitle files under the state directory and
send media to configured outputs. See [Data protection and AI
transparency](GDPR.md) for the storage and deletion model.

Local macOS builds are ad-hoc signed by default. Because changing an ad-hoc
binary changes its code requirement, repeated rebuilds can invalidate a prior
TCC grant; approval is one-time only for an unchanged ad-hoc binary.
Developers with a persistent signing certificate should build with
`PRUKKA_CODESIGN_IDENTITY='Apple Development: …' make build`; the embedded
bundle identifier then has a stable signing requirement. Public releases still
need the project-controlled Developer ID/notarization workflow before they can
promise permission continuity.

## Desktop Meet or Zoom call

Install the Prukka audio devices, then configure the native call application:

- speaker/output: **Prukka Speaker**;
- microphone/input: **Prukka Microphone**.

Choose **Call** in the dashboard, then confirm four roles: the Prukka Speaker
capture carrying the other participant, your real listening output, your real
microphone and the Prukka Microphone output sent back to the call application.
The label-based defaults are conveniences, not immutable device identities;
confirm them after hardware or OS device-order changes.

When both directed MT routes and a local voice for each language are installed,
the wizard creates two call-profile lanes (`<name>-in` and `<name>-out`) and
routes them as a pair: the incoming lane translates the other participant to
your listening output, and the outgoing lane translates your microphone to
Prukka Microphone. Creation and routing are rolled back together if either side
fails. If only the incoming route and its output voice are available, the
wizard says why and offers an incoming-only call instead; it does not claim that
the session is bidirectional.

The equivalent two-direction CLI shape is:

```bash
prukka session add call-in --profile call \
  --in '<Prukka Speaker capture URL>' --source it --langs en --dub-langs en \
  --pair call-out
prukka session push call-in --lang en '<real listening-output URL>'

prukka session add call-out --profile call \
  --in '<real microphone URL>' --source en --langs it --dub-langs it \
  --pair call-in
prukka session push call-out --lang it '<Prukka Microphone output URL>'
```

Use the exact URLs reported on the current host. A working pair consumes two
active lanes, matching the built-in `max_lanes: 2` default. Wait for each lane
to report `running` before issuing its `session push` command; the dashboard
does this readiness wait automatically.
