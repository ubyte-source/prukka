# Prukka Microphone (Windows)

> A native PortCls virtual audio driver: the engine plays the dub into the driver's speaker side, call apps capture "Prukka Microphone".

Shared WaveCyclic core in [`../common`](../common), this folder's identity + INF.

## Build

```bat
:: From a WDK-enabled developer prompt.
msbuild prukka_mic.vcxproj /p:Configuration=Release /p:Platform=x64
```

## Install

The build intentionally produces no certificate. Before installation, create
the catalog with `Inf2Cat`, test-sign that `.cat`, and trust the test
certificate on the development machine. Follow Microsoft's
[test-signing procedure](https://learn.microsoft.com/windows-hardware/drivers/install/how-to-test-sign-a-driver-package);
the complete package is `prukka_mic.sys`, `prukka_mic.inf` and
`prukka_mic.cat`.

Then enable Test Mode and install from an elevated prompt:

```bat
bcdedit /set testsigning on          :: reboot before installing
devcon install prukka_mic.inf Root\PrukkaMic
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

Push the dub to its speaker endpoint and select "Prukka Microphone" in the call
app.

## Notes

Distributing the driver to end users requires attestation signing (a Windows
build host and a Microsoft hardware developer account).
