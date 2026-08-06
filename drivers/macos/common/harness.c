// Verification harness: loads the built driver bundle exactly as
// coreaudiod would and exercises the whole AudioServerPlugIn contract —
// factory, COM identity, every advertised property, the fabricated clock
// and the WriteMix→ReadInput loopback — so the driver is proven before it
// is ever installed. Run via `./build.sh test`.
//
// Each contract scenario is one function over the shared Session; the table
// at the bottom runs them in order, because later scenarios depend on the
// ring, client ledger and clock state the earlier ones leave behind.

#include "contract.h"
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

// Short local names over the shared contract constants; the harness fills
// the ledger to prove both its deterministic failure mode and slot reuse.
enum {
	kDevice = kObjectID_Device,
	kStreamIn = kObjectID_Stream_Input,
	kStreamOut = kObjectID_Stream_Output,
};

// kChannels and kRing_Frames come from contract.h; only the cycle length and
// the fixed input/output skew are the harness's own choice.
enum { kFrames = 512, kSkewFrames = kFrames };

typedef void *(*FactoryFn)(CFAllocatorRef, CFUUIDRef);

// Session is the state that outlives a single scenario: the loaded driver,
// the buffers written and read back, and the clock readings later scenarios
// compare against.
struct Session {
	FactoryFn factory;
	AudioServerPlugInDriverRef driver;
	AudioServerPlugInDriverInterface *iface;
	AudioServerPlugInIOCycleInfo cycle;
	Float32 out[kFrames * kChannels];
	Float32 in[kFrames * kChannels];
	Float64 sampleTime;
	UInt64 hostTime;
	UInt64 seed;
	UInt64 readerSeed;
	UInt64 skewInputStart;
};

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
	if (size > sizeof(buffer)) {
		return; // reading on would smash the stack instead of failing cleanly
	}

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

// The factory answers the HAL plug-in type and rejects others.
static void factoryRejectsForeignType(struct Session *s) {
	CFUUIDRef other = CFUUIDCreateFromString(NULL, CFSTR("00000000-0000-0000-0000-000000000001"));
	CHECK(s->factory(kCFAllocatorDefault, other) == NULL, "factory accepted a foreign type");
	CFRelease(other);
}

// COM identity: IUnknown and the driver interface resolve to the driver.
static void comIdentity(struct Session *s) {
	void *resolved = NULL;
	CFUUIDBytes unknown = CFUUIDGetUUIDBytes(IUnknownUUID);
	CHECK(s->iface->QueryInterface(s->driver, unknown, &resolved) == S_OK &&
	          resolved == s->driver,
	      "QueryInterface(IUnknown)");
	s->iface->Release(s->driver);
}

static void initialize(struct Session *s) {
	// The host interface must outlive Initialize: the driver keeps the pointer.
	static AudioServerPlugInHostInterface host;
	CHECK(s->iface->Initialize(s->driver, &host) == 0, "Initialize");
}

static void pluginProperties(struct Session *s) {
	AudioObjectPropertySelector selectors[] = {
	    kAudioObjectPropertyBaseClass,      kAudioObjectPropertyClass,
	    kAudioObjectPropertyOwner,          kAudioObjectPropertyOwnedObjects,
	    kAudioObjectPropertyManufacturer,   kAudioPlugInPropertyDeviceList,
	    kAudioPlugInPropertyResourceBundle,
	};
	for (size_t i = 0; i < sizeof(selectors) / sizeof(*selectors); i++) {
		checkProperty(s->driver, kAudioObjectPlugInObject,
		              Addr(selectors[i], kAudioObjectPropertyScopeGlobal), "plugin");
	}
}

static void deviceProperties(struct Session *s) {
	AudioObjectPropertySelector selectors[] = {
	    kAudioObjectPropertyBaseClass,       kAudioObjectPropertyClass,
	    kAudioObjectPropertyOwner,           kAudioObjectPropertyOwnedObjects,
	    kAudioObjectPropertyName,            kAudioObjectPropertyManufacturer,
	    kAudioDevicePropertyDeviceUID,       kAudioDevicePropertyModelUID,
	    kAudioDevicePropertyTransportType,   kAudioDevicePropertyRelatedDevices,
	    kAudioDevicePropertyClockDomain,     kAudioDevicePropertyDeviceIsAlive,
	    kAudioDevicePropertyDeviceIsRunning, kAudioDevicePropertyNominalSampleRate,
	    kAudioDevicePropertyAvailableNominalSampleRates,
	    kAudioDevicePropertyIsHidden,        kAudioDevicePropertyZeroTimeStampPeriod,
	    kAudioDevicePropertyStreams,         kAudioObjectPropertyControlList,
	};
	for (size_t i = 0; i < sizeof(selectors) / sizeof(*selectors); i++) {
		checkProperty(s->driver, kDevice,
		              Addr(selectors[i], kAudioObjectPropertyScopeGlobal), "device");
	}
}

