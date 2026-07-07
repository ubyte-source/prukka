# Shared HAL Plug-in Core (macOS)

> One CoreAudio loopback implementation behind both macOS audio devices, plus the contract harness that gates every build.

`plugin.c` is the loopback core; `harness.c` loads the built bundle exactly as
coreaudiod would and proves the whole AudioServerPlugIn contract before signing.
[`../microphone`](../microphone) and [`../audio`](../audio) each supply an
`identity.h` + `Info.plist` over this core, and their one-line `build.sh` calls
`common/build.sh`. Fix the core once, both drivers get it.
