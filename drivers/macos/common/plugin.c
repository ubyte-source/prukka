// Prukka virtual audio device — CoreAudio server plug-in core.
//
// One virtual loopback device: whatever is played into its output side
// appears at its input side. The microphone driver points call apps at
// the input (they hear the dub pushed via device://audio/<idx>); the
// speaker driver is the same loopback the other way around (apps play
// into it, the engine captures the far end). A single ring buffer
// indexed by sample time connects the two sides; the shared zero
// timestamp keeps them on one clock. Identity comes from identity.h.
//
// Built with the Command Line Tools alone (clang -bundle); installed to
// /Library/Audio/Plug-Ins/HAL and picked up by a coreaudiod restart.

#include <CoreAudio/AudioServerPlugIn.h>
#include <mach/mach_time.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <string.h>

// Fixed object IDs: one plug-in, one device, one stream per direction.
enum {
	kObjectID_PlugIn = kAudioObjectPlugInObject,
	kObjectID_Device = 2,
	kObjectID_Stream_Input = 3,
	kObjectID_Stream_Output = 4,
};

// The device's identity (names, UIDs, exported factory symbol) comes from
// the driver folder that builds this core: each of microphone/ and audio/
// supplies its own identity.h and Info.plist over the same proven plugin.
#include "identity.h"

static const Float64 kSampleRate = 48000.0;
static const UInt32 kChannels = 2;
static const UInt32 kBitsPerChannel = 32;
static const UInt32 kBytesPerFrame = (kBitsPerChannel / 8) * kChannels;

// Ring geometry: a power of two of frames (~1.4 s) shared by both sides.
#define kRing_Frames 65536
static _Atomic unsigned long long gRing[kRing_Frames * kChannels];

_Static_assert(sizeof(Float32) == sizeof(uint32_t), "Float32 must be 32-bit");
_Static_assert(sizeof(unsigned long long) == sizeof(uint64_t), "ring word must be 64-bit");
_Static_assert(ATOMIC_LLONG_LOCK_FREE == 2, "audio-ring atomics must be lock-free");

static AudioServerPlugInHostRef gHost = NULL;
static pthread_mutex_t gStateMutex = PTHREAD_MUTEX_INITIALIZER;
static _Atomic UInt32 gIOCount = 0;
static Float64 gAnchorSampleTime = 0;
static UInt64 gAnchorHostTime = 0;
static Float64 gHostTicksPerFrame = 0;

// MARK: - COM plumbing

static AudioServerPlugInDriverInterface gInterface;
static AudioServerPlugInDriverInterface *gInterfacePtr = &gInterface;
static AudioServerPlugInDriverRef gDriverRef = &gInterfacePtr;
static _Atomic UInt32 gRefCount = 1;

static uint32_t FloatBits(Float32 value) {
	uint32_t bits;
	memcpy(&bits, &value, sizeof(bits));
	return bits;
}

static Float32 BitsFloat(uint32_t bits) {
	Float32 value;
	memcpy(&value, &bits, sizeof(value));
	return value;
}

static void RingClear(void) {
	for (size_t i = 0; i < kRing_Frames * kChannels; i++) {
		atomic_store_explicit(&gRing[i], 0, memory_order_relaxed);
	}
}

static void RingAdd(size_t position, uint32_t generation, Float32 value) {
	unsigned long long current =
	    atomic_load_explicit(&gRing[position], memory_order_relaxed);
	unsigned long long next;
	do {
		uint32_t currentGeneration = (uint32_t)(current >> 32);
		if (currentGeneration != generation &&
		    (generation - currentGeneration) > UINT32_MAX / 2) {
			return;
		}

		Float32 previous = currentGeneration == generation
		                       ? BitsFloat((uint32_t)current)
		                       : 0;
		next = ((unsigned long long)generation << 32) |
		       (unsigned long long)FloatBits(previous + value);
	} while (!atomic_compare_exchange_weak_explicit(
	    &gRing[position], &current, next, memory_order_relaxed, memory_order_relaxed));
}

