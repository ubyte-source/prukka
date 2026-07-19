# Prukka Microphone (Linux)

> A native ALSA loopback card: the engine plays the dub into its playback side, call apps capture "Prukka Microphone".

Shared ALSA core in [`../common`](../common), this folder's identity.

## Build

```bash
make    # builds snd_prukka_mic.ko against your running kernel's headers
```

## Install

```bash
sudo insmod snd_prukka_mic.ko    # unload with: sudo rmmod snd_prukka_mic
```

## Use

Push the dub into it and select "Prukka Microphone" in the call app:

```bash
prukka session push <slug> device://audio/prukkamic --lang en
```

## Notes

Fixed format: 48 kHz, stereo, S16_LE. With Secure Boot, sign and enroll the
module via MOK.
