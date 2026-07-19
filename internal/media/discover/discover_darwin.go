//go:build darwin

package discover

/*
#cgo LDFLAGS: -framework CoreAudio -framework CoreFoundation

#include <CoreAudio/CoreAudio.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

// The enumeration walks kAudioHardwarePropertyDevices in system order —
// the same array ffmpeg's audiotoolbox muxer indexes, so a device's
// position here is exactly its -audio_device_index.

#define PRUKKA_MAX_DEVICES 128

static AudioDeviceID prukkaDevices[PRUKKA_MAX_DEVICES];
static char prukkaName[256];
static char prukkaUID[256];

// prukkaLoadDevices refreshes the device array and returns its length.
static int prukkaLoadDevices(void) {
	AudioObjectPropertyAddress addr = {
		kAudioHardwarePropertyDevices,
		kAudioObjectPropertyScopeGlobal,
		kAudioObjectPropertyElementMain,
	};

	UInt32 size = 0;
	if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &addr, 0, NULL, &size) != noErr) {
		return -1;
	}

	if (size > sizeof(prukkaDevices)) {
		return -1;
	}

	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, prukkaDevices) != noErr) {
		return -1;
	}

	return (int)(size / sizeof(AudioDeviceID));
}

// prukkaCopyString reads one CFString property into buf.
static int prukkaCopyString(AudioDeviceID dev, AudioObjectPropertySelector sel, char *buf, size_t len) {
	AudioObjectPropertyAddress addr = {sel, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};

	CFStringRef ref = NULL;
	UInt32 size = sizeof(ref);

	buf[0] = '\0';

	if (AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, &ref) != noErr || ref == NULL) {
		return -1;
	}

	Boolean ok = CFStringGetCString(ref, buf, (CFIndex)len, kCFStringEncodingUTF8);
	CFRelease(ref);

	return ok ? 0 : -1;
}

// prukkaOutputChannels sums the device's output stream channels.
static int prukkaOutputChannels(AudioDeviceID dev) {
	AudioObjectPropertyAddress addr = {
		kAudioDevicePropertyStreamConfiguration,
		kAudioObjectPropertyScopeOutput,
		kAudioObjectPropertyElementMain,
	};

	UInt32 size = 0;
	if (AudioObjectGetPropertyDataSize(dev, &addr, 0, NULL, &size) != noErr) {
		return -1;
	}
	if (size == 0) {
		return 0;
	}

	AudioBufferList *list = (AudioBufferList *)malloc(size);
	if (list == NULL) {
		return -1;
	}

	int channels = 0;
	if (AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, list) != noErr) {
		free(list);
		return -1;
	}
	for (UInt32 i = 0; i < list->mNumberBuffers; i++) {
		channels += (int)list->mBuffers[i].mNumberChannels;
	}

	free(list);

	return channels;
}

// prukkaDeviceInfo fills the static name/uid buffers for one array slot
// and returns its output channel count (-1 on lookup failure).
static int prukkaDeviceInfo(int index) {
	AudioDeviceID dev = prukkaDevices[index];

	if (prukkaCopyString(dev, kAudioObjectPropertyName, prukkaName, sizeof(prukkaName)) != 0) {
		return -1;
	}

	prukkaCopyString(dev, kAudioDevicePropertyDeviceUID, prukkaUID, sizeof(prukkaUID));

	return prukkaOutputChannels(dev);
}

static const char *prukkaNamePtr(void) { return prukkaName; }
static const char *prukkaUIDPtr(void) { return prukkaUID; }

// prukkaNominalRate reads one slot's nominal sample rate (0 on failure):
// the property another application rewrites when it reconfigures a device.
static double prukkaNominalRate(int index) {
	AudioObjectPropertyAddress addr = {
		kAudioDevicePropertyNominalSampleRate,
		kAudioObjectPropertyScopeGlobal,
		kAudioObjectPropertyElementMain,
	};

	Float64 rate = 0;
	UInt32 size = sizeof(rate);
	if (AudioObjectGetPropertyData(prukkaDevices[index], &addr, 0, NULL, &size, &rate) != noErr) {
		return 0;
	}

	return (double)rate;
}
*/
import "C"