static Float32 RingRead(size_t position, uint32_t generation) {
	unsigned long long value =
	    atomic_load_explicit(&gRing[position], memory_order_relaxed);
	return (uint32_t)(value >> 32) == generation ? BitsFloat((uint32_t)value) : 0;
}

static HRESULT Plugin_QueryInterface(void *inDriver, REFIID inUUID, LPVOID *outInterface) {
	if (inDriver != gDriverRef || outInterface == NULL) {
		return kAudioHardwareBadObjectError;
	}

	CFUUIDRef requested = CFUUIDCreateFromUUIDBytes(NULL, inUUID);
	bool ok = CFEqual(requested, IUnknownUUID) ||
	          CFEqual(requested, kAudioServerPlugInDriverInterfaceUUID);
	CFRelease(requested);

	if (!ok) {
		return E_NOINTERFACE;
	}

	atomic_fetch_add_explicit(&gRefCount, 1, memory_order_relaxed);
	*outInterface = gDriverRef;

	return S_OK;
}

static ULONG Plugin_AddRef(void *inDriver) {
	return atomic_fetch_add_explicit(&gRefCount, 1, memory_order_relaxed) + 1;
}

static ULONG Plugin_Release(void *inDriver) {
	UInt32 current = atomic_load_explicit(&gRefCount, memory_order_relaxed);
	while (current > 0 && !atomic_compare_exchange_weak_explicit(
	           &gRefCount, &current, current - 1, memory_order_relaxed, memory_order_relaxed)) {
	}
	return current > 0 ? current - 1 : 0;
}

// MARK: - Lifecycle

static OSStatus Plugin_Initialize(AudioServerPlugInDriverRef inDriver,
                                     AudioServerPlugInHostRef inHost) {
	gHost = inHost;

	struct mach_timebase_info timebase;
	mach_timebase_info(&timebase);

	Float64 ticksPerSecond = 1e9 * (Float64)timebase.denom / (Float64)timebase.numer;
	gHostTicksPerFrame = ticksPerSecond / kSampleRate;

	return kAudioHardwareNoError;
}

static OSStatus Plugin_CreateDevice(AudioServerPlugInDriverRef inDriver,
                                       CFDictionaryRef inDescription,
                                       const AudioServerPlugInClientInfo *inClientInfo,
                                       AudioObjectID *outDeviceObjectID) {
	return kAudioHardwareUnsupportedOperationError;
}

static OSStatus Plugin_DestroyDevice(AudioServerPlugInDriverRef inDriver,
                                        AudioObjectID inDeviceObjectID) {
	return kAudioHardwareUnsupportedOperationError;
}

static OSStatus Plugin_AddDeviceClient(AudioServerPlugInDriverRef inDriver,
                                          AudioObjectID inDeviceObjectID,
                                          const AudioServerPlugInClientInfo *inClientInfo) {
	return kAudioHardwareNoError;
}

static OSStatus Plugin_RemoveDeviceClient(AudioServerPlugInDriverRef inDriver,
                                             AudioObjectID inDeviceObjectID,
                                             const AudioServerPlugInClientInfo *inClientInfo) {
	return kAudioHardwareNoError;
}

static OSStatus Plugin_PerformDeviceConfigurationChange(AudioServerPlugInDriverRef inDriver,
                                                           AudioObjectID inDeviceObjectID,
                                                           UInt64 inChangeAction,
                                                           void *inChangeInfo) {
	return kAudioHardwareNoError;
}

static OSStatus Plugin_AbortDeviceConfigurationChange(AudioServerPlugInDriverRef inDriver,
                                                         AudioObjectID inDeviceObjectID,
                                                         UInt64 inChangeAction,
                                                         void *inChangeInfo) {
	return kAudioHardwareNoError;
}

// MARK: - Property helpers

static bool IsPlugIn(AudioObjectID id) { return id == kObjectID_PlugIn; }
static bool IsDevice(AudioObjectID id) { return id == kObjectID_Device; }
static bool IsStream(AudioObjectID id) {
	return id == kObjectID_Stream_Input || id == kObjectID_Stream_Output;
}

