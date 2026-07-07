// Verification harness: loads the built driver bundle exactly as
// coreaudiod would and exercises the whole AudioServerPlugIn contract —
// factory, COM identity, every advertised property, the fabricated clock
// and the WriteMix→ReadInput loopback — so the driver is proven before it
// is ever installed. Run via `./build.sh test`.

#include "identity.h"

#include <CoreAudio/AudioServerPlugIn.h>
#include <CoreFoundation/CoreFoundation.h>
#include <dlfcn.h>
#include <math.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static int gFailures = 0;

#define CHECK(cond, ...)                                                                           \
	do {                                                                                       \
		if (!(cond)) {                                                                     \
			gFailures++;                                                               \
			fprintf(stderr, "FAIL %s:%d: ", __func__, __LINE__);                       \
			fprintf(stderr, __VA_ARGS__);                                              \
			fprintf(stderr, "\n");                                                     \
		}                                                                                  \
	} while (0)

enum { kDevice = 2, kStreamIn = 3, kStreamOut = 4 };

typedef void *(*FactoryFn)(CFAllocatorRef, CFUUIDRef);

static AudioObjectPropertyAddress Addr(AudioObjectPropertySelector selector,
                                       AudioObjectPropertyScope scope) {
	return (AudioObjectPropertyAddress){selector, scope, kAudioObjectPropertyElementMain};
}

// checkProperty asserts Has/Size/Get agree for one address.
static void checkProperty(AudioServerPlugInDriverRef driver, AudioObjectID object,
                          AudioObjectPropertyAddress address, const char *label) {
	AudioServerPlugInDriverInterface *iface = *driver;

	CHECK(iface->HasProperty(driver, object, 0, &address), "%s: HasProperty false", label);

	UInt32 size = 0;
	OSStatus status = iface->GetPropertyDataSize(driver, object, 0, &address, 0, NULL, &size);
	CHECK(status == 0, "%s: GetPropertyDataSize = %d", label, (int)status);

	UInt8 buffer[256] = {0};
	CHECK(size <= sizeof(buffer), "%s: size %u too large for the harness", label, size);

	UInt32 used = 0;
	status = iface->GetPropertyData(driver, object, 0, &address, 0, NULL, size, &used, buffer);
	CHECK(status == 0, "%s: GetPropertyData = %d", label, (int)status);
	CHECK(used == size, "%s: used %u != size %u", label, used, size);

	Boolean settable = true;
	CHECK(iface->IsPropertySettable(driver, object, 0, &address, &settable) == 0 && !settable,
	      "%s: unexpectedly settable", label);
}