// The zero-timestamp period IS the ring length: the fabricated clock
// ticks one cycle per lap, so geometry drift breaks every client's
// sample-time-to-ring mapping. Assert the value, not just the plumbing.
static void zeroTimeStampPeriod(struct Session *s) {
	AudioObjectPropertyAddress periodAddr =
	    Addr(kAudioDevicePropertyZeroTimeStampPeriod, kAudioObjectPropertyScopeGlobal);
	UInt32 period = 0, periodSize = 0;
	CHECK(s->iface->GetPropertyData(s->driver, kDevice, 0, &periodAddr, 0, NULL,
	                                sizeof(period), &periodSize, &period) == 0,
	      "GetPropertyData(ZeroTimeStampPeriod)");
	CHECK(period == kRing_Frames, "zero-timestamp period %u != ring frames %u", period,
	      (UInt32)kRing_Frames);
}

static void scopedDeviceProperties(struct Session *s) {
	AudioObjectPropertySelector selectors[] = {
	    kAudioDevicePropertyDeviceCanBeDefaultDevice,
	    kAudioDevicePropertyDeviceCanBeDefaultSystemDevice,
	    kAudioDevicePropertyLatency,
	    kAudioDevicePropertySafetyOffset,
	    kAudioDevicePropertyStreams,
	};
	AudioObjectPropertyScope scopes[2] = {kAudioObjectPropertyScopeInput,
	                                      kAudioObjectPropertyScopeOutput};
	const char *scopeLabels[2] = {"device/input", "device/output"};
	for (size_t scope = 0; scope < 2; scope++) {
		for (size_t i = 0; i < sizeof(selectors) / sizeof(*selectors); i++) {
			checkProperty(s->driver, kDevice, Addr(selectors[i], scopes[scope]),
			              scopeLabels[scope]);
		}
	}
}

static void streamProperties(struct Session *s) {
	AudioObjectID streams[2] = {kStreamIn, kStreamOut};
	AudioObjectPropertySelector selectors[] = {
	    kAudioObjectPropertyBaseClass,     kAudioObjectPropertyClass,
	    kAudioObjectPropertyOwner,         kAudioStreamPropertyIsActive,
	    kAudioStreamPropertyDirection,     kAudioStreamPropertyTerminalType,
	    kAudioStreamPropertyStartingChannel, kAudioStreamPropertyLatency,
	    kAudioStreamPropertyVirtualFormat, kAudioStreamPropertyPhysicalFormat,
	    kAudioStreamPropertyAvailableVirtualFormats,
	    kAudioStreamPropertyAvailablePhysicalFormats,
	};
	for (size_t stream = 0; stream < 2; stream++) {
		for (size_t i = 0; i < sizeof(selectors) / sizeof(*selectors); i++) {
			checkProperty(s->driver, streams[stream],
			              Addr(selectors[i], kAudioObjectPropertyScopeGlobal),
			              "stream");
		}
	}
}

// An unknown object is rejected with BadObject by all three property
// entry points — never answered from another object's map.
static void bogusObjectRejected(struct Session *s) {
	AudioObjectPropertyAddress nameAddr =
	    Addr(kAudioObjectPropertyName, kAudioObjectPropertyScopeGlobal);
	UInt32 bogusSize = 0;
	CFStringRef bogusName = NULL;
	CHECK(!s->iface->HasProperty(s->driver, 99, 0, &nameAddr),
	      "HasProperty answered for a bogus object");
	CHECK(s->iface->GetPropertyDataSize(s->driver, 99, 0, &nameAddr, 0, NULL, &bogusSize) ==
	          kAudioHardwareBadObjectError,
	      "GetPropertyDataSize did not reject a bogus object");
	CHECK(s->iface->GetPropertyData(s->driver, 99, 0, &nameAddr, 0, NULL, sizeof(bogusName),
	                                &bogusSize, &bogusName) == kAudioHardwareBadObjectError,
	      "GetPropertyData did not reject a bogus object");
}

static void streamDirections(struct Session *s) {
	UInt32 direction = 99, used = 0;
	AudioObjectPropertyAddress dir =
	    Addr(kAudioStreamPropertyDirection, kAudioObjectPropertyScopeGlobal);
	s->iface->GetPropertyData(s->driver, kStreamIn, 0, &dir, 0, NULL, sizeof(direction), &used,
	                          &direction);
	CHECK(direction == 1, "input stream direction = %u, want 1", direction);
	s->iface->GetPropertyData(s->driver, kStreamOut, 0, &dir, 0, NULL, sizeof(direction), &used,
	                          &direction);
	CHECK(direction == 0, "output stream direction = %u, want 0", direction);
}

static void translateUID(struct Session *s) {
	CFStringRef uid = CFSTR(kDevice_UID);
	AudioObjectID translated = 0;
	UInt32 used = 0;
	AudioObjectPropertyAddress translate =
	    Addr(kAudioPlugInPropertyTranslateUIDToDevice, kAudioObjectPropertyScopeGlobal);
	s->iface->GetPropertyData(s->driver, kAudioObjectPlugInObject, 0, &translate, sizeof(uid),
	                          &uid, sizeof(translated), &used, &translated);
	CHECK(translated == kDevice, "TranslateUIDToDevice = %u, want %u", translated, kDevice);
}

