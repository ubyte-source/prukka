# Prukka Speaker (Linux)

> A native ALSA loopback card the other way around: apps play the far end into "Prukka Speaker", the engine captures it for interpretation (the call profile's ear).

Shared ALSA core in [`../common`](../common), this folder's identity.

## Build

```bash
make    # builds snd-prukka-speaker.ko against your running kernel's headers
```

## Install

```bash
sudo insmod snd-prukka-speaker.ko    # unload with: sudo rmmod snd_prukka_speaker
```

## Use

Select "Prukka Speaker" as output in the app Prukka should hear, and capture it
as a session source:

```bash
prukka session add call --profile call \
  --in device://audio/prukkaspeaker --langs en --source it
```

## Notes

Fixed format: 48 kHz, stereo, S16_LE. With Secure Boot, sign and enroll the
module via MOK.
