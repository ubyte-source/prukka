// Prukka virtual audio device — shared PortCls (WDM/WaveCyclic) core.
//
// One adapter exposing a render endpoint looped to a capture endpoint
// through a shared ring, the same shape as the macOS HAL and Linux ALSA
// drivers: the microphone driver names the capture side "Prukka
// Microphone" (call apps hear the dub the engine plays into the render
// side); the speaker driver is the identical loopback the other way
// around. Identity comes from identity.h in the driver folder that
// builds this core.
//
// The format is fixed — 48 kHz, stereo, 16-bit PCM — one known-good
// shape instead of a negotiation matrix.
#pragma once

// The per-driver identity: names and the device string used by the INF.
#include "identity.h"

#include <portcls.h>
#include <stdunk.h>
#include <ksdebug.h>

#define PRUKKA_RATE 48000
#define PRUKKA_CHANNELS 2
#define PRUKKA_BITS 16
#define PRUKKA_BLOCK_ALIGN ((PRUKKA_BITS / 8) * PRUKKA_CHANNELS)
#define PRUKKA_BYTES_PER_SEC (PRUKKA_RATE * PRUKKA_BLOCK_ALIGN)
// ~1.4 s shared ring, a power of two of frames like the other cores.
#define PRUKKA_RING_FRAMES 65536
#define PRUKKA_RING_BYTES (PRUKKA_RING_FRAMES * PRUKKA_BLOCK_ALIGN)
// The DPC tick driving the port's copy engine.
#define PRUKKA_TICK_MS 10

// Wave filter pins (streaming + bridge per direction).
enum {
	kWavePinRender = 0,
	kWavePinRenderBridge = 1,
	kWavePinCapture = 2,
	kWavePinCaptureBridge = 3,
};

// Topology pins mirroring the wave bridges out to the endpoints.
enum {
	kTopoPinRenderIn = 0,
	kTopoPinSpeaker = 1,
	kTopoPinMicrophone = 2,
	kTopoPinCaptureOut = 3,
};

// Ring shared between the two streams of the one adapter instance.
struct PrukkaRing {
	UCHAR data[PRUKKA_RING_BYTES];
	// Absolute byte cursors; readers trail writers.
	ULONGLONG written;
	ULONGLONG read;
	KSPIN_LOCK lock;
};

extern PrukkaRing g_Ring;

NTSTATUS CreateMiniportWaveCyclic(PUNKNOWN* unknown);
NTSTATUS CreateMiniportTopology(PUNKNOWN* unknown);