// The primary reader stays registered and running for every IO scenario
// below; its clock is the reference the later re-anchor checks compare to.
static void startPrimaryReader(struct Session *s) {
	addClient(s->driver, 1, "primary reader");
	CHECK(s->iface->StartIO(s->driver, kDevice, 1) == 0, "StartIO");

	s->sampleTime = -1;
	CHECK(s->iface->GetZeroTimeStamp(s->driver, kDevice, 1, &s->sampleTime, &s->hostTime,
	                                 &s->seed) == 0,
	      "GetZeroTimeStamp");
	CHECK(s->sampleTime >= 0 && s->seed == 1, "zero timestamp: sample %f seed %llu",
	      s->sampleTime, s->seed);

	Boolean willDo = false, inPlace = false;
	s->iface->WillDoIOOperation(s->driver, kDevice, 1, kAudioServerPlugInIOOperationReadInput,
	                            &willDo, &inPlace);
	CHECK(willDo && inPlace, "WillDo(ReadInput)");
	s->iface->WillDoIOOperation(s->driver, kDevice, 1, kAudioServerPlugInIOOperationWriteMix,
	                            &willDo, &inPlace);
	CHECK(willDo && inPlace, "WillDo(WriteMix)");
}

// A full-buffer loopback with sample-exact data, then a second read of the
// same span: reads never consume the ring, so every capture client hears the
// same signal.
static void loopback(struct Session *s) {
	for (int i = 0; i < kFrames * kChannels; i++) {
		s->out[i] = sinf((Float32)i * 0.01f);
		s->in[i] = -1;
	}

	memset(&s->cycle, 0, sizeof(s->cycle));
	s->cycle.mOutputTime.mSampleTime = 4096;
	s->cycle.mInputTime.mSampleTime = 4096;

	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamOut, 1,
	                              kAudioServerPlugInIOOperationWriteMix, kFrames, &s->cycle,
	                              s->out, NULL) == 0,
	      "WriteMix");
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                              kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle,
	                              s->in, NULL) == 0,
	      "ReadInput");
	CHECK(memcmp(s->out, s->in, sizeof(s->out)) == 0,
	      "loopback: input differs from what was written");

	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                              kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle,
	                              s->in, NULL) == 0,
	      "ReadInput (second)");
	CHECK(memcmp(s->out, s->in, sizeof(s->out)) == 0,
	      "second read differs: reads must not consume the ring");
}

// A delayed callback from an older generation cannot replace newer audio.
static void generationOrdering(struct Session *s) {
	s->cycle.mOutputTime.mSampleTime = 4096 + kRing_Frames;
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamOut, 1,
	                              kAudioServerPlugInIOOperationWriteMix, kFrames, &s->cycle,
	                              s->out, NULL) == 0,
	      "WriteMix (next generation)");
	s->cycle.mOutputTime.mSampleTime = 4096;
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamOut, 1,
	                              kAudioServerPlugInIOOperationWriteMix, kFrames, &s->cycle,
	                              s->out, NULL) == 0,
	      "WriteMix (delayed generation)");
	s->cycle.mInputTime.mSampleTime = 4096 + kRing_Frames;
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                              kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle,
	                              s->in, NULL) == 0,
	      "ReadInput (next generation)");
	CHECK(memcmp(s->out, s->in, sizeof(s->out)) == 0,
	      "delayed writer replaced the current ring generation");
}

// The same physical slots after multiple ring laps must not replay
// audio left by a writer that has stopped producing frames.
static void staleAudioAfterLaps(struct Session *s) {
	s->cycle.mInputTime.mSampleTime = 4096 + 3 * kRing_Frames;
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                              kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle,
	                              s->in, NULL) == 0,
	      "ReadInput (writer stopped after two laps)");

	Float32 sum = 0;
	for (int i = 0; i < kFrames * kChannels; i++) sum += fabsf(s->in[i]);
	CHECK(sum == 0, "stale audio replayed after two ring laps (residual %f)", sum);
}

// A frame range that has never been written is silent.
static void spanAheadOfWriter(struct Session *s) {
	s->cycle.mInputTime.mSampleTime = 4096 + kFrames;
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                              kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle,
	                              s->in, NULL) == 0,
	      "ReadInput (ahead of writer)");

	Float32 sum = 0;
	for (int i = 0; i < kFrames * kChannels; i++) sum += fabsf(s->in[i]);
	CHECK(sum == 0, "span ahead of the writer not cleared (residual %f)", sum);
}

