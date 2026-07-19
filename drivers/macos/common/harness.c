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
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static int gFailures = 0;

#define CHECK(cond, ...) \
	do { \
		if (!(cond)) { \
			gFailures++; \
			fprintf(stderr, "FAIL %s:%d: ", __func__, __LINE__); \
			fprintf(stderr, __VA_ARGS__); \
			fprintf(stderr, "\n"); \
		} \
	} while (0)

enum {
	kDevice = 2,
	kStreamIn = 3,
	kStreamOut = 4,
	// Keep this synchronized with plugin.c. The harness fills the ledger to
	// prove both its deterministic failure mode and slot reuse.
	kMaxIOClients = 256,
};

typedef void *(*FactoryFn)(CFAllocatorRef, CFUUIDRef);

struct WriteTask {
	AudioServerPlugInDriverRef driver;
	AudioServerPlugInIOCycleInfo cycle;
	Float32 *samples;
	atomic_int *ready;
	atomic_bool *start;
	UInt32 frames;
	UInt32 client;
	OSStatus status;
};

struct LifecycleTask {
	AudioServerPlugInDriverRef driver;
	AudioServerPlugInClientInfo client;
	atomic_int *ready;
	atomic_bool *start;
	UInt32 iterations;
	OSStatus status;
};

static void *writeConcurrent(void *raw) {
	struct WriteTask *task = raw;
	atomic_fetch_add_explicit(task->ready, 1, memory_order_release);
	while (!atomic_load_explicit(task->start, memory_order_acquire)) {
	}

	AudioServerPlugInDriverInterface *iface = *task->driver;
	task->status = iface->DoIOOperation(
	    task->driver, kDevice, kStreamOut, task->client,
	    kAudioServerPlugInIOOperationWriteMix, task->frames, &task->cycle,
	    task->samples, NULL);

	return NULL;
}

static void *cycleLifecycleConcurrent(void *raw) {
	struct LifecycleTask *task = raw;
	AudioServerPlugInDriverInterface *iface = *task->driver;
	atomic_fetch_add_explicit(task->ready, 1, memory_order_release);
	while (!atomic_load_explicit(task->start, memory_order_acquire)) {
	}

#define RECORD_STATUS(call) \
	do { \
		OSStatus status = (call); \
		if (status != 0 && task->status == 0) task->status = status; \
	} while (0)

	for (UInt32 iteration = 0; iteration < task->iterations; iteration++) {
		RECORD_STATUS(iface->AddDeviceClient(task->driver, kDevice, &task->client));
		RECORD_STATUS(
		    iface->StartIO(task->driver, kDevice, task->client.mClientID));
		RECORD_STATUS(
		    iface->StartIO(task->driver, kDevice, task->client.mClientID));
		RECORD_STATUS(iface->StopIO(task->driver, kDevice, task->client.mClientID));
		RECORD_STATUS(iface->StopIO(task->driver, kDevice, task->client.mClientID));
		RECORD_STATUS(iface->RemoveDeviceClient(task->driver, kDevice, &task->client));
		RECORD_STATUS(iface->RemoveDeviceClient(task->driver, kDevice, &task->client));
	}

#undef RECORD_STATUS

	return NULL;
}

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

static AudioServerPlugInClientInfo Client(UInt32 id) {
	AudioServerPlugInClientInfo info;
	memset(&info, 0, sizeof(info));
	info.mClientID = id;
	info.mProcessID = getpid();
	info.mIsNativeEndian = true;
	return info;
}

static void addClient(AudioServerPlugInDriverRef driver, UInt32 id, const char *label) {
	AudioServerPlugInClientInfo info = Client(id);
	CHECK((*driver)->AddDeviceClient(driver, kDevice, &info) == 0, "AddDeviceClient (%s)",
	      label);
}