static AudioStreamBasicDescription StreamFormat(void) {
	AudioStreamBasicDescription format = {0};
	format.mSampleRate = kSampleRate;
	format.mFormatID = kAudioFormatLinearPCM;
	format.mFormatFlags = kAudioFormatFlagIsFloat | kAudioFormatFlagsNativeEndian |
	                      kAudioFormatFlagIsPacked;
	format.mBytesPerPacket = kBytesPerFrame;
	format.mFramesPerPacket = 1;
	format.mBytesPerFrame = kBytesPerFrame;
	format.mChannelsPerFrame = kChannels;
	format.mBitsPerChannel = kBitsPerChannel;

	return format;
}

static Boolean Plugin_HasProperty(AudioServerPlugInDriverRef inDriver,
                                     AudioObjectID inObjectID, pid_t inClientPID,
                                     const AudioObjectPropertyAddress *inAddress) {
	if (inAddress == NULL) {
		return false;
	}

	switch (inAddress->mSelector) {
	case kAudioObjectPropertyBaseClass:
	case kAudioObjectPropertyClass:
	case kAudioObjectPropertyOwner:
	case kAudioObjectPropertyOwnedObjects:
		return true;
	}

	if (IsPlugIn(inObjectID)) {
		switch (inAddress->mSelector) {
		case kAudioObjectPropertyManufacturer:
		case kAudioPlugInPropertyDeviceList:
		case kAudioPlugInPropertyTranslateUIDToDevice:
		case kAudioPlugInPropertyResourceBundle:
			return true;
		}
	}

	if (IsDevice(inObjectID)) {
		switch (inAddress->mSelector) {
		case kAudioObjectPropertyName:
		case kAudioObjectPropertyManufacturer:
		case kAudioDevicePropertyDeviceUID:
		case kAudioDevicePropertyModelUID:
		case kAudioDevicePropertyTransportType:
		case kAudioDevicePropertyRelatedDevices:
		case kAudioDevicePropertyClockDomain:
		case kAudioDevicePropertyDeviceIsAlive:
		case kAudioDevicePropertyDeviceIsRunning:
		case kAudioObjectPropertyControlList:
		case kAudioDevicePropertyNominalSampleRate:
		case kAudioDevicePropertyAvailableNominalSampleRates:
		case kAudioDevicePropertyIsHidden:
		case kAudioDevicePropertyZeroTimeStampPeriod:
		case kAudioDevicePropertyStreams:
			return true;
		case kAudioDevicePropertyDeviceCanBeDefaultDevice:
		case kAudioDevicePropertyDeviceCanBeDefaultSystemDevice:
		case kAudioDevicePropertyLatency:
		case kAudioDevicePropertySafetyOffset:
			return inAddress->mScope == kAudioObjectPropertyScopeInput ||
			       inAddress->mScope == kAudioObjectPropertyScopeOutput;
		}
	}

	if (IsStream(inObjectID)) {
		switch (inAddress->mSelector) {
		case kAudioStreamPropertyIsActive:
		case kAudioStreamPropertyDirection:
		case kAudioStreamPropertyTerminalType:
		case kAudioStreamPropertyStartingChannel:
		case kAudioStreamPropertyLatency:
		case kAudioStreamPropertyVirtualFormat:
		case kAudioStreamPropertyPhysicalFormat:
		case kAudioStreamPropertyAvailableVirtualFormats:
		case kAudioStreamPropertyAvailablePhysicalFormats:
			return true;
		}
	}

	return false;
}

static OSStatus Plugin_IsPropertySettable(AudioServerPlugInDriverRef inDriver,
                                             AudioObjectID inObjectID, pid_t inClientPID,
                                             const AudioObjectPropertyAddress *inAddress,
                                             Boolean *outIsSettable) {
	if (outIsSettable == NULL) {
		return kAudioHardwareIllegalOperationError;
	}

	*outIsSettable = false;

	return kAudioHardwareNoError;
}