// Pre-anchor frames are dropped, never shifted: a cycle straddling the
// anchor writes only its post-anchor tail, at the tail's own positions.
// StopIO/StartIO re-anchors first, so the ring is clear at generation 0.
static void preAnchorWriteDropped(struct Session *s) {
	CHECK(s->iface->StopIO(s->driver, kDevice, 1) == 0, "StopIO (re-anchor)");
	CHECK(s->iface->StartIO(s->driver, kDevice, 1) == 0, "StartIO (re-anchor)");
	s->cycle.mOutputTime.mSampleTime = -256;
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamOut, 1,
	                              kAudioServerPlugInIOOperationWriteMix, kFrames, &s->cycle,
	                              s->out, NULL) == 0,
	      "WriteMix (straddling the anchor)");
	s->cycle.mInputTime.mSampleTime = 0;
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                              kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle,
	                              s->in, NULL) == 0,
	      "ReadInput (after the anchor)");
	CHECK(memcmp(s->in, s->out + 256 * kChannels, 256 * kChannels * sizeof(Float32)) == 0,
	      "pre-anchor frames were shifted instead of dropped");

	Float32 sum = 0;
	for (int i = 256 * kChannels; i < kFrames * kChannels; i++) sum += fabsf(s->in[i]);
	CHECK(sum == 0, "pre-anchor write leaked past the drop (residual %f)", sum);
}

// A capture cycle straddling the anchor reads silence for the
// pre-anchor span and real audio for the rest.
static void preAnchorCaptureSilent(struct Session *s) {
	s->cycle.mInputTime.mSampleTime = -256;
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                              kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle,
	                              s->in, NULL) == 0,
	      "ReadInput (straddling the anchor)");

	Float32 sum = 0;
	for (int i = 0; i < 256 * kChannels; i++) sum += fabsf(s->in[i]);
	CHECK(sum == 0, "pre-anchor capture span not silent (residual %f)", sum);
	CHECK(memcmp(s->in + 256 * kChannels, s->out + 256 * kChannels,
	             256 * kChannels * sizeof(Float32)) == 0,
	      "post-anchor capture span mismatched");
}

// WriteMix accumulates: two writers at the same sample time sum. The extra
// clients stay registered for the concurrent scenarios that follow.
static void writersSum(struct Session *s) {
	for (UInt32 client = 2; client <= 8; client++) {
		addClient(s->driver, client, "concurrent writer");
		CHECK(s->iface->StartIO(s->driver, kDevice, client) == 0,
		      "StartIO (concurrent writer %u)", client);
	}

	s->cycle.mOutputTime.mSampleTime = 8192;
	s->cycle.mInputTime.mSampleTime = 8192;
	s->iface->DoIOOperation(s->driver, kDevice, kStreamOut, 1,
	                        kAudioServerPlugInIOOperationWriteMix, kFrames, &s->cycle, s->out,
	                        NULL);
	s->iface->DoIOOperation(s->driver, kDevice, kStreamOut, 2,
	                        kAudioServerPlugInIOOperationWriteMix, kFrames, &s->cycle, s->out,
	                        NULL);
	s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                        kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle, s->in,
	                        NULL);

	int mixed = 1;
	for (int i = 0; i < kFrames * kChannels; i++) {
		if (fabsf(s->in[i] - 2 * s->out[i]) > 1e-6f) {
			mixed = 0;
			break;
		}
	}
	CHECK(mixed, "two writers at one sample time do not sum");
}

// Concurrent clients must not lose additions or race the capture side.
// CoreAudio marks DoIOOperation nonblocking and does not promise that
// different clients share one caller thread.
static void concurrentWriters(struct Session *s) {
	enum { kWriters = 8 };
	pthread_t writers[kWriters];
	struct WriteTask tasks[kWriters];
	atomic_int ready = 0;
	atomic_bool start = false;

	s->cycle.mOutputTime.mSampleTime = 16384;
	s->cycle.mInputTime.mSampleTime = 16384;
	for (int i = 0; i < kWriters; i++) {
		tasks[i] = (struct WriteTask){s->driver, s->cycle, s->out, &ready, &start,
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

	s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                        kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle, s->in,
	                        NULL);
	for (int i = 0; i < kFrames * kChannels; i++) {
		CHECK(fabsf(s->in[i] - kWriters * s->out[i]) < 1e-5f,
		      "concurrent mix sample %d = %f, want %f", i, s->in[i], kWriters * s->out[i]);
	}
}

// A write crossing the ring edge reads back intact.
static void wrapAround(struct Session *s) {
	s->cycle.mOutputTime.mSampleTime = kRing_Frames - kFrames / 2;
	s->cycle.mInputTime.mSampleTime = kRing_Frames - kFrames / 2;
	s->iface->DoIOOperation(s->driver, kDevice, kStreamOut, 1,
	                        kAudioServerPlugInIOOperationWriteMix, kFrames, &s->cycle, s->out,
	                        NULL);
	s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                        kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle, s->in,
	                        NULL);
	CHECK(memcmp(s->out, s->in, sizeof(s->out)) == 0, "wrap-around loopback differs");
}

static void releaseConcurrentWriters(struct Session *s) {
	for (UInt32 client = 2; client <= 8; client++) {
		CHECK(s->iface->StopIO(s->driver, kDevice, client) == 0,
		      "StopIO (concurrent writer %u)", client);
		removeClient(s->driver, client, "concurrent writer");
	}
}

