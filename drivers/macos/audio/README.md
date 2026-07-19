# Prukka Speaker (macOS)

> A native CoreAudio HAL virtual speaker: apps play the far end of a call into it, the engine captures that audio for interpretation (the call profile's ear).

The same proven HAL loopback core as the Prukka Microphone
([`../common`](../common)) the other way around, with this folder's identity —
and the same contract harness gating every build. Command Line Tools only, no
Xcode.

## Build

```bash
./build.sh    # or: make speaker (repo root)
```

## Install

Build both shared-core devices with `make mic speaker`, then use the staged,
verified [safe install or upgrade](../common/README.md#safe-install-or-upgrade)
procedure. It replaces both bundles as one maintenance operation and restarts
CoreAudio only once.

## Use

Select "Prukka Speaker" as output in the app whose audio Prukka should hear,
and capture it as a session source:

```bash
prukka session add call --profile call \
  --in device://audio/<Prukka Speaker index> --langs en --source it
```

## How It Works

Identical to the microphone core — 48 kHz stereo Float32 and one generational,
non-consuming sample-time ring connecting the two sides — with the loopback
oriented so the engine reads what apps play.
