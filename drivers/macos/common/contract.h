// Contract constants shared by the plugin core and its harness. The harness
// proves the driver against these exact values, so they live in one header:
// object topology or ring geometry drifting between the two sides would
// otherwise pass silently.
#ifndef PRUKKA_DRIVER_CONTRACT_H
#define PRUKKA_DRIVER_CONTRACT_H

#include <CoreAudio/AudioServerPlugIn.h>

// Fixed object IDs: one plug-in, one device, one stream per direction.
enum {
	kObjectID_PlugIn = kAudioObjectPlugInObject,
	kObjectID_Device = 2,
	kObjectID_Stream_Input = 3,
	kObjectID_Stream_Output = 4,
};

// Ring geometry: a power of two of frames (~1.4 s) shared by both sides.
#define kRing_Frames 65536
#define kChannels 2

// CoreAudio assigns a stable ID to each device client before it can start
// IO; the plugin keeps running state in a fixed ledger of this many slots.
// 256 is far above a practical audio-device fanout, and exhaustion fails
// the new client without disturbing existing IO.
#define kMaxIOClients 256

#endif