// Keep the reader alive while output clients enter and leave. StartIO for
// those writers must not re-anchor the shared clock: Apple requires the
// device to remain running as long as any one client is still running.
static void writerJoinKeepsClock(struct Session *s) {
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 1, &s->sampleTime, &s->hostTime,
	                           &s->readerSeed);
	addClient(s->driver, 90, "first transient writer");
	CHECK(s->iface->StartIO(s->driver, kDevice, 90) == 0, "StartIO (first transient writer)");

	UInt64 writerSeed = 0;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 90, &s->sampleTime, &s->hostTime,
	                           &writerSeed);
	CHECK(writerSeed == s->readerSeed,
	      "writer joining a live reader re-anchored clock: seed %llu -> %llu", s->readerSeed,
	      writerSeed);
}

// The realistic full-duplex timing: output is one callback ahead of input, so
// each input cycle consumes the preceding output cycle.
static void fixedInputOutputSkew(struct Session *s) {
	enum { kSkewCycles = 4 };
	Float32 skewBuffers[kSkewCycles][kFrames * kChannels];

	s->skewInputStart = 5 * kRing_Frames - kSkewFrames;
	for (int ioCycle = 0; ioCycle < kSkewCycles; ioCycle++) {
		for (int sample = 0; sample < kFrames * kChannels; sample++) {
			skewBuffers[ioCycle][sample] =
			    (Float32)(ioCycle + 1) * 0.05f + sinf((Float32)sample * 0.01f);
		}

		s->cycle.mOutputTime.mSampleTime =
		    (Float64)(s->skewInputStart + kSkewFrames + ioCycle * kFrames);
		s->cycle.mInputTime.mSampleTime =
		    (Float64)(s->skewInputStart + ioCycle * kFrames);
		memset(s->in, 0x7f, sizeof(s->in));

		CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamOut, 90,
		                              kAudioServerPlugInIOOperationWriteMix, kFrames,
		                              &s->cycle, skewBuffers[ioCycle], NULL) == 0,
		      "WriteMix (skew cycle %d)", ioCycle);
		CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
		                              kAudioServerPlugInIOOperationReadInput, kFrames,
		                              &s->cycle, s->in, NULL) == 0,
		      "ReadInput (skew cycle %d)", ioCycle);

		if (ioCycle == 0) {
			Float32 skewSum = 0;
			for (int sample = 0; sample < kFrames * kChannels; sample++) {
				skewSum += fabsf(s->in[sample]);
			}
			CHECK(skewSum == 0, "initial skew cycle was not silent (residual %f)",
			      skewSum);
		} else {
			CHECK(memcmp(skewBuffers[ioCycle - 1], s->in, sizeof(s->in)) == 0,
			      "fixed input/output skew lost cycle %d", ioCycle - 1);
		}
	}
}

static void writerLeaveKeepsClock(struct Session *s) {
	CHECK(s->iface->StopIO(s->driver, kDevice, 90) == 0, "StopIO (first transient writer)");
	removeClient(s->driver, 90, "first transient writer");

	UInt64 seedAfterWriter = 0;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 1, &s->sampleTime, &s->hostTime,
	                           &seedAfterWriter);
	CHECK(seedAfterWriter == s->readerSeed,
	      "writer leaving a live reader re-anchored clock: seed %llu -> %llu", s->readerSeed,
	      seedAfterWriter);
}

// Leave a multi-lap output gap, then attach another writer while the same
// reader remains active. The gap must be silent and the resumed writer must
// become audible on the next input cycle without requiring a global restart.
static void writerGapThenReplacement(struct Session *s) {
	UInt64 resumeInput = s->skewInputStart + 4 * kRing_Frames;
	s->cycle.mInputTime.mSampleTime = (Float64)resumeInput;
	memset(s->in, 0x7f, sizeof(s->in));
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                              kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle,
	                              s->in, NULL) == 0,
	      "ReadInput (writer gap)");
	Float32 gapSum = 0;
	for (int sample = 0; sample < kFrames * kChannels; sample++) {
		gapSum += fabsf(s->in[sample]);
	}
	CHECK(gapSum == 0, "writer gap replayed stale audio (residual %f)", gapSum);

	addClient(s->driver, 91, "replacement writer");
	CHECK(s->iface->StartIO(s->driver, kDevice, 91) == 0, "StartIO (replacement writer)");
	UInt64 replacementSeed = 0;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 91, &s->sampleTime, &s->hostTime,
	                           &replacementSeed);
	CHECK(replacementSeed == s->readerSeed,
	      "replacement writer re-anchored live reader: seed %llu -> %llu", s->readerSeed,
	      replacementSeed);

	s->cycle.mOutputTime.mSampleTime = (Float64)(resumeInput + kSkewFrames);
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamOut, 91,
	                              kAudioServerPlugInIOOperationWriteMix, kFrames, &s->cycle,
	                              s->out, NULL) == 0,
	      "WriteMix (replacement writer)");
	s->cycle.mInputTime.mSampleTime = (Float64)(resumeInput + kSkewFrames);
	memset(s->in, 0, sizeof(s->in));
	CHECK(s->iface->DoIOOperation(s->driver, kDevice, kStreamIn, 1,
	                              kAudioServerPlugInIOOperationReadInput, kFrames, &s->cycle,
	                              s->in, NULL) == 0,
	      "ReadInput (replacement writer)");
	CHECK(memcmp(s->out, s->in, sizeof(s->out)) == 0,
	      "replacement writer stayed silent while reader remained live");
	CHECK(s->iface->StopIO(s->driver, kDevice, 91) == 0, "StopIO (replacement writer)");
	removeClient(s->driver, 91, "replacement writer");
}

