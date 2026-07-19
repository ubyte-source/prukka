# Prukka Speaker (Windows)

> A native PortCls virtual audio driver: apps play the far end into the driver's speaker side, the engine captures it from the microphone side (the call profile's ear).

Shared WaveCyclic core in [`../common`](../common), this folder's identity + INF.

## Build

```bat
:: From a WDK-enabled developer prompt.
msbuild prukka_speaker.vcxproj /p:Configuration=Release /p:Platform=x64
```

## Install

The build intentionally produces no certificate. Before installation, create
the catalog with `Inf2Cat`, test-sign that `.cat`, and trust the test
certificate on the development machine. Follow Microsoft's
[test-signing procedure](https://learn.microsoft.com/windows-hardware/drivers/install/how-to-test-sign-a-driver-package);
the complete package is `prukka_speaker.sys`, `prukka_speaker.inf` and
`prukka_speaker.cat`.

Then enable Test Mode and install from an elevated prompt:

```bat
bcdedit /set testsigning on          :: reboot before installing
devcon install prukka_speaker.inf Root\PrukkaSpeaker
```

`devcon` ships with the WDK used to build the driver. `install` creates the
root-enumerated device and must be used only for the first installation;
subsequent package replacements use `devcon update`. Enabling Test Mode alone
does not sign or trust an unsigned package.

Remove the device and every matching package from the Driver Store with the
same elevated release binary used by the normal uninstaller:

```bat
prukka devices remove
```

## Use

Select "Prukka Speaker" as output in the app Prukka should hear, and capture it
as a session source (`--in device://audio/<Prukka Speaker>`).

## Notes

Distributing the driver to end users requires attestation signing (a Windows
build host and a Microsoft hardware developer account).