static OSStatus Plugin_GetPropertyDataSize(AudioServerPlugInDriverRef inDriver,
                                              AudioObjectID inObjectID, pid_t inClientPID,
                                              const AudioObjectPropertyAddress *inAddress,
                                              UInt32 inQualifierDataSize,
                                              const void *inQualifierData, UInt32 *outDataSize) {
	if (inAddress == NULL || outDataSize == NULL) {
		return kAudioHardwareIllegalOperationError;
	}

	switch (inAddress->mSelector) {
	case kAudioObjectPropertyBaseClass:
	case kAudioObjectPropertyClass:
		*outDataSize = sizeof(AudioClassID);
		return kAudioHardwareNoError;
	case kAudioObjectPropertyOwner:
		*outDataSize = sizeof(AudioObjectID);
		return kAudioHardwareNoError;
	case kAudioObjectPropertyOwnedObjects:
		*outDataSize = IsPlugIn(inObjectID)   ? sizeof(AudioObjectID)
		               : IsDevice(inObjectID) ? 2 * sizeof(AudioObjectID)
		                                      : 0;
		return kAudioHardwareNoError;
	case kAudioObjectPropertyName:
	case kAudioObjectPropertyManufacturer:
	case kAudioDevicePropertyDeviceUID:
	case kAudioDevicePropertyModelUID:
	case kAudioPlugInPropertyResourceBundle:
		*outDataSize = sizeof(CFStringRef);
		return kAudioHardwareNoError;
	case kAudioPlugInPropertyDeviceList:
		*outDataSize = sizeof(AudioObjectID);
		return kAudioHardwareNoError;
	case kAudioPlugInPropertyTranslateUIDToDevice:
		*outDataSize = sizeof(AudioObjectID);
		return kAudioHardwareNoError;
	case kAudioDevicePropertyTransportType:
	case kAudioDevicePropertyClockDomain:
	case kAudioDevicePropertyDeviceIsAlive:
	case kAudioDevicePropertyDeviceIsRunning:
	case kAudioDevicePropertyDeviceCanBeDefaultDevice:
	case kAudioDevicePropertyDeviceCanBeDefaultSystemDevice:
	case kAudioDevicePropertyLatency:
	case kAudioDevicePropertySafetyOffset:
	case kAudioDevicePropertyIsHidden:
	case kAudioDevicePropertyZeroTimeStampPeriod:
	case kAudioStreamPropertyIsActive:
	case kAudioStreamPropertyDirection:
	case kAudioStreamPropertyTerminalType:
	case kAudioStreamPropertyStartingChannel:
		*outDataSize = sizeof(UInt32);
		return kAudioHardwareNoError;
	case kAudioDevicePropertyRelatedDevices:
		*outDataSize = sizeof(AudioObjectID);
		return kAudioHardwareNoError;
	case kAudioObjectPropertyControlList:
		*outDataSize = 0;
		return kAudioHardwareNoError;
	case kAudioDevicePropertyStreams:
		if (inAddress->mScope == kAudioObjectPropertyScopeGlobal) {
			*outDataSize = 2 * sizeof(AudioObjectID);
		} else {
			*outDataSize = sizeof(AudioObjectID);
		}
		return kAudioHardwareNoError;
	case kAudioDevicePropertyNominalSampleRate:
		*outDataSize = sizeof(Float64);
		return kAudioHardwareNoError;
	case kAudioDevicePropertyAvailableNominalSampleRates:
		*outDataSize = sizeof(AudioValueRange);
		return kAudioHardwareNoError;
	case kAudioStreamPropertyVirtualFormat:
	case kAudioStreamPropertyPhysicalFormat:
		*outDataSize = sizeof(AudioStreamBasicDescription);
		return kAudioHardwareNoError;
	case kAudioStreamPropertyAvailableVirtualFormats:
	case kAudioStreamPropertyAvailablePhysicalFormats:
		*outDataSize = sizeof(AudioStreamRangedDescription);
		return kAudioHardwareNoError;
	}

	return kAudioHardwareUnknownPropertyError;
}

