# Prukka Camera (macOS)

> A native CoreMedia I/O virtual camera: any video app selects "Prukka Camera" and receives a session's video — dubbed audio track and burned-in captions included — with a branded splash when idle.

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
./build/prukka-camfeed http://127.0.0.1:8080/<session>/master.m3u8
```

## Layout

| Folder | Role |
|---|---|
| `Extension/` | The system extension: one device, a sink stream the feeder writes into and a source stream apps read; splash frames when idle |
| `App/` | The host app System Extensions require; it only activates and deactivates the extension |
| `Feeder/` | `prukka-camfeed`: AVPlayer on the session's HLS master, frames pushed into the sink at 30 fps |

## Notes

If activation reports a policy error on ad-hoc builds, macOS is enforcing
provisioning on the `system-extension.install` entitlement even in developer
mode. The escape hatch for a demo machine is booting with SIP's AMFI relaxed
(`csrutil` from Recovery) or signing with any Development certificate once one
exists. Distribution builds will use Developer ID + notarization.