static void removeClient(AudioServerPlugInDriverRef driver, UInt32 id, const char *label) {
	AudioServerPlugInClientInfo info = Client(id);
	CHECK((*driver)->RemoveDeviceClient(driver, kDevice, &info) == 0,
	      "RemoveDeviceClient (%s)", label);
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
	addClient(driver, 1, "primary reader");
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

	enum { kFrames = 512, kChannels = 2, kRingFrames = 65536 };
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

	// A second read of the same span returns the same audio: reads never
	// consume the ring, so every capture client hears the same signal.
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
	                           kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
	                           inBuffer, NULL) == 0,
	      "ReadInput (second)");
	CHECK(memcmp(outBuffer, inBuffer, sizeof(outBuffer)) == 0,
	      "second read differs: reads must not consume the ring");

	// A delayed callback from an older generation cannot replace newer audio.
	cycle.mOutputTime.mSampleTime = 4096 + kRingFrames;
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamOut, 1,
	                           kAudioServerPlugInIOOperationWriteMix, kFrames, &cycle,
	                           outBuffer, NULL) == 0,
	      "WriteMix (next generation)");
	cycle.mOutputTime.mSampleTime = 4096;
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamOut, 1,
	                           kAudioServerPlugInIOOperationWriteMix, kFrames, &cycle,
	                           outBuffer, NULL) == 0,
	      "WriteMix (delayed generation)");
	cycle.mInputTime.mSampleTime = 4096 + kRingFrames;
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
	                           kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
	                           inBuffer, NULL) == 0,
	      "ReadInput (next generation)");
	CHECK(memcmp(outBuffer, inBuffer, sizeof(outBuffer)) == 0,
	      "delayed writer replaced the current ring generation");

	// The same physical slots after multiple ring laps must not replay
	// audio left by a writer that has stopped producing frames.
	cycle.mInputTime.mSampleTime = 4096 + 3 * kRingFrames;
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
	                           kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
	                           inBuffer, NULL) == 0,
	      "ReadInput (writer stopped after two laps)");

	Float32 sum = 0;
	for (int i = 0; i < kFrames * kChannels; i++) sum += fabsf(inBuffer[i]);
	CHECK(sum == 0, "stale audio replayed after two ring laps (residual %f)", sum);

	// A frame range that has never been written is silent.
	cycle.mInputTime.mSampleTime = 4096 + kFrames;
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
	                           kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
	                           inBuffer, NULL) == 0,
	      "ReadInput (ahead of writer)");

	sum = 0;
	for (int i = 0; i < kFrames * kChannels; i++) sum += fabsf(inBuffer[i]);
	CHECK(sum == 0, "span ahead of the writer not cleared (residual %f)", sum);

	// WriteMix accumulates: two writers at the same sample time sum.
	for (UInt32 client = 2; client <= 8; client++) {
		addClient(driver, client, "concurrent writer");
		CHECK(iface->StartIO(driver, kDevice, client) == 0,
		      "StartIO (concurrent writer %u)", client);
	}

	cycle.mOutputTime.mSampleTime = 8192;
	cycle.mInputTime.mSampleTime = 8192;
	iface->DoIOOperation(driver, kDevice, kStreamOut, 1, kAudioServerPlugInIOOperationWriteMix,
	                     kFrames, &cycle, outBuffer, NULL);
	iface->DoIOOperation(driver, kDevice, kStreamOut, 2, kAudioServerPlugInIOOperationWriteMix,
	                     kFrames, &cycle, outBuffer, NULL);
	iface->DoIOOperation(driver, kDevice, kStreamIn, 1, kAudioServerPlugInIOOperationReadInput,
	                     kFrames, &cycle, inBuffer, NULL);

	int mixed = 1;
	for (int i = 0; i < kFrames * kChannels; i++) {
		if (fabsf(inBuffer[i] - 2 * outBuffer[i]) > 1e-6f) {
			mixed = 0;
			break;
		}
	}
	CHECK(mixed, "two writers at one sample time do not sum");

	// Concurrent clients must not lose additions or race the capture side.
	// CoreAudio marks DoIOOperation nonblocking and does not promise that
	// different clients share one caller thread.
	enum { kWriters = 8 };
	pthread_t writers[kWriters];
	struct WriteTask tasks[kWriters];
	atomic_int ready = 0;
	atomic_bool start = false;

	cycle.mOutputTime.mSampleTime = 16384;
	cycle.mInputTime.mSampleTime = 16384;
	for (int i = 0; i < kWriters; i++) {
		tasks[i] = (struct WriteTask){driver, cycle, outBuffer, &ready, &start,
		                                    kFrames, (UInt32)(i + 1), -1};
		CHECK(pthread_create(&writers[i], NULL, writeConcurrent, &tasks[i]) == 0,
		      "pthread_create writer %d", i);
	}

	while (atomic_load_explicit(&ready, memory_order_acquire) != kWriters) {
	}
	atomic_store_explicit(&start, true, memory_order_release);

	for (int i = 0; i < kWriters; i++) {
		CHECK(pthread_join(writers[i], NULL) == 0, "pthread_join writer %d", i);
		CHECK(tasks[i].status == 0, "concurrent writer %d status = %d", i,
		      (int)tasks[i].status);
	}

	iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
	                     kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
	                     inBuffer, NULL);
	for (int i = 0; i < kFrames * kChannels; i++) {
		CHECK(fabsf(inBuffer[i] - kWriters * outBuffer[i]) < 1e-5f,
		      "concurrent mix sample %d = %f, want %f", i, inBuffer[i],
		      kWriters * outBuffer[i]);
	}

	// Wrap-around: a write crossing the ring edge reads back intact.
	cycle.mOutputTime.mSampleTime = kRingFrames - kFrames / 2;
	cycle.mInputTime.mSampleTime = kRingFrames - kFrames / 2;
	iface->DoIOOperation(driver, kDevice, kStreamOut, 1, kAudioServerPlugInIOOperationWriteMix,
	                     kFrames, &cycle, outBuffer, NULL);
	iface->DoIOOperation(driver, kDevice, kStreamIn, 1, kAudioServerPlugInIOOperationReadInput,
	                     kFrames, &cycle, inBuffer, NULL);
	CHECK(memcmp(outBuffer, inBuffer, sizeof(outBuffer)) == 0, "wrap-around loopback differs");

	for (UInt32 client = 2; client <= 8; client++) {
		CHECK(iface->StopIO(driver, kDevice, client) == 0,
		      "StopIO (concurrent writer %u)", client);
		removeClient(driver, client, "concurrent writer");
	}

	// Keep the reader alive while output clients enter and leave. StartIO for
	// those writers must not re-anchor the shared clock: Apple requires the
	// device to remain running as long as any one client is still running.
	// Exercise the realistic full-duplex timing too: output is one callback
	// ahead of input, so each input cycle consumes the preceding output cycle.
	UInt64 readerSeed = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 1, &sampleTime, &hostTime, &readerSeed);
	addClient(driver, 90, "first transient writer");
	CHECK(iface->StartIO(driver, kDevice, 90) == 0, "StartIO (first transient writer)");

	UInt64 writerSeed = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 90, &sampleTime, &hostTime, &writerSeed);
	CHECK(writerSeed == readerSeed,
	      "writer joining a live reader re-anchored clock: seed %llu -> %llu", readerSeed,
	      writerSeed);

	enum { kSkewFrames = kFrames, kSkewCycles = 4 };
	Float32 skewBuffers[kSkewCycles][kFrames * kChannels];
	UInt64 skewInputStart = 5 * kRingFrames - kSkewFrames;
	for (int ioCycle = 0; ioCycle < kSkewCycles; ioCycle++) {
		for (int sample = 0; sample < kFrames * kChannels; sample++) {
			skewBuffers[ioCycle][sample] =
			    (Float32)(ioCycle + 1) * 0.05f + sinf((Float32)sample * 0.01f);
		}

		cycle.mOutputTime.mSampleTime =
		    (Float64)(skewInputStart + kSkewFrames + ioCycle * kFrames);
		cycle.mInputTime.mSampleTime =
		    (Float64)(skewInputStart + ioCycle * kFrames);
		memset(inBuffer, 0x7f, sizeof(inBuffer));

		CHECK(iface->DoIOOperation(driver, kDevice, kStreamOut, 90,
		                           kAudioServerPlugInIOOperationWriteMix, kFrames, &cycle,
		                           skewBuffers[ioCycle], NULL) == 0,
		      "WriteMix (skew cycle %d)", ioCycle);
		CHECK(iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
		                           kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
		                           inBuffer, NULL) == 0,
		      "ReadInput (skew cycle %d)", ioCycle);

		if (ioCycle == 0) {
			Float32 skewSum = 0;
			for (int sample = 0; sample < kFrames * kChannels; sample++) {
				skewSum += fabsf(inBuffer[sample]);
			}
			CHECK(skewSum == 0, "initial skew cycle was not silent (residual %f)",
			      skewSum);
		} else {
			CHECK(memcmp(skewBuffers[ioCycle - 1], inBuffer, sizeof(inBuffer)) == 0,
			      "fixed input/output skew lost cycle %d", ioCycle - 1);
		}
	}

	CHECK(iface->StopIO(driver, kDevice, 90) == 0, "StopIO (first transient writer)");
	removeClient(driver, 90, "first transient writer");
	UInt64 seedAfterWriter = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 1, &sampleTime, &hostTime, &seedAfterWriter);
	CHECK(seedAfterWriter == readerSeed,
	      "writer leaving a live reader re-anchored clock: seed %llu -> %llu", readerSeed,
	      seedAfterWriter);

	// Leave a multi-lap output gap, then attach another writer while the same
	// reader remains active. The gap must be silent and the resumed writer must
	// become audible on the next input cycle without requiring a global restart.
	UInt64 resumeInput = skewInputStart + 4 * kRingFrames;
	cycle.mInputTime.mSampleTime = (Float64)resumeInput;
	memset(inBuffer, 0x7f, sizeof(inBuffer));
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
	                           kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
	                           inBuffer, NULL) == 0,
	      "ReadInput (writer gap)");
	Float32 gapSum = 0;
	for (int sample = 0; sample < kFrames * kChannels; sample++) {
		gapSum += fabsf(inBuffer[sample]);
	}
	CHECK(gapSum == 0, "writer gap replayed stale audio (residual %f)", gapSum);

	addClient(driver, 91, "replacement writer");
	CHECK(iface->StartIO(driver, kDevice, 91) == 0, "StartIO (replacement writer)");
	UInt64 replacementSeed = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 91, &sampleTime, &hostTime, &replacementSeed);
	CHECK(replacementSeed == readerSeed,
	      "replacement writer re-anchored live reader: seed %llu -> %llu", readerSeed,
	      replacementSeed);

	cycle.mOutputTime.mSampleTime = (Float64)(resumeInput + kSkewFrames);
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamOut, 91,
	                           kAudioServerPlugInIOOperationWriteMix, kFrames, &cycle,
	                           outBuffer, NULL) == 0,
	      "WriteMix (replacement writer)");
	cycle.mInputTime.mSampleTime = (Float64)(resumeInput + kSkewFrames);
	memset(inBuffer, 0, sizeof(inBuffer));
	CHECK(iface->DoIOOperation(driver, kDevice, kStreamIn, 1,
	                           kAudioServerPlugInIOOperationReadInput, kFrames, &cycle,
	                           inBuffer, NULL) == 0,
	      "ReadInput (replacement writer)");
	CHECK(memcmp(outBuffer, inBuffer, sizeof(outBuffer)) == 0,
	      "replacement writer stayed silent while reader remained live");
	CHECK(iface->StopIO(driver, kDevice, 91) == 0, "StopIO (replacement writer)");
	removeClient(driver, 91, "replacement writer");

	// Lifecycle callbacks are not part of DoIO, but the Host may deliver them
	// from different control threads. Keep the primary reader running while
	// independent clients repeatedly register, duplicate-start, duplicate-stop
	// and unregister in parallel. The ledger must remain race-free and must not
	// invent a last-stop/first-start transition under that contention.
	enum { kLifecycleThreads = 16, kLifecycleIterations = 256 };
	pthread_t lifecycleThreads[kLifecycleThreads];
	struct LifecycleTask lifecycleTasks[kLifecycleThreads];
	atomic_int lifecycleReady = 0;
	atomic_bool lifecycleStart = false;
	for (UInt32 thread = 0; thread < kLifecycleThreads; thread++) {
		lifecycleTasks[thread] = (struct LifecycleTask){
		    .driver = driver,
		    .client = Client(200 + thread),
		    .ready = &lifecycleReady,
		    .start = &lifecycleStart,
		    .iterations = kLifecycleIterations,
		    .status = 0,
		};
		CHECK(pthread_create(&lifecycleThreads[thread], NULL, cycleLifecycleConcurrent,
		                     &lifecycleTasks[thread]) == 0,
		      "pthread_create lifecycle client %u", lifecycleTasks[thread].client.mClientID);
	}
	while (atomic_load_explicit(&lifecycleReady, memory_order_acquire) !=
	       kLifecycleThreads) {
	}
	atomic_store_explicit(&lifecycleStart, true, memory_order_release);

	for (UInt32 thread = 0; thread < kLifecycleThreads; thread++) {
		CHECK(pthread_join(lifecycleThreads[thread], NULL) == 0,
		      "pthread_join lifecycle client %u", lifecycleTasks[thread].client.mClientID);
		CHECK(lifecycleTasks[thread].status == 0,
		      "concurrent lifecycle client %u status = %d",
		      lifecycleTasks[thread].client.mClientID, (int)lifecycleTasks[thread].status);
	}
	UInt64 seedAfterLifecycleStress = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 1, &sampleTime, &hostTime,
	                        &seedAfterLifecycleStress);
	CHECK(seedAfterLifecycleStress == readerSeed,
	      "concurrent lifecycle re-anchored live reader: seed %llu -> %llu", readerSeed,
	      seedAfterLifecycleStress);

	// The clock advances monotonically across ring laps.
	usleep(50 * 1000);
	Float64 laterSample = -1;
	iface->GetZeroTimeStamp(driver, kDevice, 1, &laterSample, &hostTime, &seed);
	CHECK(laterSample >= sampleTime, "clock went backwards: %f then %f", sampleTime,
	      laterSample);

	CHECK(iface->StopIO(driver, kDevice, 1) == 0, "StopIO");

	// A full stop -> start cycle re-anchors the device clock; the HAL only
	// notices the discontinuity through a CHANGED seed. A constant seed here
	// permanently desynchronizes clients that survive the restart.
	UInt64 seedBefore = seed;
	CHECK(iface->StartIO(driver, kDevice, 1) == 0, "StartIO after restart");
	UInt64 seedAfter = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 1, &sampleTime, &hostTime, &seedAfter);
	CHECK(seedAfter != seedBefore, "re-anchor kept seed %llu: clients cannot detect the clock jump",
	      seedAfter);
	CHECK(iface->StopIO(driver, kDevice, 1) == 0, "StopIO after restart");
	removeClient(driver, 1, "primary reader");

	// StartIO and StopIO describe client state, not anonymous counter deltas.
	// Duplicate delivery must be idempotent: otherwise an extra Start leaks a
	// phantom runner, while an extra Stop can falsely create a 0->1 transition
	// and re-anchor a reader that never left.
	addClient(driver, 100, "duplicate-start reader");
	addClient(driver, 101, "duplicate-start writer");
	addClient(driver, 102, "post-duplicate-start client");
	CHECK(iface->StartIO(driver, kDevice, 100) == 0, "StartIO (duplicate-start reader)");
	UInt64 duplicateStartSeed = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 100, &sampleTime, &hostTime,
	                        &duplicateStartSeed);
	CHECK(iface->StartIO(driver, kDevice, 101) == 0,
	      "StartIO (duplicate-start writer)");
	CHECK(iface->StartIO(driver, kDevice, 101) == 0,
	      "StartIO (duplicate-start writer, duplicate)");
	CHECK(iface->StopIO(driver, kDevice, 101) == 0, "StopIO (duplicate-start writer)");
	CHECK(iface->StopIO(driver, kDevice, 100) == 0, "StopIO (duplicate-start reader)");
	CHECK(iface->StartIO(driver, kDevice, 102) == 0,
	      "StartIO (post-duplicate-start client)");
	UInt64 postDuplicateStartSeed = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 102, &sampleTime, &hostTime,
	                        &postDuplicateStartSeed);
	CHECK(postDuplicateStartSeed != duplicateStartSeed,
	      "duplicate StartIO leaked a runner: full stop kept seed %llu",
	      postDuplicateStartSeed);
	CHECK(iface->StopIO(driver, kDevice, 102) == 0,
	      "StopIO (post-duplicate-start client)");
	// Balances the legacy counter implementation after the assertion above;
	// the ledger implementation intentionally treats it as a no-op.
	CHECK(iface->StopIO(driver, kDevice, 101) == 0,
	      "StopIO (duplicate-start legacy cleanup)");
	removeClient(driver, 100, "duplicate-start reader");
	removeClient(driver, 101, "duplicate-start writer");
	removeClient(driver, 102, "post-duplicate-start client");

	addClient(driver, 110, "duplicate-stop reader");
	addClient(driver, 111, "duplicate-stop writer");
	addClient(driver, 112, "post-duplicate-stop writer");
	CHECK(iface->StartIO(driver, kDevice, 110) == 0, "StartIO (duplicate-stop reader)");
	UInt64 duplicateStopSeed = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 110, &sampleTime, &hostTime,
	                        &duplicateStopSeed);
	CHECK(iface->StartIO(driver, kDevice, 111) == 0, "StartIO (duplicate-stop writer)");
	CHECK(iface->StopIO(driver, kDevice, 111) == 0, "StopIO (duplicate-stop writer)");
	CHECK(iface->StopIO(driver, kDevice, 111) == 0,
	      "StopIO (duplicate-stop writer, duplicate)");
	CHECK(iface->StartIO(driver, kDevice, 112) == 0,
	      "StartIO (post-duplicate-stop writer)");
	UInt64 postDuplicateStopSeed = 0;
	iface->GetZeroTimeStamp(driver, kDevice, 112, &sampleTime, &hostTime,
	                        &postDuplicateStopSeed);
	CHECK(postDuplicateStopSeed == duplicateStopSeed,
	      "duplicate StopIO re-anchored a live reader: seed %llu -> %llu",
	      duplicateStopSeed, postDuplicateStopSeed);
	CHECK(iface->StopIO(driver, kDevice, 112) == 0,
	      "StopIO (post-duplicate-stop writer)");
	CHECK(iface->StopIO(driver, kDevice, 110) == 0, "StopIO (duplicate-stop reader)");
	removeClient(driver, 110, "duplicate-stop reader");
	removeClient(driver, 111, "duplicate-stop writer");
	removeClient(driver, 112, "post-duplicate-stop writer");

	// Fill every fixed ledger slot. Capacity exhaustion must fail cleanly,
	// removing an active client must release its running state and slot, and
	// both the same ID and a different ID must be reusable in that slot.
	for (UInt32 offset = 0; offset < kMaxIOClients; offset++) {
		UInt32 client = 1000 + offset;
		addClient(driver, client, "capacity client");
		CHECK(iface->StartIO(driver, kDevice, client) == 0,
		      "StartIO (capacity client %u)", client);
	}

	AudioServerPlugInClientInfo overflow = Client(2000);
	OSStatus overflowAdd = iface->AddDeviceClient(driver, kDevice, &overflow);
	CHECK(overflowAdd != 0, "full client ledger accepted client %u", overflow.mClientID);
	OSStatus overflowStart = iface->StartIO(driver, kDevice, overflow.mClientID);
	CHECK(overflowStart != 0, "unregistered overflow client started IO");
	if (overflowStart == 0) {
		CHECK(iface->StopIO(driver, kDevice, overflow.mClientID) == 0,
		      "StopIO (legacy overflow cleanup)");
	}

	AudioServerPlugInClientInfo reused = Client(1000);
	CHECK(iface->RemoveDeviceClient(driver, kDevice, &reused) == 0,
	      "RemoveDeviceClient (active reusable ID)");
	CHECK(iface->AddDeviceClient(driver, kDevice, &reused) == 0,
	      "AddDeviceClient (reused ID)");
	CHECK(iface->StartIO(driver, kDevice, reused.mClientID) == 0,
	      "StartIO (reused ID)");
	CHECK(iface->RemoveDeviceClient(driver, kDevice, &reused) == 0,
	      "RemoveDeviceClient (reused active ID)");

	CHECK(iface->AddDeviceClient(driver, kDevice, &overflow) == 0,
	      "freed client slot rejected a different ID");
	CHECK(iface->StartIO(driver, kDevice, overflow.mClientID) == 0,
	      "StartIO (replacement capacity client)");
	CHECK(iface->RemoveDeviceClient(driver, kDevice, &overflow) == 0,
	      "RemoveDeviceClient (replacement active client)");

	for (UInt32 offset = 1; offset < kMaxIOClients; offset++) {
		UInt32 client = 1000 + offset;
		CHECK(iface->StopIO(driver, kDevice, client) == 0,
		      "StopIO (capacity client %u)", client);
		removeClient(driver, client, "capacity client");
	}
	// Legacy-counter cleanup. All are harmless idempotent stops with the
	// ledger, but they balance operations that the old driver accepted above.
	CHECK(iface->StopIO(driver, kDevice, reused.mClientID) == 0,
	      "StopIO (legacy reused-ID cleanup 1)");
	CHECK(iface->StopIO(driver, kDevice, reused.mClientID) == 0,
	      "StopIO (legacy reused-ID cleanup 2)");
	CHECK(iface->StopIO(driver, kDevice, overflow.mClientID) == 0,
	      "StopIO (legacy replacement cleanup)");

	if (gFailures == 0) {
		printf("harness: all driver contract checks PASS\n");
		return 0;
	}

	fprintf(stderr, "harness: %d failure(s)\n", gFailures);
	return 1;
}