// Lifecycle callbacks are not part of DoIO, but the Host may deliver them
// from different control threads. Keep the primary reader running while
// independent clients repeatedly register, duplicate-start, duplicate-stop
// and unregister in parallel. The ledger must remain race-free and must not
// invent a last-stop/first-start transition under that contention.
static void concurrentLifecycle(struct Session *s) {
	enum { kLifecycleThreads = 16, kLifecycleIterations = 256 };
	pthread_t threads[kLifecycleThreads];
	struct LifecycleTask tasks[kLifecycleThreads];
	atomic_int ready = 0;
	atomic_bool start = false;

	for (UInt32 thread = 0; thread < kLifecycleThreads; thread++) {
		tasks[thread] = (struct LifecycleTask){
		    .driver = s->driver,
		    .client = Client(200 + thread),
		    .ready = &ready,
		    .start = &start,
		    .iterations = kLifecycleIterations,
		    .status = 0,
		};
		CHECK(pthread_create(&threads[thread], NULL, cycleLifecycleConcurrent,
		                     &tasks[thread]) == 0,
		      "pthread_create lifecycle client %u", tasks[thread].client.mClientID);
	}
	while (atomic_load_explicit(&ready, memory_order_acquire) != kLifecycleThreads) {
	}
	atomic_store_explicit(&start, true, memory_order_release);

	for (UInt32 thread = 0; thread < kLifecycleThreads; thread++) {
		CHECK(pthread_join(threads[thread], NULL) == 0,
		      "pthread_join lifecycle client %u", tasks[thread].client.mClientID);
		CHECK(tasks[thread].status == 0, "concurrent lifecycle client %u status = %d",
		      tasks[thread].client.mClientID, (int)tasks[thread].status);
	}

	UInt64 seedAfterStress = 0;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 1, &s->sampleTime, &s->hostTime,
	                           &seedAfterStress);
	CHECK(seedAfterStress == s->readerSeed,
	      "concurrent lifecycle re-anchored live reader: seed %llu -> %llu", s->readerSeed,
	      seedAfterStress);
}

// The clock advances monotonically across ring laps.
static void monotonicClock(struct Session *s) {
	usleep(50 * 1000);
	Float64 laterSample = -1;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 1, &laterSample, &s->hostTime, &s->seed);
	CHECK(laterSample >= s->sampleTime, "clock went backwards: %f then %f", s->sampleTime,
	      laterSample);
}

// A full stop -> start cycle re-anchors the device clock; the HAL only
// notices the discontinuity through a CHANGED seed. A constant seed here
// permanently desynchronizes clients that survive the restart.
static void restartReanchorsClock(struct Session *s) {
	CHECK(s->iface->StopIO(s->driver, kDevice, 1) == 0, "StopIO");

	UInt64 seedBefore = s->seed;
	CHECK(s->iface->StartIO(s->driver, kDevice, 1) == 0, "StartIO after restart");
	UInt64 seedAfter = 0;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 1, &s->sampleTime, &s->hostTime, &seedAfter);
	CHECK(seedAfter != seedBefore,
	      "re-anchor kept seed %llu: clients cannot detect the clock jump", seedAfter);
	CHECK(s->iface->StopIO(s->driver, kDevice, 1) == 0, "StopIO after restart");
	removeClient(s->driver, 1, "primary reader");
}

