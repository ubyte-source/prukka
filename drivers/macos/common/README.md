# Shared HAL Plug-in Core (macOS)

> One CoreAudio loopback implementation behind both macOS audio devices, plus the contract harness that gates every build.

`plugin.c` is the loopback core; `harness.c` loads the built bundle exactly as
coreaudiod would and proves the whole AudioServerPlugIn contract before signing.
[`../microphone`](../microphone) and [`../audio`](../audio) each supply an
`identity.h` + `Info.plist` over this core, and their one-line `build.sh` calls
`common/build.sh`. Fix the core once, both drivers get it.

## Build compatibility

Both universal driver slices and the local contract harness target macOS 12.0
by default, matching the speech-engine runtime. The build reads each generated
Mach-O's `LC_BUILD_VERSION` and fails before signing if its minimum OS drifted
from that target. A specialized build can override the floor explicitly:

```bash
MACOSX_DEPLOYMENT_TARGET=13.0 make mic speaker
```

## Client lifecycle

CoreAudio registers each device client before starting its I/O. The core keeps
that identity in a fixed 256-entry ledger, making duplicate `StartIO` and
`StopIO` delivery idempotent. This prevents an unmatched callback from either
leaking a phantom runner or creating a false last-stop/first-start clock
re-anchor while another client is still recording.

The ledger is deliberately bounded and statically allocated: lifecycle work is
allocation-free and lookup cost has a hard ceiling. The realtime paths stay off
that state entirely: `DoIOOperation` touches no lock at all, and
`GetZeroTimeStamp` takes only a dedicated IO mutex shared with nothing but the
brief clock-anchor rewrite at the 0→1 `StartIO` transition — never with the
ledger scan or the ring clear. If all 256 slots are occupied, registration of
the new client fails without changing the clock or any existing stream. A
matching `RemoveDeviceClient` releases the slot and stops that client if the
host removes it while it is still marked running.

## Safe install or upgrade

Build and install the microphone and speaker together: they share the same
core, and one CoreAudio restart should expose a matched pair. Never `cp -R`
over a live `.driver` bundle; that can leave CoreAudio seeing a partially
updated or merged bundle.

From anywhere inside the repository, build both bundles and stage them beside
the HAL directory so every later `mv` remains on the same filesystem:

```bash
make mic speaker

REPO="$(git rev-parse --show-toplevel)"
HAL=/Library/Audio/Plug-Ins/HAL
STAMP="$(date +%Y%m%dT%H%M%S)-$$"
STAGE="/Library/Audio/Plug-Ins/.prukka-stage-$STAMP"
BACKUP="/Library/Audio/Plug-Ins/.prukka-backup-$STAMP"

sudo install -d -m 0755 "$STAGE" "$BACKUP"
sudo ditto "$REPO/drivers/macos/microphone/build/PrukkaMic.driver" \
  "$STAGE/PrukkaMic.driver"
sudo ditto "$REPO/drivers/macos/audio/build/PrukkaSpeaker.driver" \
  "$STAGE/PrukkaSpeaker.driver"
sudo xattr -cr "$STAGE"
sudo chown -R root:wheel "$STAGE"
sudo chmod -R u+rwX,go+rX,go-w "$STAGE"

sudo codesign --verify --strict --verbose=2 "$STAGE/PrukkaMic.driver"
sudo codesign --verify --strict --verbose=2 "$STAGE/PrukkaSpeaker.driver"
xcrun lipo -archs "$STAGE/PrukkaMic.driver/Contents/MacOS/PrukkaMic"
xcrun lipo -archs "$STAGE/PrukkaSpeaker.driver/Contents/MacOS/PrukkaSpeaker"
```

Both `lipo` checks must report `x86_64 arm64` (order is immaterial). Close OBS,
call clients, and every other audio client using these devices, then stop the
Prukka daemon. Back up any installed pair and atomically rename each staged
bundle into place:

```bash
test ! -e "$HAL/PrukkaMic.driver" || \
  sudo mv "$HAL/PrukkaMic.driver" "$BACKUP/PrukkaMic.driver"
test ! -e "$HAL/PrukkaSpeaker.driver" || \
  sudo mv "$HAL/PrukkaSpeaker.driver" "$BACKUP/PrukkaSpeaker.driver"
sudo mv "$STAGE/PrukkaMic.driver" "$HAL/PrukkaMic.driver"
sudo mv "$STAGE/PrukkaSpeaker.driver" "$HAL/PrukkaSpeaker.driver"
sudo rmdir "$STAGE"

sudo launchctl kickstart -kp system/com.apple.audio.coreaudiod || \
  sudo killall coreaudiod
```

Restart the daemon and audio applications only after CoreAudio has returned.
Keep `$BACKUP` until live capture and playback are verified. To roll back, stop
the same clients, move the current pair into a new sibling directory, move both
backup bundles into `$HAL`, and run the same single CoreAudio restart command.
