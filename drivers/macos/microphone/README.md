# Prukka Microphone (macOS)

> A native CoreAudio HAL virtual microphone: the engine plays the dub into it, any call app captures "Prukka Microphone".

Built with the Command Line Tools alone — no Xcode, no developer account. The
build runs a contract harness that loads the exact bundle and proves the whole
AudioServerPlugIn surface before signing, so a driver that fails its contract
cannot be produced. Shared HAL core in [`../common`](../common), this folder's
identity.

## Build

```bash
./build.sh    # or: make mic (repo root)
```

## Install

Build both shared-core devices with `make mic speaker`, then use the staged,
verified [safe install or upgrade](../common/README.md#safe-install-or-upgrade)
procedure. It replaces both bundles as one maintenance operation and restarts
CoreAudio only once. Afterwards, confirm discovery with:

```bash
ffmpeg -f avfoundation -list_devices true -i ""   # → "Prukka Microphone"
```

## Use

Push the dub into it, then select "Prukka Microphone" as input in the call app:

```bash
prukka session push <slug> --lang en device://audio/<audiotoolbox-index>
```

## How It Works

48 kHz stereo Float32; one ring buffer (~1.4 s) indexed by sample time connects
WriteMix (output side) to ReadInput (input side). Reads do not consume samples,
so concurrent call and monitor clients hear the same audio; generation tags
make unwritten or obsolete ring spans silent. Both sides share one fabricated
zero-timestamp clock anchored when the first registered client starts I/O.
