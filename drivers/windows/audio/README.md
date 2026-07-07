# Prukka Speaker (Windows)

> A native PortCls virtual audio driver: apps play the far end into the driver's speaker side, the engine captures it from the microphone side (the call profile's ear).

Shared WaveCyclic core in [`../common`](../common), this folder's identity + INF.

## Build

```bat
:: In CI the WDK is restored via NuGet; locally use a WDK-enabled prompt.
msbuild prukka_speaker.vcxproj /p:Configuration=Release /p:Platform=x64
```

## Install

Install the built `prukka_audio.sys` + `prukka_speaker.inf`, test-signed for
local use:

```bat
bcdedit /set testsigning on          :: then reboot
pnputil /add-driver prukka_speaker.inf /install
```

## Use

Select "Prukka Speaker" as output in the app Prukka should hear, and capture it
as a session source (`--in device://audio/<Prukka Speaker>`).

## Notes

A production-signed build needs attestation signing (a Windows build host + a
hardware dev account); that is the last packaging step on the roadmap.
