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

```bash
sudo cp -R build/PrukkaMic.driver /Library/Audio/Plug-Ins/HAL/
sudo launchctl kickstart -kp system/com.apple.audio.coreaudiod
ffmpeg -f avfoundation -list_devices true -i ""   # → "Prukka Microphone"
```

## Use

Push the dub into it, then select "Prukka Microphone" as input in the call app:

```bash
prukka session push <slug> --lang en device://audio/<audiotoolbox-index>
```

## How It Works

48 kHz stereo Float32; one ring buffer (~1.4 s) indexed by sample time connects
WriteMix (output side) to ReadInput (input side). The input clears what it
consumes so stale audio never loops; both sides share one fabricated
zero-timestamp clock anchored at the first StartIO.
