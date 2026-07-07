# Prukka Microphone (Windows)

> A native PortCls virtual audio driver: the engine plays the dub into the driver's speaker side, call apps capture "Prukka Microphone".

Shared WaveCyclic core in [`../common`](../common), this folder's identity + INF.

## Build

```bat
:: In CI the WDK is restored via NuGet; locally use a WDK-enabled prompt.
msbuild prukka_mic.vcxproj /p:Configuration=Release /p:Platform=x64
```

## Install

Install the built `prukka_audio.sys` + `prukka_mic.inf`, test-signed for local
use:

```bat
bcdedit /set testsigning on          :: then reboot
pnputil /add-driver prukka_mic.inf /install
```

## Use

Push the dub to its speaker endpoint and select "Prukka Microphone" in the call
app.

## Notes

A production-signed build needs attestation signing (a Windows build host + a
hardware dev account); that is the last packaging step on the roadmap.