static OSStatus Plugin_GetPropertyData(AudioServerPlugInDriverRef inDriver,
                                          AudioObjectID inObjectID, pid_t inClientPID,
                                          const AudioObjectPropertyAddress *inAddress,
                                          UInt32 inQualifierDataSize, const void *inQualifierData,
                                          UInt32 inDataSize, UInt32 *outDataSize, void *outData) {
	if (inAddress == NULL || outDataSize == NULL || outData == NULL) {
		return kAudioHardwareIllegalOperationError;
	}

#define RETURN_VALUE(type, value) \
	do { \
		if (inDataSize < sizeof(type)) return kAudioHardwareBadPropertySizeError; \
		*((type *)outData) = (value); \
		*outDataSize = sizeof(type); \
		return kAudioHardwareNoError; \
	} while (0)

	switch (inAddress->mSelector) {
	case kAudioObjectPropertyBaseClass:
		RETURN_VALUE(AudioClassID, IsPlugIn(inObjectID)   ? kAudioObjectClassID
		                           : IsDevice(inObjectID) ? kAudioObjectClassID
		                                                  : kAudioObjectClassID);
	case kAudioObjectPropertyClass:
		RETURN_VALUE(AudioClassID, IsPlugIn(inObjectID)   ? kAudioPlugInClassID
		                           : IsDevice(inObjectID) ? kAudioDeviceClassID
		                                                  : kAudioStreamClassID);
	case kAudioObjectPropertyOwner:
		RETURN_VALUE(AudioObjectID, IsPlugIn(inObjectID)   ? kAudioObjectUnknown
		                            : IsDevice(inObjectID) ? kObjectID_PlugIn
		                                                   : kObjectID_Device);
	case kAudioObjectPropertyName:
		RETURN_VALUE(CFStringRef, CFSTR(kDevice_Name));
	case kAudioObjectPropertyManufacturer:
		RETURN_VALUE(CFStringRef, CFSTR(kManufacturer));
	case kAudioDevicePropertyDeviceUID:
		RETURN_VALUE(CFStringRef, CFSTR(kDevice_UID));
	case kAudioDevicePropertyModelUID:
		RETURN_VALUE(CFStringRef, CFSTR(kDevice_ModelUID));
	case kAudioPlugInPropertyResourceBundle:
		RETURN_VALUE(CFStringRef, CFSTR(""));
	}

	if (IsPlugIn(inObjectID)) {
		switch (inAddress->mSelector) {
		case kAudioObjectPropertyOwnedObjects:
		case kAudioPlugInPropertyDeviceList:
			RETURN_VALUE(AudioObjectID, kObjectID_Device);
		case kAudioPlugInPropertyTranslateUIDToDevice: {
			AudioObjectID device = kAudioObjectUnknown;
			if (inQualifierDataSize == sizeof(CFStringRef) && inQualifierData != NULL &&
			    CFEqual(*(const CFStringRef *)inQualifierData, CFSTR(kDevice_UID))) {
				device = kObjectID_Device;
			}
			RETURN_VALUE(AudioObjectID, device);
		}
		}
	}

	if (IsDevice(inObjectID)) {
		switch (inAddress->mSelector) {
		case kAudioObjectPropertyOwnedObjects: {
			if (inDataSize < 2 * sizeof(AudioObjectID)) {
				return kAudioHardwareBadPropertySizeError;
			}
			AudioObjectID *ids = (AudioObjectID *)outData;
			ids[0] = kObjectID_Stream_Input;
			ids[1] = kObjectID_Stream_Output;
			*outDataSize = 2 * sizeof(AudioObjectID);
			return kAudioHardwareNoError;
		}
		case kAudioDevicePropertyTransportType:
			RETURN_VALUE(UInt32, kAudioDeviceTransportTypeVirtual);
		case kAudioDevicePropertyRelatedDevices:
			RETURN_VALUE(AudioObjectID, kObjectID_Device);
		case kAudioDevicePropertyClockDomain:
			RETURN_VALUE(UInt32, 0);
		case kAudioDevicePropertyDeviceIsAlive:
			RETURN_VALUE(UInt32, 1);
		case kAudioDevicePropertyDeviceIsRunning:
			RETURN_VALUE(UInt32, atomic_load_explicit(&gIOCount, memory_order_relaxed) > 0 ? 1 : 0);
		case kAudioDevicePropertyDeviceCanBeDefaultDevice:
			RETURN_VALUE(UInt32, 1);
		case kAudioDevicePropertyDeviceCanBeDefaultSystemDevice:
			RETURN_VALUE(UInt32, 0);
		case kAudioDevicePropertyLatency:
		case kAudioDevicePropertySafetyOffset:
			RETURN_VALUE(UInt32, 0);
		case kAudioObjectPropertyControlList:
			*outDataSize = 0;
			return kAudioHardwareNoError;
		case kAudioDevicePropertyStreams: {
			AudioObjectID *ids = (AudioObjectID *)outData;
			if (inAddress->mScope == kAudioObjectPropertyScopeInput) {
				if (inDataSize < sizeof(AudioObjectID)) {
					return kAudioHardwareBadPropertySizeError;
				}
				ids[0] = kObjectID_Stream_Input;
				*outDataSize = sizeof(AudioObjectID);
			} else if (inAddress->mScope == kAudioObjectPropertyScopeOutput) {
				if (inDataSize < sizeof(AudioObjectID)) {
					return kAudioHardwareBadPropertySizeError;
				}
				ids[0] = kObjectID_Stream_Output;
				*outDataSize = sizeof(AudioObjectID);
			} else {
				if (inDataSize < 2 * sizeof(AudioObjectID)) {
					return kAudioHardwareBadPropertySizeError;
				}
				ids[0] = kObjectID_Stream_Input;
				ids[1] = kObjectID_Stream_Output;
				*outDataSize = 2 * sizeof(AudioObjectID);
			}
			return kAudioHardwareNoError;
		}
		case kAudioDevicePropertyNominalSampleRate:
			RETURN_VALUE(Float64, kSampleRate);
		case kAudioDevicePropertyAvailableNominalSampleRates: {
			if (inDataSize < sizeof(AudioValueRange)) {
				return kAudioHardwareBadPropertySizeError;
			}
			AudioValueRange *range = (AudioValueRange *)outData;
			range->mMinimum = kSampleRate;
			range->mMaximum = kSampleRate;
			*outDataSize = sizeof(AudioValueRange);
			return kAudioHardwareNoError;
		}
		case kAudioDevicePropertyIsHidden:
			RETURN_VALUE(UInt32, 0);
		case kAudioDevicePropertyZeroTimeStampPeriod:
			RETURN_VALUE(UInt32, kRing_Frames);
		}
	}

	if (IsStream(inObjectID)) {
		bool isInput = inObjectID == kObjectID_Stream_Input;

		switch (inAddress->mSelector) {
		case kAudioObjectPropertyOwnedObjects:
			*outDataSize = 0;
			return kAudioHardwareNoError;
		case kAudioStreamPropertyIsActive:
			RETURN_VALUE(UInt32, 1);
		case kAudioStreamPropertyDirection:
			RETURN_VALUE(UInt32, isInput ? 1 : 0);
		case kAudioStreamPropertyTerminalType:
			RETURN_VALUE(UInt32, isInput ? kAudioStreamTerminalTypeMicrophone
			                             : kAudioStreamTerminalTypeSpeaker);
		case kAudioStreamPropertyStartingChannel:
			RETURN_VALUE(UInt32, 1);
		case kAudioStreamPropertyLatency:
			RETURN_VALUE(UInt32, 0);
		case kAudioStreamPropertyVirtualFormat:
		case kAudioStreamPropertyPhysicalFormat: {
			if (inDataSize < sizeof(AudioStreamBasicDescription)) {
				return kAudioHardwareBadPropertySizeError;
			}
			*((AudioStreamBasicDescription *)outData) = StreamFormat();
			*outDataSize = sizeof(AudioStreamBasicDescription);
			return kAudioHardwareNoError;
		}
		case kAudioStreamPropertyAvailableVirtualFormats:
		case kAudioStreamPropertyAvailablePhysicalFormats: {
			if (inDataSize < sizeof(AudioStreamRangedDescription)) {
				return kAudioHardwareBadPropertySizeError;
			}
			AudioStreamRangedDescription *ranged = (AudioStreamRangedDescription *)outData;
			ranged->mFormat = StreamFormat();
			ranged->mSampleRateRange.mMinimum = kSampleRate;
			ranged->mSampleRateRange.mMaximum = kSampleRate;
			*outDataSize = sizeof(AudioStreamRangedDescription);
			return kAudioHardwareNoError;
		}
		}
	}

#undef RETURN_VALUE

	return kAudioHardwareUnknownPropertyError;
}

