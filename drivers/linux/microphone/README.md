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

Push the dub into it and select "Prukka Microphone" in the call app. On
Linux, Prukka addresses audio devices by PulseAudio name, never by ALSA card
id — the card id `prukkamic` is only what PulseAudio derives its name from
(typically `alsa_output.platform-prukkamic.analog-stereo` for the playback
side). List the sinks the way Prukka itself does, or copy the URL from the
dashboard's device inventory:

```bash
ffmpeg -hide_banner -sinks pulse    # or: pactl list short sinks
prukka session push <slug> "device://audio/<pulse sink name>" --lang en
```

## Notes

Fixed format: 48 kHz, stereo, S16_LE. With Secure Boot, sign and enroll the
module via MOK.
