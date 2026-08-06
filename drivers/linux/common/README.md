# Shared Linux Module Core

> One ALSA implementation behind both Linux audio devices, and one platform
> module glue behind all three.

`pcm_loop.c` is the loopback core. [`../microphone`](../microphone) and
[`../audio`](../audio) each build it with their own `identity.h` via kbuild's
include-the-core pattern (`main.c` includes the core). Fix it once, both modules
get it.

`platform_module.h` is the driver/device registration glue — the driver record,
the module init/exit pair and the kernel-version dance around the remove
callback. `pcm_loop.c` and [`../webcam`](../webcam) both use it, so all three
modules bind their platform device the same way.
