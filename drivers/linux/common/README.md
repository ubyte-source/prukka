# Shared ALSA Loopback Core (Linux)

> One ALSA implementation behind both Linux audio devices.

`pcm_loop.c` is the loopback core. [`../microphone`](../microphone) and
[`../audio`](../audio) each build it with their own `identity.h` via kbuild's
include-the-core pattern (`main.c` includes the core). Fix it once, both modules
get it.