static OSStatus Plugin_SetPropertyData(AudioServerPlugInDriverRef inDriver,
                                          AudioObjectID inObjectID, pid_t inClientPID,
                                          const AudioObjectPropertyAddress *inAddress,
                                          UInt32 inQualifierDataSize, const void *inQualifierData,
                                          UInt32 inDataSize, const void *inData) {
	return kAudioHardwareUnsupportedOperationError;
}

// MARK: - IO

static OSStatus Plugin_StartIO(AudioServerPlugInDriverRef inDriver,
                                  AudioObjectID inDeviceObjectID, UInt32 inClientID) {
	pthread_mutex_lock(&gStateMutex);

	if (atomic_load_explicit(&gIOCount, memory_order_relaxed) == 0) {
		gAnchorSampleTime = 0;
		gAnchorHostTime = mach_absolute_time();
		RingClear();
	}

	atomic_fetch_add_explicit(&gIOCount, 1, memory_order_relaxed);
	pthread_mutex_unlock(&gStateMutex);

	return kAudioHardwareNoError;
}

static OSStatus Plugin_StopIO(AudioServerPlugInDriverRef inDriver,
                                 AudioObjectID inDeviceObjectID, UInt32 inClientID) {
	pthread_mutex_lock(&gStateMutex);

	if (atomic_load_explicit(&gIOCount, memory_order_relaxed) > 0) {
		atomic_fetch_sub_explicit(&gIOCount, 1, memory_order_relaxed);
	}

	pthread_mutex_unlock(&gStateMutex);

	return kAudioHardwareNoError;
}