int main(int argc, char **argv) {
	if (argc != 2) {
		fprintf(stderr, "usage: harness <path-to-driver-binary>\n");
		return 2;
	}

	void *handle = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
	CHECK(handle != NULL, "dlopen: %s", dlerror());
	if (handle == NULL) return 1;

	FactoryFn factory = (FactoryFn)dlsym(handle, kFactoryName);
	CHECK(factory != NULL, "factory symbol missing");
	if (factory == NULL) return 1;

	// The factory answers the HAL plug-in type and rejects others.
	AudioServerPlugInDriverRef driver =
	    (AudioServerPlugInDriverRef)factory(kCFAllocatorDefault, kAudioServerPlugInTypeUUID);
	CHECK(driver != NULL, "factory returned NULL for the plug-in type");
	if (driver == NULL) return 1;

	CFUUIDRef other = CFUUIDCreateFromString(NULL, CFSTR("00000000-0000-0000-0000-000000000001"));
	CHECK(factory(kCFAllocatorDefault, other) == NULL, "factory accepted a foreign type");
	CFRelease(other);

	AudioServerPlugInDriverInterface *iface = *driver;

	// COM identity: IUnknown and the driver interface resolve to the driver.
	void *resolved = NULL;
	CFUUIDBytes unknown = CFUUIDGetUUIDBytes(IUnknownUUID);
	CHECK(iface->QueryInterface(driver, unknown, &resolved) == S_OK && resolved == driver,
	      "QueryInterface(IUnknown)");
	iface->Release(driver);

	static AudioServerPlugInHostInterface host;
	CHECK(iface->Initialize(driver, &host) == 0, "Initialize");

	// Property sweep over everything the driver advertises.
	AudioObjectPropertyScope global = kAudioObjectPropertyScopeGlobal;
	AudioObjectPropertyScope input = kAudioObjectPropertyScopeInput;

	AudioObjectPropertySelector pluginSelectors[] = {
	    kAudioObjectPropertyBaseClass,      kAudioObjectPropertyClass,
	    kAudioObjectPropertyOwner,          kAudioObjectPropertyOwnedObjects,
	    kAudioObjectPropertyManufacturer,   kAudioPlugInPropertyDeviceList,
	    kAudioPlugInPropertyResourceBundle,
	};
	for (size_t i = 0; i < sizeof(pluginSelectors) / sizeof(*pluginSelectors); i++) {
		checkProperty(driver, kAudioObjectPlugInObject, Addr(pluginSelectors[i], global),
		              "plugin");
	}

	AudioObjectPropertySelector deviceSelectors[] = {
	    kAudioObjectPropertyBaseClass,       kAudioObjectPropertyClass,
	    kAudioObjectPropertyOwner,           kAudioObjectPropertyOwnedObjects,
	    kAudioObjectPropertyName,            kAudioObjectPropertyManufacturer,
	    kAudioDevicePropertyDeviceUID,       kAudioDevicePropertyModelUID,
	    kAudioDevicePropertyTransportType,   kAudioDevicePropertyRelatedDevices,
	    kAudioDevicePropertyClockDomain,     kAudioDevicePropertyDeviceIsAlive,
	    kAudioDevicePropertyDeviceIsRunning, kAudioDevicePropertyNominalSampleRate,
	    kAudioDevicePropertyAvailableNominalSampleRates,
	    kAudioDevicePropertyIsHidden,        kAudioDevicePropertyZeroTimeStampPeriod,
	    kAudioDevicePropertyStreams,
	};
	for (size_t i = 0; i < sizeof(deviceSelectors) / sizeof(*deviceSelectors); i++) {
		checkProperty(driver, kDevice, Addr(deviceSelectors[i], global), "device");
	}

	AudioObjectPropertySelector scopedSelectors[] = {
	    kAudioDevicePropertyDeviceCanBeDefaultDevice,
	    kAudioDevicePropertyDeviceCanBeDefaultSystemDevice,
	    kAudioDevicePropertyLatency,
	    kAudioDevicePropertySafetyOffset,
	    kAudioDevicePropertyStreams,
	};
	for (size_t i = 0; i < sizeof(scopedSelectors) / sizeof(*scopedSelectors); i++) {
		checkProperty(driver, kDevice, Addr(scopedSelectors[i], input), "device/input");
	}

	AudioObjectID streams[2] = {kStreamIn, kStreamOut};
	AudioObjectPropertySelector streamSelectors[] = {
	    kAudioObjectPropertyBaseClass,     kAudioObjectPropertyClass,
	    kAudioObjectPropertyOwner,         kAudioStreamPropertyIsActive,
	    kAudioStreamPropertyDirection,     kAudioStreamPropertyTerminalType,
	    kAudioStreamPropertyStartingChannel, kAudioStreamPropertyLatency,
	    kAudioStreamPropertyVirtualFormat, kAudioStreamPropertyPhysicalFormat,
	    kAudioStreamPropertyAvailableVirtualFormats,
	};
	for (size_t s = 0; s < 2; s++) {
		for (size_t i = 0; i < sizeof(streamSelectors) / sizeof(*streamSelectors); i++) {
			checkProperty(driver, streams[s], Addr(streamSelectors[i], global),
			              "stream");
		}
	}

	// Directions and the translate-UID round trip.
	UInt32 direction = 99, used = 0;
	AudioObjectPropertyAddress dir = Addr(kAudioStreamPropertyDirection, global);
	iface->GetPropertyData(driver, kStreamIn, 0, &dir, 0, NULL, 4, &used, &direction);
	CHECK(direction == 1, "input stream direction = %u, want 1", direction);
	iface->GetPropertyData(driver, kStreamOut, 0, &dir, 0, NULL, 4, &used, &direction);
	CHECK(direction == 0, "output stream direction = %u, want 0", direction);

	CFStringRef uid = CFSTR(kDevice_UID);
	AudioObjectID translated = 0;
	AudioObjectPropertyAddress translate =
	    Addr(kAudioPlugInPropertyTranslateUIDToDevice, global);
	iface->GetPropertyData(driver, kAudioObjectPlugInObject, 0, &translate, sizeof(uid), &uid,
	                       sizeof(translated), &used, &translated);
	CHECK(translated == kDevice, "TranslateUIDToDevice = %u, want %u", translated, kDevice);

	// IO: clock sanity, then a full-buffer loopback with sample-exact data.
	CHECK(iface->StartIO(driver, kDevice, 1) == 0, "StartIO");

	Float64 sampleTime = -1;
	UInt64 hostTime = 0, seed = 0;
	CHECK(iface->GetZeroTimeStamp(driver, kDevice, 1, &sampleTime, &hostTime, &seed) == 0,
	      "GetZeroTimeStamp");
	CHECK(sampleTime >= 0 && seed == 1, "zero timestamp: sample %f seed %llu", sampleTime, seed);

	Boolean willDo = false, inPlace = false;
	iface->WillDoIOOperation(driver, kDevice, 1, kAudioServerPlugInIOOperationReadInput,
	                         &willDo, &inPlace);
	CHECK(willDo && inPlace, "WillDo(ReadInput)");
	iface->WillDoIOOperation(driver, kDevice, 1, kAudioServerPlugInIOOperationWriteMix, &willDo,
	                         &inPlace);
	CHECK(willDo && inPlace, "WillDo(WriteMix)");

	enum { kFrames = 512, kChannels = 2 };
	Float32 outBuffer[kFrames * kChannels], inBuffer[kFrames * kChannels];

	for (int i = 0; i < kFrames * kChannels; i++) {
		outBuffer[i] = sinf((Float32)i * 0.01f);
		inBuffer[i] = -1;
	}

	AudioServerPlugInIOCycleInfo cycle;
	memset(&cycle, 0, sizeof(cycle));
	cycle.mOutputTime.mSampleTime = 4096;
	cycle.mInputTime.mSampleTime = 4096;

	CHECK(iface->DoIOOperation(driver, kDevice, kStreamOut, 1,
	                           kAudioServerPlugInIOOperationWriteMix, kFrames, &cycle,
	                           outBuffer, NULL) == 0,
	      "WriteMix");
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
	                           kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
	                           inBuffer, NULL) == 0,
	      "ReadInput");

	CHECK(memcmp(outBuffer, inBuffer, sizeof(outBuffer)) == 0,
	      "loopback: input differs from what was written");

	// A second read of the same span must be silence: consumed data is
	// cleared so stale audio never loops.
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
	                           kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
	                           inBuffer, NULL) == 0,
	      "ReadInput (second)");

	Float32 sum = 0;
	for (int i = 0; i < kFrames * kChannels; i++) sum += fabsf(inBuffer[i]);
	CHECK(sum == 0, "consumed span not cleared (residual %f)", sum);

	// Wrap-around: a write crossing the ring edge reads back intact.
	cycle.mOutputTime.mSampleTime = 65536 - kFrames / 2;
	cycle.mInputTime.mSampleTime = 65536 - kFrames / 2;
	iface->DoIOOperation(driver, kDevice, kStreamOut, 1, kAudioServerPlugInIOOperationWriteMix,
	                     kFrames, &cycle, outBuffer, NULL);
	iface->DoIOOperation(driver, kDevice, kStreamIn, 1, kAudioServerPlugInIOOperationReadInput,
	                     kFrames, &cycle, inBuffer, NULL);
	CHECK(memcmp(outBuffer, inBuffer, sizeof(outBuffer)) == 0, "wrap-around loopback differs");

	// The clock advances monotonically across ring laps.
	usleep(50 * 1000);
	Float64 laterSample = -1;
	iface->GetZeroTimeStamp(driver, kDevice, 1, &laterSample, &hostTime, &seed);
	CHECK(laterSample >= sampleTime, "clock went backwards: %f then %f", sampleTime,
	      laterSample);

	CHECK(iface->StopIO(driver, kDevice, 1) == 0, "StopIO");

	if (gFailures == 0) {
		printf("harness: all driver contract checks PASS\n");
		return 0;
	}

	fprintf(stderr, "harness: %d failure(s)\n", gFailures);
	return 1;
}