// StartIO and StopIO describe client state, not anonymous counter deltas.
// Duplicate delivery must be idempotent: otherwise an extra Start leaks a
// phantom runner, while an extra Stop can falsely create a 0->1 transition
// and re-anchor a reader that never left.
static void duplicateStartIsIdempotent(struct Session *s) {
	addClient(s->driver, 100, "duplicate-start reader");
	addClient(s->driver, 101, "duplicate-start writer");
	addClient(s->driver, 102, "post-duplicate-start client");
	CHECK(s->iface->StartIO(s->driver, kDevice, 100) == 0, "StartIO (duplicate-start reader)");
	UInt64 duplicateStartSeed = 0;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 100, &s->sampleTime, &s->hostTime,
	                           &duplicateStartSeed);
	CHECK(s->iface->StartIO(s->driver, kDevice, 101) == 0, "StartIO (duplicate-start writer)");
	CHECK(s->iface->StartIO(s->driver, kDevice, 101) == 0,
	      "StartIO (duplicate-start writer, duplicate)");
	CHECK(s->iface->StopIO(s->driver, kDevice, 101) == 0, "StopIO (duplicate-start writer)");
	CHECK(s->iface->StopIO(s->driver, kDevice, 100) == 0, "StopIO (duplicate-start reader)");
	CHECK(s->iface->StartIO(s->driver, kDevice, 102) == 0,
	      "StartIO (post-duplicate-start client)");
	UInt64 postDuplicateStartSeed = 0;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 102, &s->sampleTime, &s->hostTime,
	                           &postDuplicateStartSeed);
	CHECK(postDuplicateStartSeed != duplicateStartSeed,
	      "duplicate StartIO leaked a runner: full stop kept seed %llu",
	      postDuplicateStartSeed);
	CHECK(s->iface->StopIO(s->driver, kDevice, 102) == 0,
	      "StopIO (post-duplicate-start client)");
	// Balances the legacy counter implementation after the assertion above;
	// the ledger implementation intentionally treats it as a no-op.
	CHECK(s->iface->StopIO(s->driver, kDevice, 101) == 0,
	      "StopIO (duplicate-start legacy cleanup)");
	removeClient(s->driver, 100, "duplicate-start reader");
	removeClient(s->driver, 101, "duplicate-start writer");
	removeClient(s->driver, 102, "post-duplicate-start client");
}

static void duplicateStopIsIdempotent(struct Session *s) {
	addClient(s->driver, 110, "duplicate-stop reader");
	addClient(s->driver, 111, "duplicate-stop writer");
	addClient(s->driver, 112, "post-duplicate-stop writer");
	CHECK(s->iface->StartIO(s->driver, kDevice, 110) == 0, "StartIO (duplicate-stop reader)");
	UInt64 duplicateStopSeed = 0;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 110, &s->sampleTime, &s->hostTime,
	                           &duplicateStopSeed);
	CHECK(s->iface->StartIO(s->driver, kDevice, 111) == 0, "StartIO (duplicate-stop writer)");
	CHECK(s->iface->StopIO(s->driver, kDevice, 111) == 0, "StopIO (duplicate-stop writer)");
	CHECK(s->iface->StopIO(s->driver, kDevice, 111) == 0,
	      "StopIO (duplicate-stop writer, duplicate)");
	CHECK(s->iface->StartIO(s->driver, kDevice, 112) == 0,
	      "StartIO (post-duplicate-stop writer)");
	UInt64 postDuplicateStopSeed = 0;
	s->iface->GetZeroTimeStamp(s->driver, kDevice, 112, &s->sampleTime, &s->hostTime,
	                           &postDuplicateStopSeed);
	CHECK(postDuplicateStopSeed == duplicateStopSeed,
	      "duplicate StopIO re-anchored a live reader: seed %llu -> %llu", duplicateStopSeed,
	      postDuplicateStopSeed);
	CHECK(s->iface->StopIO(s->driver, kDevice, 112) == 0,
	      "StopIO (post-duplicate-stop writer)");
	CHECK(s->iface->StopIO(s->driver, kDevice, 110) == 0, "StopIO (duplicate-stop reader)");
	removeClient(s->driver, 110, "duplicate-stop reader");
	removeClient(s->driver, 111, "duplicate-stop writer");
	removeClient(s->driver, 112, "post-duplicate-stop writer");
}

// Fill every fixed ledger slot. Capacity exhaustion must fail cleanly,
// removing an active client must release its running state and slot, and
// both the same ID and a different ID must be reusable in that slot.
static void clientLedgerCapacity(struct Session *s) {
	for (UInt32 offset = 0; offset < kMaxIOClients; offset++) {
		UInt32 client = 1000 + offset;
		addClient(s->driver, client, "capacity client");
		CHECK(s->iface->StartIO(s->driver, kDevice, client) == 0,
		      "StartIO (capacity client %u)", client);
	}

	AudioServerPlugInClientInfo overflow = Client(2000);
	OSStatus overflowAdd = s->iface->AddDeviceClient(s->driver, kDevice, &overflow);
	CHECK(overflowAdd != 0, "full client ledger accepted client %u", overflow.mClientID);
	OSStatus overflowStart = s->iface->StartIO(s->driver, kDevice, overflow.mClientID);
	CHECK(overflowStart != 0, "unregistered overflow client started IO");
	if (overflowStart == 0) {
		CHECK(s->iface->StopIO(s->driver, kDevice, overflow.mClientID) == 0,
		      "StopIO (legacy overflow cleanup)");
	}

	AudioServerPlugInClientInfo reused = Client(1000);
	CHECK(s->iface->RemoveDeviceClient(s->driver, kDevice, &reused) == 0,
	      "RemoveDeviceClient (active reusable ID)");
	CHECK(s->iface->AddDeviceClient(s->driver, kDevice, &reused) == 0,
	      "AddDeviceClient (reused ID)");
	CHECK(s->iface->StartIO(s->driver, kDevice, reused.mClientID) == 0, "StartIO (reused ID)");
	CHECK(s->iface->RemoveDeviceClient(s->driver, kDevice, &reused) == 0,
	      "RemoveDeviceClient (reused active ID)");

	CHECK(s->iface->AddDeviceClient(s->driver, kDevice, &overflow) == 0,
	      "freed client slot rejected a different ID");
	CHECK(s->iface->StartIO(s->driver, kDevice, overflow.mClientID) == 0,
	      "StartIO (replacement capacity client)");
	CHECK(s->iface->RemoveDeviceClient(s->driver, kDevice, &overflow) == 0,
	      "RemoveDeviceClient (replacement active client)");

	for (UInt32 offset = 1; offset < kMaxIOClients; offset++) {
		UInt32 client = 1000 + offset;
		CHECK(s->iface->StopIO(s->driver, kDevice, client) == 0,
		      "StopIO (capacity client %u)", client);
		removeClient(s->driver, client, "capacity client");
	}
	// Legacy-counter cleanup. All are harmless idempotent stops with the
	// ledger, but they balance operations that the old driver accepted above.
	CHECK(s->iface->StopIO(s->driver, kDevice, reused.mClientID) == 0,
	      "StopIO (legacy reused-ID cleanup 1)");
	CHECK(s->iface->StopIO(s->driver, kDevice, reused.mClientID) == 0,
	      "StopIO (legacy reused-ID cleanup 2)");
	CHECK(s->iface->StopIO(s->driver, kDevice, overflow.mClientID) == 0,
	      "StopIO (legacy replacement cleanup)");
}