// GetZeroTimeStamp fabricates the device clock: one cycle per ring lap,
// anchored at the first StartIO.
static OSStatus Plugin_GetZeroTimeStamp(AudioServerPlugInDriverRef inDriver,
                                           AudioObjectID inDeviceObjectID, UInt32 inClientID,
                                           Float64 *outSampleTime, UInt64 *outHostTime,
                                           UInt64 *outSeed) {
	pthread_mutex_lock(&gStateMutex);

	Float64 ticksPerRing = gHostTicksPerFrame * (Float64)kRing_Frames;
	UInt64 now = mach_absolute_time();
	UInt64 elapsed = now - gAnchorHostTime;
	UInt64 laps = (UInt64)((Float64)elapsed / ticksPerRing);

	*outSampleTime = gAnchorSampleTime + (Float64)laps * (Float64)kRing_Frames;
	*outHostTime = gAnchorHostTime + (UInt64)((Float64)laps * ticksPerRing);
	*outSeed = 1;

	pthread_mutex_unlock(&gStateMutex);

	return kAudioHardwareNoError;
}

static OSStatus Plugin_WillDoIOOperation(AudioServerPlugInDriverRef inDriver,
                                            AudioObjectID inDeviceObjectID, UInt32 inClientID,
                                            UInt32 inOperationID, Boolean *outWillDo,
                                            Boolean *outWillDoInPlace) {
	if (outWillDo == NULL || outWillDoInPlace == NULL) {
		return kAudioHardwareIllegalOperationError;
	}

	*outWillDo = inOperationID == kAudioServerPlugInIOOperationReadInput ||
	             inOperationID == kAudioServerPlugInIOOperationWriteMix;
	*outWillDoInPlace = true;

	return kAudioHardwareNoError;
}

static OSStatus Plugin_BeginIOOperation(AudioServerPlugInDriverRef inDriver,
                                           AudioObjectID inDeviceObjectID, UInt32 inClientID,
                                           UInt32 inOperationID, UInt32 inIOBufferFrameSize,
                                           const AudioServerPlugInIOCycleInfo *inIOCycleInfo) {
	return kAudioHardwareNoError;
}