import (
	"context"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// labeledAudioURL tags an audio device URL with a display-name rebinding hint:
// positional indexes reshuffle whenever any device appears or vanishes, while
// an unchanged, unique label lets consumers rebind at start time. The label is
// not an immutable hardware identifier.
func labeledAudioURL(id, label string) string {
	return "device://audio/" + id + "?label=" + neturl.QueryEscape(label)
}

// coreAudioMu guards the C side's static device and string buffers. CoreAudio
// property reads are synchronous and cannot be canceled, so only inventory
// refreshes take this lock; route construction reads an immutable snapshot.
var coreAudioMu sync.Mutex

type coreAudioOutput struct {
	label string
	uid   string
	index int
	rate  float64
}

type outputSnapshot struct {
	outputs []coreAudioOutput
}

// coreAudioCatalog keeps device routing off CoreAudio's uncancellable call
// path: callers use the last complete inventory (or their URL's embedded
// positional fallback) while one worker refreshes it.
// Process-lifetime by design: CoreAudio routing is machine-global state
// consumed through package-level resolvers with no owning instance.
var coreAudioCatalog = newCatalog(context.Background(), loadCoreAudioSnapshot)

// captureListBudget bounds the avfoundation listing on its own: a pending
// camera-consent decision can wedge it, and the native playback list must
// still come back promptly.
const captureListBudget = 2500 * time.Millisecond

// Devices enumerates capture sources through ffmpeg's avfoundation layer
// (its indexes are what device:// sources use) and playback targets
// through CoreAudio, whose array positions are audiotoolbox indexes.
func Devices(ctx context.Context, bin string) ([]Device, error) {
	var out []Device

	if bin != "" {
		listCtx, cancel := context.WithTimeout(ctx, captureListBudget)
		defer cancel()

		raw, err := ffmpeg.ListRaw(listCtx, bin, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
		if err == nil {
			audio, video := parseAVFoundation(raw)
			out = append(out, avAudioInputs(audio)...)

			for _, e := range video {
				out = append(out, Device{
					URL: "device://video/" + e.id, Label: e.label, Kind: VideoIn, Virtual: virtualLabel(e.label),
				})
			}
		}
	}

	out = append(out, coreAudioOutputs(ctx)...)

	return appendNativeVideoOutput(out, "Prukka Camera", ffmpeg.NativeVideoAvailable(ctx)), nil
}

// avAudioInputs includes a display-name rebinding hint only when the current
// AVFoundation inventory says that name is unique. With duplicate names the
// selected numeric index is safer than silently rebinding to the first match.
func avAudioInputs(entries []entry) []Device {
	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		counts[e.label]++
	}

	devices := make([]Device, 0, len(entries))
	for _, e := range entries {
		url := "device://audio/" + e.id
		if counts[e.label] == 1 {
			url = labeledAudioURL(e.id, e.label)
		}
		devices = append(devices, Device{
			URL: url, Label: e.label, Kind: AudioIn, Virtual: virtualLabel(e.label),
		})
	}

	return devices
}

// loadCoreAudioSnapshot walks every device with output channels. The index is
// its position in the full system array, exactly what audiotoolbox consumes.
// A failed native inventory is never published over the last complete one.
func loadCoreAudioSnapshot() (*outputSnapshot, bool) {
	coreAudioMu.Lock()
	defer coreAudioMu.Unlock()

	count := int(C.prukkaLoadDevices())
	if count < 0 {
		return nil, false
	}

	outputs := make([]coreAudioOutput, 0, count)
	for i := range count {
		channels := int(C.prukkaDeviceInfo(C.int(i)))
		if channels < 0 {
			return nil, false
		}
		if channels == 0 {
			continue
		}

		outputs = append(outputs, coreAudioOutput{
			index: i,
			rate:  float64(C.prukkaNominalRate(C.int(i))),
			label: C.GoString(C.prukkaNamePtr()),
			uid:   C.GoString(C.prukkaUIDPtr()),
		})
	}

	return &outputSnapshot{outputs: outputs}, true
}

func (s *outputSnapshot) unique(label string) (coreAudioOutput, bool) {
	var found coreAudioOutput
	matches := 0
	for _, output := range s.outputs {
		if output.label != label {
			continue
		}
		found = output
		matches++
		if matches > 1 {
			return coreAudioOutput{}, false
		}
	}

	return found, matches == 1
}

func outputFingerprint(output coreAudioOutput) string {
	return output.uid + "@" + strconv.FormatFloat(output.rate, 'f', -1, 64) +
		"#" + strconv.Itoa(output.index)
}

func (s *outputSnapshot) devices() []Device {
	if s == nil {
		return nil
	}

	labelCounts := make(map[string]int, len(s.outputs))
	for _, output := range s.outputs {
		labelCounts[output.label]++
	}

	devices := make([]Device, 0, len(s.outputs))
	for _, output := range s.outputs {
		id := strconv.Itoa(output.index)
		deviceURL := "device://audio/" + id
		if labelCounts[output.label] == 1 {
			deviceURL = labeledAudioURL(id, output.label)
		}
		devices = append(devices, Device{
			URL: deviceURL, Label: output.label, Kind: AudioOut,
			Virtual: virtualLabel(output.label) || strings.HasPrefix(output.uid, "Prukka"),
		})
	}

	return devices
}

// coreAudioOutputs lists the latest complete native inventory without calling
// CoreAudio on the enumerating goroutine. A warm cache returns immediately;
// only a cold start waits briefly for the single refresh worker before
// degrading to an empty playback list.
func coreAudioOutputs(ctx context.Context) []Device {
	waitCtx, cancel := context.WithTimeout(ctx, coldStartBudget)
	defer cancel()

	snapshot := coreAudioCatalog.currentWithin(waitCtx)

	return snapshot.devices()
}

// OutputStamp fingerprints a unique output label's current array index,
// hardware UID and nominal sample rate. It never enters CoreAudio: each call
// schedules a coalesced refresh and reads the last complete snapshot. An index
// change therefore repairs a sink initially opened from stale inventory, while
// a rate change repairs queues invalidated by another application. False means
// no published inventory yet, or an absent/ambiguous label.
func OutputStamp(label string) (string, bool) {
	snapshot := coreAudioCatalog.current()
	if snapshot == nil {
		return "", false
	}
	output, ok := snapshot.unique(label)
	if !ok || output.uid == "" || output.rate <= 0 {
		return "", false
	}

	return outputFingerprint(output), true
}

// OutputIndex returns a unique label's last published position in the system
// device array — the value audiotoolbox indexes. It schedules a refresh but
// never waits on CoreAudio, keeping device push admission bounded even if the
// native service wedges. Duplicate or unavailable labels deliberately fall
// back to the numeric index carried by the selected URL.
func OutputIndex(label string) (int, bool) {
	snapshot := coreAudioCatalog.current()
	if snapshot == nil {
		return 0, false
	}
	output, ok := snapshot.unique(label)

	return output.index, ok
}
