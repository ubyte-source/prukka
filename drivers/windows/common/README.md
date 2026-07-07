# Shared PortCls Audio Core (Windows)

> One WDM/WaveCyclic implementation behind both Windows audio devices.

`adapter.cpp`, `wave.cpp` and `topology.cpp` are the loopback core: a render
endpoint looped to a capture endpoint through a shared ring — the same shape as
the macOS HAL and Linux ALSA cores. [`../microphone`](../microphone) and
[`../audio`](../audio) each build this core with their own `identity.h` + INF
(their `.vcxproj` compiles `../common/*.cpp`). Fix the core once, both drivers
get it.

Fixed format: 48 kHz, stereo, 16-bit PCM. Capture clears what it reads, so a
reader with no writer gets silence, never stale audio.