// DoIOOperation is the loopback: WriteMix accumulates the dub into the
// ring at its sample time. ReadInput copies matching generations without
// consuming, so every capture client hears the same signal.
static OSStatus Plugin_DoIOOperation(AudioServerPlugInDriverRef inDriver,
                                        AudioObjectID inDeviceObjectID,
                                        AudioObjectID inStreamObjectID, UInt32 inClientID,
                                        UInt32 inOperationID, UInt32 inIOBufferFrameSize,
                                        const AudioServerPlugInIOCycleInfo *inIOCycleInfo,
                                        void *ioMainBuffer, void *ioSecondaryBuffer) {
	if (inIOCycleInfo == NULL || ioMainBuffer == NULL) {
		return kAudioHardwareIllegalOperationError;
	}

	Float32 *samples = (Float32 *)ioMainBuffer;

	if (inOperationID == kAudioServerPlugInIOOperationWriteMix) {
		// A negative sample time (possible in the first cycles) would be
		// undefined in the Float64->UInt64 conversion; treat it as zero.
		Float64 outputTime = inIOCycleInfo->mOutputTime.mSampleTime;
		UInt64 start = outputTime > 0 ? (UInt64)outputTime : 0;

		for (UInt32 frame = 0; frame < inIOBufferFrameSize; frame++) {
			UInt64 sampleFrame = start + frame;
			UInt64 position = (sampleFrame % kRing_Frames) * kChannels;
			uint32_t generation = (uint32_t)(sampleFrame / kRing_Frames);

			for (UInt32 ch = 0; ch < kChannels; ch++) {
				RingAdd(position + ch, generation, samples[frame * kChannels + ch]);
			}
		}

		return kAudioHardwareNoError;
	}

	if (inOperationID == kAudioServerPlugInIOOperationReadInput) {
		Float64 inputTime = inIOCycleInfo->mInputTime.mSampleTime;
		UInt64 start = inputTime > 0 ? (UInt64)inputTime : 0;

		for (UInt32 frame = 0; frame < inIOBufferFrameSize; frame++) {
			UInt64 sampleFrame = start + frame;
			UInt64 position = (sampleFrame % kRing_Frames) * kChannels;
			uint32_t generation = (uint32_t)(sampleFrame / kRing_Frames);

			for (UInt32 ch = 0; ch < kChannels; ch++) {
				samples[frame * kChannels + ch] = RingRead(position + ch, generation);
			}
		}

		return kAudioHardwareNoError;
	}

	return kAudioHardwareNoError;
}

static OSStatus Plugin_EndIOOperation(AudioServerPlugInDriverRef inDriver,
                                         AudioObjectID inDeviceObjectID, UInt32 inClientID,
                                         UInt32 inOperationID, UInt32 inIOBufferFrameSize,
                                         const AudioServerPlugInIOCycleInfo *inIOCycleInfo) {
	return kAudioHardwareNoError;
}

// MARK: - Interface table and factory

static AudioServerPlugInDriverInterface gInterface = {
	NULL,
	Plugin_QueryInterface,
	Plugin_AddRef,
	Plugin_Release,
	Plugin_Initialize,
	Plugin_CreateDevice,
	Plugin_DestroyDevice,
	Plugin_AddDeviceClient,
	Plugin_RemoveDeviceClient,
	Plugin_PerformDeviceConfigurationChange,
	Plugin_AbortDeviceConfigurationChange,
	Plugin_HasProperty,
	Plugin_IsPropertySettable,
	Plugin_GetPropertyDataSize,
	Plugin_GetPropertyData,
	Plugin_SetPropertyData,
	Plugin_StartIO,
	Plugin_StopIO,
	Plugin_GetZeroTimeStamp,
	Plugin_WillDoIOOperation,
	Plugin_BeginIOOperation,
	Plugin_DoIOOperation,
	Plugin_EndIOOperation,
};

void *kFactory(CFAllocatorRef inAllocator, CFUUIDRef inRequestedTypeUUID);

void *kFactory(CFAllocatorRef inAllocator, CFUUIDRef inRequestedTypeUUID) {
	if (CFEqual(inRequestedTypeUUID, kAudioServerPlugInTypeUUID)) {
		return gDriverRef;
	}

	return NULL;
}