struct Scenario {
	const char *name;
	void (*run)(struct Session *);
};

static const struct Scenario kScenarios[] = {
    {"factory rejects a foreign type", factoryRejectsForeignType},
    {"COM identity", comIdentity},
    {"initialize", initialize},
    {"plug-in properties", pluginProperties},
    {"device properties", deviceProperties},
    {"zero-timestamp period", zeroTimeStampPeriod},
    {"scoped device properties", scopedDeviceProperties},
    {"stream properties", streamProperties},
    {"bogus object rejected", bogusObjectRejected},
    {"stream directions", streamDirections},
    {"translate UID to device", translateUID},
    {"primary reader starts", startPrimaryReader},
    {"loopback", loopback},
    {"generation ordering", generationOrdering},
    {"stale audio after laps", staleAudioAfterLaps},
    {"span ahead of the writer", spanAheadOfWriter},
    {"pre-anchor write dropped", preAnchorWriteDropped},
    {"pre-anchor capture silent", preAnchorCaptureSilent},
    {"writers sum", writersSum},
    {"concurrent writers", concurrentWriters},
    {"wrap-around", wrapAround},
    {"concurrent writers released", releaseConcurrentWriters},
    {"joining writer keeps the clock", writerJoinKeepsClock},
    {"fixed input/output skew", fixedInputOutputSkew},
    {"leaving writer keeps the clock", writerLeaveKeepsClock},
    {"writer gap then replacement", writerGapThenReplacement},
    {"concurrent lifecycle", concurrentLifecycle},
    {"monotonic clock", monotonicClock},
    {"restart re-anchors the clock", restartReanchorsClock},
    {"duplicate StartIO is idempotent", duplicateStartIsIdempotent},
    {"duplicate StopIO is idempotent", duplicateStopIsIdempotent},
    {"client ledger capacity", clientLedgerCapacity},
};

// openSession loads the bundle the way coreaudiod does: dlopen, the factory
// symbol, then the HAL plug-in type.
static int openSession(struct Session *s, const char *path) {
	void *handle = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	CHECK(handle != NULL, "dlopen: %s", dlerror());
	if (handle == NULL) return 0;

	s->factory = (FactoryFn)dlsym(handle, kFactoryName);
	CHECK(s->factory != NULL, "factory symbol missing");
	if (s->factory == NULL) return 0;

	s->driver = (AudioServerPlugInDriverRef)s->factory(kCFAllocatorDefault,
	                                                   kAudioServerPlugInTypeUUID);
	CHECK(s->driver != NULL, "factory returned NULL for the plug-in type");
	if (s->driver == NULL) return 0;

	s->iface = *s->driver;

	return 1;
}

int main(int argc, char **argv) {
	if (argc != 2) {
		fprintf(stderr, "usage: harness <path-to-driver-binary>\n");
		return 2;
	}

	static struct Session session;
	if (!openSession(&session, argv[1])) return 1;

	for (size_t i = 0; i < sizeof(kScenarios) / sizeof(*kScenarios); i++) {
		int before = gFailures;
		kScenarios[i].run(&session);
		if (gFailures > before) {
			fprintf(stderr, "scenario %s: %d failure(s)\n", kScenarios[i].name,
			        gFailures - before);
		}
	}

	if (gFailures == 0) {
		printf("harness: all driver contract checks PASS\n");
		return 0;
	}

	fprintf(stderr, "harness: %d failure(s)\n", gFailures);
	return 1;
}
