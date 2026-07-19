# Prukka Camera (macOS)

> A native CoreMedia I/O virtual camera: any video app selects "Prukka Camera" and receives a session's video, with a branded splash when idle. Audio is routed separately through Prukka's virtual audio devices.

Built with the Command Line Tools alone — no Xcode, no developer account. The
`swiftc` and SDK compile everything; the app and system-extension bundles are
assembled and ad-hoc signed by `build.sh`, which then verifies the bundles
(plist lint, extension-id match, signature) before finishing.

## Build

```bash
./build.sh    # or: make webcam (repo root) → build/Prukka Camera.app
```

## Install & Activate

```bash
systemextensionsctl developer on             # one-time; admin password
open "build/Prukka Camera.app"               # → Activate → approve in System Settings
'./build/Prukka Camera.app/Contents/MacOS/prukka-camfeed' /path/to/video/index.m3u8
```

## Layout

| Folder | Role |
|---|---|
| `Extension/` | The system extension: one device, a sink stream the feeder writes into and a source stream apps read; splash frames when idle |
| `App/` | The host app System Extensions require; it only activates and deactivates the extension |
| `Feeder/` | `prukka-camfeed`: AVPlayer on the session's HLS master, frames pushed into the sink at 30 fps |

## Notes

Camera-extension activation requires an appropriately provisioned signature.
The ad-hoc build validates compilation and bundle integrity but does not claim
activation. Distribution builds use Developer ID signing and notarization.
